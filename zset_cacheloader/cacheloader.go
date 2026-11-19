// Package zset_cacheloader provides read-only ordered snapshots, backed by the
// same atomic snapshot store as string_cacheloader (not native Redis zsets).
package zset_cacheloader

import (
	"context"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/Luoyuhao/go-cacheloader/v2/string_cacheloader"
)

type LoadFunc func(context.Context, string) ([]cacheloader.Z, error)
type CacheLoader struct {
	snapshots *string_cacheloader.CacheLoader[[]cacheloader.Z]
}

func New(cache cacheloader.StringCacher, load LoadFunc, cfg cacheloader.Config) (*CacheLoader, error) {
	if load == nil {
		return nil, fmt.Errorf("zset: load function is required")
	}
	reader, err := string_cacheloader.New(cache, func(ctx context.Context, key string) ([]cacheloader.Z, bool, error) {
		rows, err := load(ctx, key)
		if err != nil {
			return nil, false, err
		}
		rows, err = normalize(rows)
		return rows, len(rows) > 0, err
	}, cfg)
	if err != nil {
		return nil, err
	}
	return &CacheLoader{snapshots: reader}, nil
}

// Get returns a complete independent snapshot. Empty source sets are cached
// with NegativeTTL and returned as an empty slice, not as a cache miss.
func (cl *CacheLoader) Get(ctx context.Context, key string) ([]cacheloader.Z, error) {
	rows, _, err := cl.snapshots.Get(ctx, key)
	if err == nil && rows == nil {
		rows = []cacheloader.Z{}
	}
	return rows, err
}

// ZRange uses inclusive ranks; negative ranks count from the end. Reverse
// reverses score and member ordering together.
func (cl *CacheLoader) ZRange(ctx context.Context, key string, start, stop int64, reverse bool) ([]cacheloader.Z, error) {
	rows, err := cl.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return rankRange(rows, start, stop, reverse), nil
}
func (cl *CacheLoader) ZCard(ctx context.Context, key string) (int64, error) {
	rows, err := cl.Get(ctx, key)
	return int64(len(rows)), err
}
func (cl *CacheLoader) Close() error { return cl.snapshots.Close() }

func normalize(rows []cacheloader.Z) ([]cacheloader.Z, error) {
	byMember := make(map[string]float64, len(rows))
	for _, row := range rows {
		if !utf8.ValidString(row.Member) {
			return nil, fmt.Errorf("member %q is not valid UTF-8", row.Member)
		}
		if math.IsNaN(row.Score) || math.IsInf(row.Score, 0) {
			return nil, fmt.Errorf("non-finite score for %q", row.Member)
		}
		byMember[row.Member] = row.Score
	}
	out := make([]cacheloader.Z, 0, len(byMember))
	for member, score := range byMember {
		out = append(out, cacheloader.Z{Score: score, Member: member})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Member < out[j].Member
		}
		return out[i].Score < out[j].Score
	})
	return out, nil
}

func rankRange(rows []cacheloader.Z, start, stop int64, reverse bool) []cacheloader.Z {
	n := int64(len(rows))
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop >= n {
		stop = n - 1
	}
	if start >= n || stop < 0 || start > stop {
		return []cacheloader.Z{}
	}
	out := make([]cacheloader.Z, 0, stop-start+1)
	for i := start; i <= stop; i++ {
		index := i
		if reverse {
			index = n - 1 - i
		}
		out = append(out, rows[index])
	}
	return out
}
