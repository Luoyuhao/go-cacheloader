// Package internal implements the shared read-only snapshot lifecycle.
package internal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
)

type snapshot[T any] struct {
	Version   int       `json:"version"`
	Revision  string    `json:"revision"`
	WrittenAt time.Time `json:"written_at"`
	Found     bool      `json:"found"`
	Value     T         `json:"value"`
}

type flight struct {
	done chan struct{}
	raw  string
	err  error
}

type Reader[T any] struct {
	cache         cacheloader.StringCacher
	load          cacheloader.LoadFunc[T]
	batch         cacheloader.BatchLoadFunc[T]
	cfg           cacheloader.Config
	mu            sync.Mutex
	flights       map[string]*flight
	closed        bool
	root          context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	requestWindow time.Time
	requests      int
}

func NewReader[T any](cache cacheloader.StringCacher, load cacheloader.LoadFunc[T], cfg cacheloader.Config, batch cacheloader.BatchLoadFunc[T]) (*Reader[T], error) {
	if cache == nil || load == nil {
		return nil, fmt.Errorf("cacheloader: cache and load function are required")
	}
	v := reflect.ValueOf(cache)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if v.IsNil() {
			return nil, fmt.Errorf("cacheloader: cache must not be typed nil")
		}
	}

	cfg, err := cfg.WithDefaults()
	if err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	return &Reader[T]{cache: cache, load: load, batch: batch, cfg: cfg, flights: make(map[string]*flight), root: root, cancel: cancel}, nil
}

// Close rejects new reads, cancels source work and waits for all admitted work.
// Backends and source functions must honor context for Close to finish promptly.
func (r *Reader[T]) Close() error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

func (r *Reader[T]) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return cacheloader.ErrClosed
	}
	return nil
}

func (r *Reader[T]) Get(ctx context.Context, key string) (value T, found bool, err error) {
	if err = r.admit(ctx); err != nil {
		return
	}
	return r.get(ctx, key)
}

func (r *Reader[T]) admit(ctx context.Context) error {
	if err := r.check(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return cacheloader.ErrClosed
	}
	if r.cfg.RequestsPerSecond == 0 {
		return nil
	}
	now := time.Now()
	if now.Sub(r.requestWindow) >= time.Second {
		r.requestWindow = now
		r.requests = 0
	}
	if r.requests >= r.cfg.RequestsPerSecond {
		return cacheloader.ErrDowngraded
	}
	r.requests++
	return nil
}

func (r *Reader[T]) get(ctx context.Context, key string) (value T, found bool, err error) {
	if err = r.check(ctx); err != nil {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, r.cfg.CacheTimeout)
	stop := context.AfterFunc(r.root, cancel)
	raw, cacheErr := safe(func() (string, error) { return r.cache.Get(cacheCtx, key) })
	if cacheErr == nil {
		cacheErr = cacheCtx.Err()
	}
	stop()
	cancel()
	if err = r.check(ctx); err != nil {
		return
	}
	var previous *string
	var fallback *snapshot[T]
	if cacheErr == nil {
		previous = &raw
		entry, decodeErr := decode[T](raw)
		if decodeErr == nil {
			if r.cfg.RefreshAfter == 0 || time.Since(entry.WrittenAt) < r.cfg.RefreshAfter {
				return entry.Value, entry.Found, nil
			}
			if !r.cfg.SyncRefresh {
				_, _ = r.start(ctx, key, previous)
				return entry.Value, entry.Found, nil
			}
			fallback = &entry
		} else {
			r.report(ctx, decodeErr)
		}
	} else if !errors.Is(cacheErr, cacheloader.ErrNotFound) {
		r.report(ctx, cacheErr)
	}
	f, err := r.start(ctx, key, previous)
	if err == nil {
		if err = r.waitForFlight(ctx, f); err != nil {
			return value, false, err
		}
		if f.err == nil {
			entry, decodeErr := decode[T](f.raw)
			return entry.Value, entry.Found, decodeErr
		}
		err = f.err
	} else if !errors.Is(err, cacheloader.ErrDowngraded) {
		return value, false, err
	}
	if fallback == nil {
		return value, false, err
	}
	if stateErr := r.check(ctx); stateErr != nil {
		return value, false, stateErr
	}
	return fallback.Value, fallback.Found, nil
}

