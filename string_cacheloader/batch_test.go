package string_cacheloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/stretchr/testify/require"
)

type batchBackend struct {
	*backend
	many func(context.Context, ...string) ([]cacheloader.Result[string], error)
}

func (b *batchBackend) MGet(ctx context.Context, keys ...string) ([]cacheloader.Result[string], error) {
	return b.many(ctx, keys...)
}

func TestOptionalBatchCapabilities(t *testing.T) {
	for _, cacheBatch := range []bool{false, true} {
		for _, sourceBatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("cache=%v/source=%v", cacheBatch, sourceBatch), func(t *testing.T) {
				ctx := context.Background()
				m := mem(t)
				var gets, mgets, loads, bloads atomic.Int32
				base := &backend{StringCacher: m, get: func(ctx context.Context, k string) (string, error) { gets.Add(1); return m.Get(ctx, k) }}
				var cache cacheloader.StringCacher = base
				if cacheBatch {
					cache = &batchBackend{backend: base, many: func(ctx context.Context, keys ...string) ([]cacheloader.Result[string], error) {
						mgets.Add(1)
						return m.MGet(ctx, keys...)
					}}
				}
				source := func(k string) cacheloader.Result[[]string] {
					return cacheloader.Result[[]string]{Value: []string{k}, Found: k != "absent"}
				}
				var opts []Option[[]string]
				if sourceBatch {
					opts = append(opts, WithBatchLoader(func(_ context.Context, keys []string) ([]cacheloader.Result[[]string], error) {
						bloads.Add(1)
						if fmt.Sprint(keys) != "[a absent b]" {
							t.Errorf("unexpected batch keys %v", keys)
						}
						out := make([]cacheloader.Result[[]string], len(keys))
						for i, k := range keys {
							out[i] = source(k)
						}
						return out, nil
					}))
				}
				cl := reader(t, cache, func(_ context.Context, k string) ([]string, bool, error) {
					loads.Add(1)
					v := source(k)
					return v.Value, v.Found, nil
				}, cacheloader.Config{}, opts...)
				_, _, e := cl.Get(ctx, "hot")
				require.NoError(t, e)
				gets.Store(0)
				loads.Store(0)
				rows, e := cl.MGet(ctx, "hot", "a", "absent", "hot", "b")
				require.NoError(t, e)
				require.Len(t, rows, 5)
				require.Equal(t, []string{"hot"}, rows[0].Value)
				require.Equal(t, []string{"a"}, rows[1].Value)
				require.False(t, rows[2].Found)
				require.Nil(t, rows[2].Value)
				require.Equal(t, []string{"b"}, rows[4].Value)
				rows[0].Value[0] = "changed"
				require.Equal(t, "hot", rows[3].Value[0])
				if cacheBatch {
					require.EqualValues(t, 0, gets.Load())
					require.EqualValues(t, 2, mgets.Load())
				} else {
					require.EqualValues(t, 7, gets.Load())
					require.Zero(t, mgets.Load())
				}
				if sourceBatch {
					require.EqualValues(t, 1, bloads.Load())
					require.Zero(t, loads.Load())
				} else {
					require.EqualValues(t, 3, loads.Load())
					require.Zero(t, bloads.Load())
				}
				_, e = cl.MGet(ctx, "hot", "a", "absent", "b")
				require.NoError(t, e)
				if sourceBatch {
					require.EqualValues(t, 1, bloads.Load())
				} else {
					require.EqualValues(t, 3, loads.Load())
				}
			})
		}
	}
}

func TestBatchCacheFailureFallsBack(t *testing.T) {
	for _, mode := range []string{"error-with-partial", "length", "panic", "timeout", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			m := mem(t)
			var called atomic.Int32
			b := &batchBackend{backend: &backend{StringCacher: m}, many: func(ctx context.Context, keys ...string) ([]cacheloader.Result[string], error) {
				switch mode {
				case "error-with-partial":
					return []cacheloader.Result[string]{{Value: "bad", Found: true}}, errors.New("offline")
				case "length":
					return nil, nil
				case "panic":
					panic("cache batch")
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				default:
					return []cacheloader.Result[string]{{Value: "broken", Found: true}, {Value: "broken", Found: true}}, nil
				}
			}}
			cl := reader(t, b, func(context.Context, string) (int, bool, error) { t.Error("single source used"); return 0, false, nil }, cacheloader.Config{CacheTimeout: 5 * time.Millisecond}, WithBatchLoader(func(ctx context.Context, keys []string) ([]cacheloader.Result[int], error) {
				called.Add(1)
				if ctx.Err() != nil {
					t.Error("cache timeout consumed source budget")
				}
				return []cacheloader.Result[int]{{Value: 1, Found: true}, {Value: 2, Found: true}}, nil
			}))
			rows, e := cl.MGet(context.Background(), "a", "b")
			require.NoError(t, e)
			require.EqualValues(t, 1, called.Load())
			require.Equal(t, 1, rows[0].Value)
			require.Equal(t, 2, rows[1].Value)
		})
	}
}

