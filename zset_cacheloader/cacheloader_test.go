package zset_cacheloader

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/Luoyuhao/go-cacheloader/v2/memory"
	"github.com/stretchr/testify/require"
)

func newTest(t *testing.T, load LoadFunc, cfg cacheloader.Config) *CacheLoader {
	t.Helper()
	m, e := memory.New(100)
	require.NoError(t, e)
	cfg.OnError = func(context.Context, error) {}
	cl, e := New(m, load, cfg)
	require.NoError(t, e)
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}
func TestSnapshotRanges(t *testing.T) {
	var calls atomic.Int32
	cl := newTest(t, func(context.Context, string) ([]cacheloader.Z, error) {
		calls.Add(1)
		return []cacheloader.Z{{Score: 3, Member: "c"}, {Score: 1, Member: "b"}, {Score: 1, Member: "a"}, {Score: 2, Member: "c"}}, nil
	}, cacheloader.Config{})
	want := []cacheloader.Z{{Score: 1, Member: "a"}, {Score: 1, Member: "b"}, {Score: 2, Member: "c"}}
	rows, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	require.Equal(t, want, rows)
	rows[0].Score = 999
	for _, tc := range []struct {
		start, stop int64
		reverse     bool
		want        []cacheloader.Z
	}{
		{0, -1, false, want}, {0, 0, false, want[:1]}, {-99, 99, false, want}, {-2, -1, true, []cacheloader.Z{want[1], want[0]}},
		{3, 9, false, []cacheloader.Z{}}, {2, 1, false, []cacheloader.Z{}}, {0, -4, false, []cacheloader.Z{}}, {math.MaxInt64, math.MaxInt64, false, []cacheloader.Z{}},
	} {
		rows, e = cl.ZRange(context.Background(), "k", tc.start, tc.stop, tc.reverse)
		require.NoError(t, e)
		require.Equal(t, tc.want, rows)
	}
	n, e := cl.ZCard(context.Background(), "k")
	require.NoError(t, e)
	require.EqualValues(t, 3, n)
	require.EqualValues(t, 1, calls.Load())
}
func TestEmptySnapshotAndExpiry(t *testing.T) {
	var calls atomic.Int32
	cl := newTest(t, func(context.Context, string) ([]cacheloader.Z, error) { calls.Add(1); return nil, nil }, cacheloader.Config{NegativeTTL: 5 * time.Millisecond})
	for i := 0; i < 2; i++ {
		rows, e := cl.Get(context.Background(), "k")
		require.NoError(t, e)
		require.NotNil(t, rows)
		require.Empty(t, rows)
	}
	require.EqualValues(t, 1, calls.Load())
	time.Sleep(10 * time.Millisecond)
	_, e := cl.ZCard(context.Background(), "k")
	require.NoError(t, e)
	require.EqualValues(t, 2, calls.Load())
}
func TestRefreshReplacesEntireSnapshot(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	cl := newTest(t, func(context.Context, string) ([]cacheloader.Z, error) {
		switch version.Load() {
		case 1:
			return []cacheloader.Z{{Score: 1, Member: "old"}, {Score: 2, Member: "deleted"}}, nil
		case 2:
			return []cacheloader.Z{{Score: 8, Member: "new"}}, nil
		default:
			return nil, nil
		}
	}, cacheloader.Config{RefreshAfter: time.Millisecond})
	_, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	version.Store(2)
	time.Sleep(2 * time.Millisecond)
	rows, e := cl.Get(context.Background(), "k")
	require.NoError(t, e)
	require.Len(t, rows, 2)
	require.Eventually(t, func() bool {
		r, e := cl.Get(context.Background(), "k")
		return e == nil && len(r) == 1 && r[0].Member == "new"
	}, time.Second, time.Millisecond)
	version.Store(3)
	require.Eventually(t, func() bool { n, e := cl.ZCard(context.Background(), "k"); return e == nil && n == 0 }, time.Second, time.Millisecond)
	version.Store(2)
	require.Eventually(t, func() bool { n, e := cl.ZCard(context.Background(), "k"); return e == nil && n == 1 }, time.Second, time.Millisecond)
}