func (r *Reader[T]) waitForFlight(ctx context.Context, f *flight) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.root.Done():
		return cacheloader.ErrClosed
	case <-f.done:
		return r.check(ctx)
	}
}

// start joins same-key work, reserving all new keys before spawning work.
func (r *Reader[T]) start(ctx context.Context, key string, previous *string) (*flight, error) {
	flights, err := r.startGroup(ctx, []request{{key: key, previous: previous}}, false)
	if err != nil {
		return nil, err
	}
	return flights[key], nil
}

type request struct {
	key      string
	previous *string
}
type work struct {
	request
	f *flight
}

func (r *Reader[T]) startGroup(ctx context.Context, requests []request, useBatch bool) (map[string]*flight, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, cacheloader.ErrClosed
	}
	newCount := 0
	for _, req := range requests {
		if r.flights[req.key] == nil {
			newCount++
		}
	}
	if len(r.flights)+newCount > r.cfg.MaxConcurrency {
		return nil, cacheloader.ErrDowngraded
	}
	flights := make(map[string]*flight, len(requests))
	owned := make([]work, 0, newCount)
	for _, req := range requests {
		f := r.flights[req.key]
		if f == nil {
			f = &flight{done: make(chan struct{})}
			r.flights[req.key] = f
			owned = append(owned, work{request: req, f: f})
		}
		flights[req.key] = f
	}
	if len(owned) > 0 {
		r.wg.Add(1)
		go r.runGroup(context.WithoutCancel(ctx), owned, useBatch)
	}
	return flights, nil
}

