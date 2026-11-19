package internal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
)

// readMany returns one cache observation per key. Failed/partial native batch
// responses are discarded; they must never become false successful zero values.
func (r *Reader[T]) readMany(ctx context.Context, keys []string, useBatch bool) []cacheloader.Result[string] {
	results := make([]cacheloader.Result[string], len(keys))
	cacheCtx, cancel := context.WithTimeout(ctx, r.cfg.CacheTimeout)
	defer cancel()
	stop := context.AfterFunc(r.root, cancel)
	defer stop()
	if batch, ok := r.cache.(cacheloader.BatchStringCacher); ok && useBatch {
		rows, err := safe(func() ([]cacheloader.Result[string], error) {
			return batch.MGet(cacheCtx, append([]string(nil), keys...)...)
		})
		if err == nil {
			err = cacheCtx.Err()
		}
		if err == nil && len(rows) != len(keys) {
			err = fmt.Errorf("cacheloader: batch cache returned %d results for %d keys", len(rows), len(keys))
		}
		if err != nil {
			r.report(ctx, err)
			return results
		}
		return rows
	}
	for i, key := range keys {
		raw, err := safe(func() (string, error) {
			if err := cacheCtx.Err(); err != nil {
				return "", err
			}
			return r.cache.Get(cacheCtx, key)
		})
		if err == nil {
			err = cacheCtx.Err()
		}
		if err != nil {
			if !errors.Is(err, cacheloader.ErrNotFound) {
				r.report(ctx, err)
			}
			continue
		}
		results[i] = cacheloader.Result[string]{Value: raw, Found: true}
	}
	return results
}

// MGet deduplicates keys and chunks the request, preserving original positions.
// Each chunk joins existing per-key flights and batches only newly owned misses.
func (r *Reader[T]) MGet(ctx context.Context, keys ...string) ([]cacheloader.Result[T], error) {
	if err := r.admit(ctx); err != nil {
		return nil, err
	}
	unique := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			unique = append(unique, key)
		}
	}
	rawByKey := make(map[string]string, len(unique))
	refreshes := make([]request, 0)
	size := min(r.cfg.BatchSize, r.cfg.MaxConcurrency)
	for offset := 0; offset < len(unique); offset += size {
		if err := r.check(ctx); err != nil {
			return nil, err
		}
		chunk := unique[offset:min(offset+size, len(unique))]
		rows := r.readMany(ctx, chunk, true)
		if err := r.check(ctx); err != nil {
			return nil, err
		}
		misses := make([]request, 0, len(chunk))

		for i, key := range chunk {
			var previous *string
			if rows[i].Found {
				raw := rows[i].Value
				previous = &raw
				entry, err := decode[T](raw)
				if err == nil {
					rawByKey[key] = raw
					if r.cfg.RefreshAfter > 0 && time.Since(entry.WrittenAt) >= r.cfg.RefreshAfter {
						refreshes = append(refreshes, request{key: key, previous: previous})
					}
					continue
				}
				r.report(ctx, err)
			}
			misses = append(misses, request{key: key, previous: previous})
		}
		flights, err := r.startGroup(ctx, misses, true)
		if err != nil {
			return nil, err
		}
		for _, req := range misses {
			f := flights[req.key]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-r.root.Done():
				return nil, cacheloader.ErrClosed
			case <-f.done:
				if err := r.check(ctx); err != nil {
					return nil, err
				}
				if f.err != nil {
					return nil, f.err
				}
				rawByKey[req.key] = f.raw
			}
		}
	}
	if err := r.check(ctx); err != nil {
		return nil, err
	}
	out := make([]cacheloader.Result[T], 0, len(keys))
	for _, key := range keys {
		entry, err := decode[T](rawByKey[key])
		if err != nil {
			return nil, err
		}
		out = append(out, cacheloader.Result[T]{Value: entry.Value, Found: entry.Found})
	}
	// Defer optional refresh until ALL foreground chunks finish, otherwise
	// refreshing a hit in the first chunk could starve a miss in a later chunk.
	for offset := 0; offset < len(refreshes); offset += size {
		_, _ = r.startGroup(ctx, refreshes[offset:min(offset+size, len(refreshes))], true)
	}
	return out, nil
}
