package string_cacheloader

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/Luoyuhao/go-cacheloader/v2/memory"
	"github.com/stretchr/testify/require"
)

type backend struct {
	cacheloader.StringCacher
	get func(context.Context, string) (string, error)
	cas func(context.Context, string, *string, string, time.Duration) (bool, error)
}

func (b *backend) Get(c context.Context, k string) (string, error) {
	if b.get != nil {
		return b.get(c, k)
	}
	return b.StringCacher.Get(c, k)
}
func (b *backend) CompareAndSwap(c context.Context, k string, p *string, v string, d time.Duration) (bool, error) {
	if b.cas != nil {
		return b.cas(c, k, p, v, d)
	}
	return b.StringCacher.CompareAndSwap(c, k, p, v, d)
}
func mem(t *testing.T) *memory.Cache {
	t.Helper()
	m, e := memory.New(100)
	require.NoError(t, e)
	return m
}
func quiet(context.Context, error) {}
func reader[T any](t *testing.T, b cacheloader.StringCacher, f cacheloader.LoadFunc[T], cfg cacheloader.Config, opts ...Option[T]) *CacheLoader[T] {
	t.Helper()
	if cfg.OnError == nil {
		cfg.OnError = quiet
	}
	c, e := New(b, f, cfg, opts...)
	require.NoError(t, e)
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func TestReadAndBatch(t *testing.T) {
	for _, mode := range []string{"healthy", "offline", "corrupt", "panic"} {
		t.Run(mode, func(t *testing.T) {
			b := &backend{StringCacher: mem(t)}
			switch mode {
			case "offline":
				b.get = func(context.Context, string) (string, error) { return "", errors.New("offline") }
				b.cas = func(context.Context, string, *string, string, time.Duration) (bool, error) {
					return false, errors.New("offline")
				}
			case "corrupt":
				b.get = func(context.Context, string) (string, error) { return "broken", nil }
			case "panic":
				b.get = func(context.Context, string) (string, error) { panic("get") }
				b.cas = func(context.Context, string, *string, string, time.Duration) (bool, error) { panic("set") }
			}
			var calls atomic.Int32
			cl := reader(t, b, func(_ context.Context, k string) ([]int, bool, error) {
				calls.Add(1)
				if k == "missing" {
					return []int{99}, false, nil
				}
				return []int{1, 2}, true, nil
			}, cacheloader.Config{})
			rows, e := cl.MGet(context.Background(), "a", "missing", "a")
			require.NoError(t, e)
			require.Len(t, rows, 3)
			require.EqualValues(t, 2, calls.Load())
			require.True(t, rows[0].Found)
			require.False(t, rows[1].Found)
			require.Nil(t, rows[1].Value)
			rows[0].Value[0] = 99
			require.Equal(t, 1, rows[2].Value[0])
			if mode == "healthy" {
				_, _, e = cl.Get(context.Background(), "a")
				require.NoError(t, e)
				require.EqualValues(t, 2, calls.Load())
			}
		})
	}
}
func TestZeroAndNegativeCache(t *testing.T) {
	var calls atomic.Int32
	cl := reader(t, mem(t), func(_ context.Context, k string) (int, bool, error) { calls.Add(1); return 0, k != "missing", nil }, cacheloader.Config{NegativeTTL: 15 * time.Millisecond})
	for i := 0; i < 2; i++ {
		v, found, e := cl.Get(context.Background(), "zero")
		require.NoError(t, e)
		require.True(t, found)
		require.Zero(t, v)
		_, found, e = cl.Get(context.Background(), "missing")
		require.NoError(t, e)
		require.False(t, found)
	}
	require.EqualValues(t, 2, calls.Load())
	time.Sleep(20 * time.Millisecond)
	_, _, e := cl.Get(context.Background(), "missing")
	require.NoError(t, e)
	require.EqualValues(t, 3, calls.Load())
}
func TestSourceErrorsAndTimeout(t *testing.T) {
	boom := errors.New("source failed")
	for _, mode := range []string{"error", "panic", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			cl := reader(t, mem(t), func(ctx context.Context, _ string) (int, bool, error) {
				switch mode {
				case "error":
					return 0, false, boom
				case "panic":
					panic("source")
				default:
					<-ctx.Done()
					return 0, false, ctx.Err()
				}
			}, cacheloader.Config{LoadTimeout: 10 * time.Millisecond})
			for i := 0; i < 2; i++ {
				_, _, e := cl.Get(context.Background(), "k")
				require.Error(t, e)
				if mode == "error" {
					require.ErrorIs(t, e, boom)
				}
				if mode == "timeout" {
					require.ErrorIs(t, e, context.DeadlineExceeded)
				}
			}
		})
	}
}
func TestSingleflightCancellationAndCapacity(t *testing.T) {
	cache := mem(t)
	ok, err := cache.CompareAndSwap(context.Background(), "hot", nil,
		`{"version":1,"revision":"old","written_at":"2000-01-01T00:00:00Z","found":true,"value":[3]}`, 0)
	require.NoError(t, err)
	require.True(t, ok)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	cl := reader(t, cache, func(ctx context.Context, _ string) ([]int, bool, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return []int{7}, true, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}, cacheloader.Config{MaxConcurrency: 1, RefreshAfter: time.Hour})
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { _, _, e := cl.Get(first, "k"); firstDone <- e }()
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, found, e := cl.Get(context.Background(), "k")
			if e != nil || !found || v[0] != 7 {
				t.Errorf("unexpected result %v %v %v", v, found, e)
			}
		}()
	}
	_, _, e := cl.Get(context.Background(), "other")
	require.ErrorIs(t, e, cacheloader.ErrDowngraded)
	value, found, e := cl.Get(context.Background(), "hot")
	require.NoError(t, e)
	require.True(t, found)
	require.Equal(t, []int{3}, value)
	cancel()
	require.ErrorIs(t, <-firstDone, context.Canceled)
	close(release)
	wg.Wait()
	require.NoError(t, cl.Close())
	require.EqualValues(t, 1, calls.Load())
}
func TestCacheTimeoutLeavesSourceBudget(t *testing.T) {
	b := &backend{StringCacher: mem(t), get: func(ctx context.Context, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }}
	cl := reader(t, b, func(ctx context.Context, _ string) (int, bool, error) {
		require.NoError(t, ctx.Err())
		return 42, true, nil
	}, cacheloader.Config{CacheTimeout: 5 * time.Millisecond, LoadTimeout: time.Second})
	v, found, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	require.True(t, found)
	require.Equal(t, 42, v)
}
func TestRefreshResetsTTLAndSupportsNoExpiry(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			m := mem(t)
			var calls atomic.Int32
			var writes atomic.Int32
			var lastTTL atomic.Int64
			b := &backend{StringCacher: m, cas: func(ctx context.Context, k string, p *string, v string, d time.Duration) (bool, error) {
				ok, e := m.CompareAndSwap(ctx, k, p, v, d)
				if ok {
					lastTTL.Store(int64(d))
					writes.Add(1)
				}
				return ok, e
			}}
			cl := reader(t, b, func(context.Context, string) (int, bool, error) { return int(calls.Add(1)), true, nil }, cacheloader.Config{TTL: ttl, RefreshAfter: 5 * time.Millisecond})
			v, _, e := cl.Get(context.Background(), "k")
			require.NoError(t, e)
			require.Equal(t, 1, v)
			time.Sleep(10 * time.Millisecond)
			v, _, e = cl.Get(context.Background(), "k")
			require.NoError(t, e)
			require.Equal(t, 1, v)
			require.Eventually(t, func() bool { return writes.Load() == 2 }, time.Second, time.Millisecond)
			require.Equal(t, int64(ttl), lastTTL.Load())
			// A hit immediately after the successful write must not refresh again.
			_, _, e = cl.Get(context.Background(), "k")
			require.NoError(t, e)
		})
	}
}
func TestRefreshEmptyAndFailureRetry(t *testing.T) {
	var state atomic.Int32
	state.Store(1)
	failed := make(chan struct{}, 1)
	m := mem(t)
	cl := reader(t, m, func(context.Context, string) (int, bool, error) {
		s := state.Load()
		if s == 2 {
			return 0, false, errors.New("temporary")
		}
		return int(s), s != 0, nil
	}, cacheloader.Config{RefreshAfter: time.Millisecond, OnError: func(context.Context, error) {
		select {
		case failed <- struct{}{}:
		default:
		}
	}})
	_, _, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	time.Sleep(2 * time.Millisecond)
	state.Store(2)
	_, _, e = cl.Get(context.Background(), "k")
	require.NoError(t, e)
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("refresh error not observed")
	}
	raw, err := m.Get(context.Background(), "k")
	require.NoError(t, err)
	var cached struct{ Value int }
	require.NoError(t, json.Unmarshal([]byte(raw), &cached))
	require.Equal(t, 1, cached.Value)
	// Failed refresh must leave cached data intact and permit subsequent retry.
	require.Eventually(t, func() bool {
		state.Store(0)
		_, found, err := cl.Get(context.Background(), "k")
		return err == nil && !found
	}, time.Second, time.Millisecond)
}