func (r *Reader[T]) runGroup(parent context.Context, owned []work, useBatch bool) {
	defer r.wg.Done()
	var groupErr error
	defer func() {
		if p := recover(); p != nil {
			groupErr = fmt.Errorf("cacheloader: source panic: %v", p)
		}
		if groupErr != nil {
			r.report(parent, groupErr)
		}
		for _, w := range owned {
			if w.f.raw == "" && w.f.err == nil {
				w.f.err = groupErr
			} else if w.f.err != nil {
				r.report(parent, fmt.Errorf("load snapshot %q: %w", w.key, w.f.err))
			}
		}
		r.mu.Lock()
		for _, w := range owned {
			delete(r.flights, w.key)
			close(w.f.done)
		}
		r.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(r.root, cancel)
	defer stop()
	defer cancel()
	// Recheck after reserving the keys: another flight may have filled a key
	// between our initial cache read and admission. Native batching is preserved.
	keys := make([]string, len(owned))
	for i, w := range owned {
		keys[i] = w.key
	}
	current := r.readMany(ctx, keys, useBatch)
	if err := ctx.Err(); err != nil {
		groupErr = err
		return
	}
	pending := make([]work, 0, len(owned))
	loadKeys := make([]string, 0, len(owned))
	for i, w := range owned {
		item := current[i]
		if item.Found && (w.previous == nil || item.Value != *w.previous) {
			if _, err := decode[T](item.Value); err == nil {
				w.f.raw = item.Value
				continue
			}
		}
		pending = append(pending, w)
		loadKeys = append(loadKeys, w.key)
	}
	if len(pending) == 0 {
		return
	}
	loadCtx, loadCancel := context.WithTimeout(ctx, r.cfg.LoadTimeout)
	defer loadCancel()
	values := make([]cacheloader.Result[T], len(pending))
	batched := useBatch && r.batch != nil
	if batched {
		values, groupErr = r.batch(loadCtx, append([]string(nil), loadKeys...))
		if groupErr == nil {
			groupErr = loadCtx.Err()
		}
		if groupErr != nil {
			return
		}
		if len(values) != len(pending) {
			groupErr = fmt.Errorf("cacheloader: batch loader returned %d results for %d keys", len(values), len(pending))
			return
		}
	} else {
		// A single-key callback yields a result only for its own flight.
		for i, w := range pending {
			values[i], w.f.err = safe(func() (cacheloader.Result[T], error) {
				if err := loadCtx.Err(); err != nil {
					return cacheloader.Result[T]{}, err
				}
				value, found, err := r.load(loadCtx, w.key)
				if err == nil {
					err = loadCtx.Err()
				}
				return cacheloader.Result[T]{Value: value, Found: found}, err
			})
		}
	}
	loadCancel()
	// Validate every result from a native batch before publishing any of it.
	// With single-key callbacks, encoding failures belong to their own flight.
	encoded := make([]string, len(values))
	for i, w := range pending {
		if w.f.err != nil {
			continue
		}
		raw, err := safe(func() (string, error) { return encode(values[i]) })
		if err != nil {
			if batched {
				groupErr = err
				return
			}
			w.f.err = err
			continue
		}
		encoded[i] = raw
	}
	for i, w := range pending {
		w.f.raw = encoded[i]
	}
	// Conditional writes stay per-key, sharing a bounded write-stage budget.
	writeCtx, writeCancel := context.WithTimeout(ctx, r.cfg.CacheTimeout)
	defer writeCancel()
	for i, w := range pending {
		if w.f.err != nil {
			continue
		}
		ttl := r.cfg.TTL
		if !values[i].Found {
			ttl = r.cfg.NegativeTTL
		}
		_, err := safe(func() (bool, error) {
			if err := writeCtx.Err(); err != nil {
				return false, err
			}
			return r.cache.CompareAndSwap(writeCtx, w.key, w.previous, encoded[i], ttl)
		})
		if err == nil {
			err = writeCtx.Err()
		}
		if err != nil {
			r.report(ctx, fmt.Errorf("cache write %q: %w", w.key, err))
		}
	}
}

func encode[T any](item cacheloader.Result[T]) (string, error) {
	if !item.Found {
		var zero T
		item.Value = zero
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	entry := snapshot[T]{Version: 1, Revision: hex.EncodeToString(id[:]), WrittenAt: time.Now(), Found: item.Found, Value: item.Value}
	raw, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("encode snapshot: %w", err)
	}
	if _, err := decode[T](string(raw)); err != nil {
		return "", err
	}
	return string(raw), nil
}

func decode[T any](raw string) (entry snapshot[T], err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("cacheloader: JSON decode panic: %v", p)
		}
	}()
	// The writer always includes both fields. RawMessage distinguishes an
	// absent value from an explicit JSON null belonging to a valid nil value.
	wire := struct {
		*snapshot[T]
		Found *bool           `json:"found"`
		Value json.RawMessage `json:"value"`
	}{snapshot: &entry}
	if err = json.Unmarshal([]byte(raw), &wire); err != nil {
		return entry, fmt.Errorf("decode snapshot: %w", err)
	}
	if entry.Version != 1 || entry.Revision == "" || entry.WrittenAt.IsZero() || entry.WrittenAt.After(time.Now()) {
		return entry, fmt.Errorf("decode snapshot: invalid metadata")
	}
	if wire.Found == nil || wire.Value == nil {
		return entry, fmt.Errorf("decode snapshot: required found or value field missing or invalid")
	}
	if err = json.Unmarshal(wire.Value, &entry.Value); err != nil {
		return entry, fmt.Errorf("decode snapshot: %w", err)
	}
	entry.Found = *wire.Found
	if !entry.Found {
		var zero T
		entry.Value = zero
	}
	return entry, nil
}

func safe[T any](fn func() (T, error)) (v T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("cacheloader: callback panic: %v", p)
		}
	}()
	return fn()
}

func (r *Reader[T]) report(ctx context.Context, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.ErrorContext(ctx, "cacheloader observer panic", "panic", p)
		}
	}()
	if r.cfg.OnError != nil {
		r.cfg.OnError(ctx, err)
	} else {
		slog.WarnContext(ctx, "cacheloader", "error", err)
	}
}