func TestBatchSourceFailureDoesNotPoisonCache(t *testing.T) {
	boom := errors.New("source down")
	for _, mode := range []string{"error", "length", "panic", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			m := mem(t)
			var calls atomic.Int32
			cl := reader(t, m, func(context.Context, string) (int, bool, error) { return 0, false, nil }, cacheloader.Config{LoadTimeout: 5 * time.Millisecond, MaxConcurrency: 2}, WithBatchLoader(func(ctx context.Context, keys []string) ([]cacheloader.Result[int], error) {
				if calls.Add(1) > 1 {
					return []cacheloader.Result[int]{{Value: 1, Found: true}, {Found: false}}, nil
				}
				switch mode {
				case "error":
					return []cacheloader.Result[int]{{Value: 9, Found: true}}, boom
				case "length":
					return nil, nil
				case "panic":
					panic("source batch")
				default:
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}))
			_, e := cl.MGet(context.Background(), "a", "b")
			require.Error(t, e)
			if mode == "error" {
				require.ErrorIs(t, e, boom)
			}
			if mode == "timeout" {
				require.ErrorIs(t, e, context.DeadlineExceeded)
			}
			_, e = m.Get(context.Background(), "a")
			require.ErrorIs(t, e, cacheloader.ErrNotFound)
			rows, e := cl.MGet(context.Background(), "a", "b")
			require.NoError(t, e)
			require.Equal(t, 1, rows[0].Value)
			require.False(t, rows[1].Found)
		})
	}
}

func TestBatchChunksAndAdmission(t *testing.T) {
	m := mem(t)
	var sizes []int
	cl := reader(t, m, func(context.Context, string) (string, bool, error) { return "", false, nil }, cacheloader.Config{BatchSize: 10, MaxConcurrency: 2}, WithBatchLoader(func(_ context.Context, keys []string) ([]cacheloader.Result[string], error) {
		sizes = append(sizes, len(keys))
		out := make([]cacheloader.Result[string], len(keys))
		for i, k := range keys {
			out[i] = cacheloader.Result[string]{Value: k, Found: true}
		}
		return out, nil
	}))
	rows, e := cl.MGet(context.Background(), "a", "b", "a", "c", "d", "e")
	require.NoError(t, e)
	require.Equal(t, []int{2, 2, 1}, sizes)
	require.Equal(t, "a", rows[2].Value)
	require.Equal(t, "e", rows[5].Value)
}