func seedStaleSnapshot(t *testing.T, cache cacheloader.StringCacher, key, valueJSON string) string {
	t.Helper()
	raw := `{"version":1,"revision":"old","written_at":"2000-01-01T00:00:00Z","found":true,"value":` + valueJSON + `}`
	ok, err := cache.CompareAndSwap(context.Background(), key, nil, raw, 0)
	require.NoError(t, err)
	require.True(t, ok)
	return raw
}

func TestSyncRefreshReturnsOldValueOnFailureAndRetries(t *testing.T) {
	ctx := context.Background()
	m := mem(t)
	old := seedStaleSnapshot(t, m, "stale", "1")
	boom := errors.New("source failed")
	var calls int
	cl := reader(t, m, func(ctx context.Context, key string) (int, bool, error) {
		if key != "stale" {
			return 0, false, boom
		}
		calls++
		switch calls {
		case 1:
			return 0, false, boom
		case 2:
			<-ctx.Done()
			return 0, false, ctx.Err()
		default:
			return 2, true, nil
		}
	}, cacheloader.Config{RefreshAfter: 30 * time.Second, SyncRefresh: true, LoadTimeout: 50 * time.Millisecond})

	for range 2 {
		value, found, err := cl.Get(ctx, "stale")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, 1, value)
		raw, err := m.Get(ctx, "stale")
		require.NoError(t, err)
		require.Equal(t, old, raw)
	}
	for range 2 {
		value, found, err := cl.Get(ctx, "stale")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, 2, value)
	}
	require.Equal(t, 3, calls)
	_, _, err := cl.Get(ctx, "missing")
	require.ErrorIs(t, err, boom)
}

