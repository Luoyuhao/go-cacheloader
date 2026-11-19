package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/stretchr/testify/require"
)

func TestAtomicReplacement(t *testing.T) {
	c, e := New(2)
	require.NoError(t, e)
	ctx := context.Background()
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := c.CompareAndSwap(ctx, "k", nil, "v1", 0)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	old := "v1"
	ok, e := c.CompareAndSwap(ctx, "k", &old, "v2", 0)
	require.NoError(t, e)
	require.True(t, ok)
	ok, e = c.CompareAndSwap(ctx, "k", &old, "late", 0)
	require.NoError(t, e)
	require.False(t, ok)
	old = "v2"
	ok, e = c.CompareAndSwap(ctx, "k", &old, "expiring", time.Nanosecond)
	require.NoError(t, e)
	require.True(t, ok)
	old = "expiring"
	ok, e = c.CompareAndSwap(ctx, "k", &old, "resurrected", 0)
	require.NoError(t, e)
	require.False(t, ok)
	_, e = c.Get(ctx, "k")
	require.ErrorIs(t, e, cacheloader.ErrNotFound)
}
func TestLRUAndCancellation(t *testing.T) {
	c, e := New(2)
	require.NoError(t, e)
	ctx := context.Background()
	_, _ = c.CompareAndSwap(ctx, "a", nil, "a", 0)
	_, _ = c.CompareAndSwap(ctx, "b", nil, "b", 0)
	_, e = c.Get(ctx, "a")
	require.NoError(t, e)
	_, _ = c.CompareAndSwap(ctx, "c", nil, "c", 0)
	_, e = c.Get(ctx, "b")
	require.ErrorIs(t, e, cacheloader.ErrNotFound)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, e = c.Get(canceled, "a")
	require.ErrorIs(t, e, context.Canceled)
	_, e = c.CompareAndSwap(canceled, "d", nil, "d", 0)
	require.ErrorIs(t, e, context.Canceled)
}

func TestReplacementResetsExpiration(t *testing.T) {
	c, e := New(1)
	require.NoError(t, e)
	ctx := context.Background()
	_, e = c.CompareAndSwap(ctx, "k", nil, "old", time.Millisecond)
	require.NoError(t, e)
	old := "old"
	ok, e := c.CompareAndSwap(ctx, "k", &old, "new", time.Hour)
	require.NoError(t, e)
	require.True(t, ok)
	c.mu.Lock()
	expiry := c.items["k"].Value.(entry).expires
	c.mu.Unlock()
	require.WithinDuration(t, time.Now().Add(time.Hour), expiry, time.Second)
	old = "new"
	ok, e = c.CompareAndSwap(ctx, "k", &old, "forever", 0)
	require.NoError(t, e)
	require.True(t, ok)
	c.mu.Lock()
	require.True(t, c.items["k"].Value.(entry).expires.IsZero())
	c.mu.Unlock()
}

func TestMGet(t *testing.T) {
	c, e := New(3)
	require.NoError(t, e)
	ctx := context.Background()
	_, _ = c.CompareAndSwap(ctx, "a", nil, "value", 0)
	_, _ = c.CompareAndSwap(ctx, "expired", nil, "old", time.Nanosecond)
	rows, e := c.MGet(ctx, "a", "missing", "a", "expired")
	require.NoError(t, e)
	require.Equal(t, []cacheloader.Result[string]{{Value: "value", Found: true}, {}, {Value: "value", Found: true}, {}}, rows)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, e = c.MGet(canceled, "a")
	require.ErrorIs(t, e, context.Canceled)
	rows, e = c.MGet(ctx)
	require.NoError(t, e)
	require.Empty(t, rows)
}