func TestBatchSharesFlightsWithGetAndOtherBatch(t *testing.T) {
	m := mem(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cl := reader(t, m, func(context.Context, string) (string, bool, error) {
		t.Error("unexpected single source")
		return "", false, nil
	}, cacheloader.Config{MaxConcurrency: 2}, WithBatchLoader(func(ctx context.Context, keys []string) ([]cacheloader.Result[string], error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []cacheloader.Result[string]{{Value: "a", Found: true}, {Value: "b", Found: true}}, nil
	}))
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { _, e := cl.MGet(first, "a", "b"); firstDone <- e }()
	<-entered
	// A batch needing a third key must fail admission without starting partial work.
	_, e := cl.MGet(context.Background(), "a", "c")
	require.ErrorIs(t, e, cacheloader.ErrDowngraded)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				v, found, e := cl.Get(context.Background(), "a")
				if e != nil || !found || v != "a" {
					t.Errorf("Get: %v %v %v", v, found, e)
				}
			} else {
				v, e := cl.MGet(context.Background(), "b", "a")
				if e != nil || len(v) != 2 || v[0].Value != "b" {
					t.Errorf("MGet: %v %v", v, e)
				}
			}
		}(i)
	}
	cancel()
	require.ErrorIs(t, <-firstDone, context.Canceled)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestBatchJoinsExistingSingleFlight(t *testing.T) {
	m := mem(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	batchKeys := make(chan []string, 1)
	cl := reader(t, m, func(ctx context.Context, k string) (string, bool, error) {
		close(entered)
		select {
		case <-release:
			return k, true, nil
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}, cacheloader.Config{}, WithBatchLoader(func(_ context.Context, keys []string) ([]cacheloader.Result[string], error) {
		batchKeys <- keys
		return []cacheloader.Result[string]{{Value: "b", Found: true}}, nil
	}))
	getDone := make(chan error, 1)
	go func() { _, _, e := cl.Get(context.Background(), "a"); getDone <- e }()
	<-entered
	batchDone := make(chan error, 1)
	go func() { _, e := cl.MGet(context.Background(), "a", "b"); batchDone <- e }()
	select {
	case keys := <-batchKeys:
		require.Equal(t, []string{"b"}, keys)
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	close(release)
	require.NoError(t, <-getDone)
	require.NoError(t, <-batchDone)
}

func TestBatchClose(t *testing.T) {
	entered := make(chan struct{})
	stopped := make(chan struct{})
	cl := reader(t, mem(t), func(context.Context, string) (int, bool, error) { return 0, false, nil }, cacheloader.Config{}, WithBatchLoader(func(ctx context.Context, keys []string) ([]cacheloader.Result[int], error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}))
	done := make(chan error, 1)
	go func() { _, e := cl.MGet(context.Background(), "a", "b"); done <- e }()
	<-entered
	require.NoError(t, cl.Close())
	<-stopped
	require.ErrorIs(t, <-done, cacheloader.ErrClosed)
}

func TestBatchValidation(t *testing.T) {
	source := func(context.Context, string) (int, bool, error) { return 0, false, nil }
	_, e := New(mem(t), source, cacheloader.Config{}, WithBatchLoader[int](nil))
	require.Error(t, e)
	_, e = New(mem(t), source, cacheloader.Config{}, nil)
	require.Error(t, e)
	_, e = New(mem(t), source, cacheloader.Config{BatchSize: -1})
	require.Error(t, e)
}

func TestBatchRefresh(t *testing.T) {
	m := mem(t)
	refreshed := make(chan []string, 1)
	cl := reader(t, m, func(context.Context, string) (int, bool, error) { return 1, true, nil }, cacheloader.Config{RefreshAfter: time.Millisecond}, WithBatchLoader(func(_ context.Context, keys []string) ([]cacheloader.Result[int], error) {
		select {
		case refreshed <- keys:
		default:
		}
		return []cacheloader.Result[int]{{Value: 2, Found: true}, {Value: 3, Found: true}}, nil
	}))
	_, _, e := cl.Get(context.Background(), "a")
	require.NoError(t, e)
	_, _, e = cl.Get(context.Background(), "b")
	require.NoError(t, e)
	time.Sleep(2 * time.Millisecond)
	rows, e := cl.MGet(context.Background(), "a", "b")
	require.NoError(t, e)
	require.Equal(t, 1, rows[0].Value)
	require.Equal(t, 1, rows[1].Value)
	select {
	case keys := <-refreshed:
		require.Equal(t, []string{"a", "b"}, keys)
	case <-time.After(time.Second):
		t.Fatal("refresh not batched")
	}
	// Inspect the backend directly to avoid initiating another refresh in the assertion.
	require.Eventually(t, func() bool {
		raw, err := m.MGet(context.Background(), "a", "b")
		if err != nil {
			return false
		}
		var a, b struct{ Value int }
		return json.Unmarshal([]byte(raw[0].Value), &a) == nil && json.Unmarshal([]byte(raw[1].Value), &b) == nil && a.Value == 2 && b.Value == 3
	}, time.Second, time.Millisecond)
}

func TestBatchCacheErrorDiscardsValidPartialData(t *testing.T) {
	m := mem(t)
	seed := reader(t, m, func(context.Context, string) (int, bool, error) { return 99, true, nil }, cacheloader.Config{})
	_, _, e := seed.Get(context.Background(), "a")
	require.NoError(t, e)
	b := &batchBackend{backend: &backend{StringCacher: m}, many: func(ctx context.Context, keys ...string) ([]cacheloader.Result[string], error) {
		rows, _ := m.MGet(ctx, keys...)
		return rows, errors.New("partial response")
	}}
	var keysSeen []string
	cl := reader(t, b, func(context.Context, string) (int, bool, error) { return 0, false, nil }, cacheloader.Config{}, WithBatchLoader(func(_ context.Context, keys []string) ([]cacheloader.Result[int], error) {
		keysSeen = keys
		return []cacheloader.Result[int]{{Value: 1, Found: true}, {Value: 2, Found: true}}, nil
	}))
	rows, e := cl.MGet(context.Background(), "a", "b")
	require.NoError(t, e)
	require.Equal(t, []string{"a", "b"}, keysSeen)
	require.Equal(t, 1, rows[0].Value)
	// Unknown cache state must not cause an unconditional overwrite of a.
	v, _, e := seed.Get(context.Background(), "a")
	require.NoError(t, e)
	require.Equal(t, 99, v)
}

func TestBatchRefreshCannotStarveLaterMisses(t *testing.T) {
	cl := reader(t, mem(t), func(context.Context, string) (int, bool, error) { return 1, true, nil }, cacheloader.Config{BatchSize: 1, MaxConcurrency: 1, RefreshAfter: time.Millisecond}, WithBatchLoader(func(ctx context.Context, keys []string) ([]cacheloader.Result[int], error) {
		if keys[0] == "a" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []cacheloader.Result[int]{{Value: 2, Found: true}}, nil
	}))
	_, _, e := cl.Get(context.Background(), "a")
	require.NoError(t, e)
	time.Sleep(2 * time.Millisecond)
	rows, e := cl.MGet(context.Background(), "a", "b")
	require.NoError(t, e)
	require.Equal(t, 1, rows[0].Value)
	require.Equal(t, 2, rows[1].Value)
}