func TestLateRefreshCannotOverwriteOrResurrect(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "expiry"}[expire], func(t *testing.T) {
			m := mem(t)
			var calls atomic.Int32
			entered := make(chan struct{})
			release := make(chan struct{})
			written := make(chan bool, 1)
			b := &backend{StringCacher: m, cas: func(ctx context.Context, k string, p *string, v string, d time.Duration) (bool, error) {
				ok, e := m.CompareAndSwap(ctx, k, p, v, d)
				if p != nil {
					written <- ok
				}
				return ok, e
			}}
			cl := reader(t, b, func(context.Context, string) (int, bool, error) {
				if calls.Add(1) == 2 {
					close(entered)
					<-release
				}
				return int(calls.Load()), true, nil
			}, cacheloader.Config{RefreshAfter: time.Millisecond})
			_, _, e := cl.Get(context.Background(), "k")
			require.NoError(t, e)
			time.Sleep(2 * time.Millisecond)
			_, _, e = cl.Get(context.Background(), "k")
			require.NoError(t, e)
			<-entered
			old, e := m.Get(context.Background(), "k")
			require.NoError(t, e)
			ttl := time.Duration(0)
			if expire {
				ttl = time.Nanosecond
			}
			ok, e := m.CompareAndSwap(context.Background(), "k", &old, "newer", ttl)
			require.NoError(t, e)
			require.True(t, ok)
			close(release)
			select {
			case ok := <-written:
				require.False(t, ok, "stale CAS must be rejected")
			case <-time.After(time.Second):
				t.Fatal("refresh did not attempt CAS")
			}
			require.NoError(t, cl.Close())
			raw, e := m.Get(context.Background(), "k")
			if expire {
				require.ErrorIs(t, e, cacheloader.ErrNotFound)
			} else {
				require.NoError(t, e)
				require.Equal(t, "newer", raw)
			}
		})
	}
}
func TestCloseAndValidation(t *testing.T) {
	for _, cfg := range []cacheloader.Config{{TTL: -1}, {NegativeTTL: -1}, {RefreshAfter: -1}, {LoadTimeout: -1}, {CacheTimeout: -1}, {MaxConcurrency: -1}, {RequestsPerSecond: -1}} {
		_, e := New(mem(t), func(context.Context, string) (int, bool, error) { return 0, false, nil }, cfg)
		require.Error(t, e)
	}
	var nilCache *memory.Cache
	_, e := New(nilCache, func(context.Context, string) (int, bool, error) { return 0, false, nil }, cacheloader.Config{})
	require.Error(t, e)
	entered := make(chan struct{})
	ended := make(chan struct{})
	cl := reader(t, mem(t), func(ctx context.Context, _ string) (int, bool, error) {
		close(entered)
		<-ctx.Done()
		close(ended)
		return 0, false, ctx.Err()
	}, cacheloader.Config{})
	done := make(chan error, 1)
	go func() { _, _, err := cl.Get(context.Background(), "k"); done <- err }()
	<-entered
	require.NoError(t, cl.Close())
	<-ended
	require.ErrorIs(t, <-done, cacheloader.ErrClosed)
	_, _, e = cl.Get(context.Background(), "k")
	require.ErrorIs(t, e, cacheloader.ErrClosed)
}
func TestCorruptSnapshotRepairAndSourceOwnership(t *testing.T) {
	m := mem(t)
	_, e := m.CompareAndSwap(context.Background(), "k", nil, "invalid", 0)
	require.NoError(t, e)
	source := []int{1}
	cl := reader(t, m, func(context.Context, string) ([]int, bool, error) { return source, true, nil }, cacheloader.Config{})
	v, _, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	source[0] = 9
	require.Equal(t, 1, v[0])
	v[0] = 8
	v, _, e = cl.Get(context.Background(), "k")
	require.NoError(t, e)
	require.Equal(t, 1, v[0])
	raw, e := m.Get(context.Background(), "k")
	require.NoError(t, e)
	require.True(t, json.Valid([]byte(raw)))
}