func TestSyncRefreshAppliesToSnapshotViews(t *testing.T) {
	ctx := context.Background()
	m, err := memory.New(10)
	require.NoError(t, err)
	ok, err := m.CompareAndSwap(ctx, "ranking", nil,
		`{"version":1,"revision":"old","written_at":"2000-01-01T00:00:00Z","found":true,"value":[{"member":"old","score":1}]}`, 0)
	require.NoError(t, err)
	require.True(t, ok)
	var calls int
	want := []cacheloader.Z{{Member: "new", Score: 2}}
	cl, err := New(m, func(context.Context, string) ([]cacheloader.Z, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("source failed")
		}
		return want, nil
	}, cacheloader.Config{RefreshAfter: 30 * time.Second, SyncRefresh: true, OnError: func(context.Context, error) {}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })

	rows, err := cl.ZRange(ctx, "ranking", 0, 0, false)
	require.NoError(t, err)
	require.Equal(t, []cacheloader.Z{{Member: "old", Score: 1}}, rows)
	rows, err = cl.ZRange(ctx, "ranking", 0, 0, false)
	require.NoError(t, err)
	require.Equal(t, want, rows)
	require.Equal(t, 2, calls)
}

func TestInvalidScoresAndSourceError(t *testing.T) {
	for _, score := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		cl := newTest(t, func(context.Context, string) ([]cacheloader.Z, error) {
			return []cacheloader.Z{{Score: score, Member: "bad"}}, nil
		}, cacheloader.Config{})
		_, e := cl.Get(context.Background(), "k")
		require.Error(t, e)
	}
	boom := errors.New("source failed")
	cl := newTest(t, func(context.Context, string) ([]cacheloader.Z, error) { return nil, boom }, cacheloader.Config{})
	_, e := cl.ZCard(context.Background(), "k")
	require.ErrorIs(t, e, boom)
}

func TestInvalidUTF8MembersRejectSnapshot(t *testing.T) {
	ctx := context.Background()
	cache, err := memory.New(10)
	require.NoError(t, err)
	valid := []cacheloader.Z{{Score: 1, Member: "é"}, {Score: 1, Member: "�"}}
	var loads atomic.Int32
	cl, err := New(cache, func(context.Context, string) ([]cacheloader.Z, error) {
		if loads.Add(1) == 1 {
			return []cacheloader.Z{{Score: 1, Member: "\xff"}, {Score: 1, Member: "\xfe"}}, nil
		}
		return valid, nil
	}, cacheloader.Config{OnError: func(context.Context, error) {}})
	require.NoError(t, err)
	defer cl.Close()

	rows, err := cl.Get(ctx, "ranking")
	require.ErrorContains(t, err, "UTF-8")
	require.Nil(t, rows)
	_, err = cache.Get(ctx, "ranking")
	require.ErrorIs(t, err, cacheloader.ErrNotFound)
	for range 2 {
		rows, err := cl.ZRange(ctx, "ranking", 0, -1, false)
		require.NoError(t, err)
		require.Equal(t, valid, rows)
	}
	require.EqualValues(t, 2, loads.Load())
}

type offline struct{}

func (offline) Get(context.Context, string) (string, error) { return "", errors.New("offline") }
func (offline) CompareAndSwap(context.Context, string, *string, string, time.Duration) (bool, error) {
	return false, errors.New("offline")
}
func TestReadsWithoutCache(t *testing.T) {
	cl, e := New(offline{}, func(context.Context, string) ([]cacheloader.Z, error) {
		return []cacheloader.Z{{Member: "a", Score: 1}}, nil
	}, cacheloader.Config{OnError: func(context.Context, error) {}})
	require.NoError(t, e)
	defer cl.Close()
	rows, e := cl.ZRange(context.Background(), "k", 0, -1, false)
	require.NoError(t, e)
	require.Len(t, rows, 1)
	n, e := cl.ZCard(context.Background(), "k")
	require.NoError(t, e)
	require.EqualValues(t, 1, n)
}