func TestPerInstanceRequestLimit(t *testing.T) {
	cl := reader(t, mem(t), func(context.Context, string) (int, bool, error) { return 1, true, nil }, cacheloader.Config{RequestsPerSecond: 1})
	rows, e := cl.MGet(context.Background(), "a", "b")
	require.NoError(t, e)
	require.Len(t, rows, 2)
	_, _, e = cl.Get(context.Background(), "a")
	require.ErrorIs(t, e, cacheloader.ErrDowngraded)
	other := reader(t, mem(t), func(context.Context, string) (int, bool, error) { return 2, true, nil }, cacheloader.Config{RequestsPerSecond: 1})
	_, _, e = other.Get(context.Background(), "a")
	require.NoError(t, e)
}

func TestIncompleteSnapshotFallsBackAndRepairs(t *testing.T) {
	for _, fields := range []struct {
		name string
		raw  string
	}{
		{"missing value", `"found":true`},
		{"missing found", `"value":0`},
		{"null found", `"found":null,"value":0`},
	} {
		t.Run(fields.name, func(t *testing.T) {
			ctx := context.Background()
			cache := mem(t)
			stamp, err := json.Marshal(time.Now())
			require.NoError(t, err)
			broken := `{"version":1,"revision":"old","written_at":` + string(stamp) + `,` + fields.raw + `}`
			for _, key := range []string{"single", "batch"} {
				ok, err := cache.CompareAndSwap(ctx, key, nil, broken, 0)
				require.NoError(t, err)
				require.True(t, ok)
			}
			var loads atomic.Int32
			cl := reader(t, cache, func(context.Context, string) (int, bool, error) {
				loads.Add(1)
				return 42, true, nil
			}, cacheloader.Config{})

			value, found, err := cl.Get(ctx, "single")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, 42, value)
			for range 2 {
				values, err := cl.MGet(ctx, "single", "batch")
				require.NoError(t, err)
				require.Equal(t, []cacheloader.Result[int]{{Value: 42, Found: true}, {Value: 42, Found: true}}, values)
			}
			require.EqualValues(t, 2, loads.Load())
		})
	}
}

func TestExplicitNullSnapshotRemainsAHit(t *testing.T) {
	ctx := context.Background()
	var loads atomic.Int32
	cl := reader(t, mem(t), func(context.Context, string) (*int, bool, error) {
		loads.Add(1)
		return nil, true, nil
	}, cacheloader.Config{})
	for range 2 {
		value, found, err := cl.Get(ctx, "nil-value")
		require.NoError(t, err)
		require.True(t, found)
		require.Nil(t, value)
	}
	require.EqualValues(t, 1, loads.Load())
}

func TestDelayedMissRechecksCache(t *testing.T) {
	m := mem(t)
	var calls atomic.Int32
	var missOnce atomic.Bool
	b := &backend{StringCacher: m, get: func(ctx context.Context, k string) (string, error) {
		if missOnce.Swap(false) {
			return "", cacheloader.ErrNotFound
		}
		return m.Get(ctx, k)
	}}
	cl := reader(t, b, func(context.Context, string) (int, bool, error) { return int(calls.Add(1)), true, nil }, cacheloader.Config{})
	v, _, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	require.Equal(t, 1, v)
	// Simulate a delayed miss observation after another caller has populated k.
	missOnce.Store(true)
	v, _, e = cl.Get(context.Background(), "k")
	require.NoError(t, e)
	require.Equal(t, 1, v)
	require.EqualValues(t, 1, calls.Load())
}
