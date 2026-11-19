package cacheloader

import (
	"context"
	"time"
)

// StringCacher stores opaque, complete snapshots. Implementations must honor
// context cancellation. The loader owns the wire format and key namespace.
type StringCacher interface {
	// Get returns ErrNotFound for missing or expired keys.
	Get(context.Context, string) (string, error)
	// CompareAndSwap atomically checks the current live value and replaces it.
	// previous=nil matches ONLY a missing/expired key; otherwise the exact live
	// value must match. On mismatch return false, nil. TTL=0 means no expiration.
	// An expired key must never match a non-nil previous value.
	CompareAndSwap(ctx context.Context, key string, previous *string, value string, ttl time.Duration) (bool, error)
}

// Z identifies a member in an ordered snapshot. Members must be valid UTF-8
// and scores must be finite.
// Equal scores are ordered by Member's bytewise lexicographic order.
type Z struct {
	Score  float64 `json:"score"`
	Member string  `json:"member"`
}

// BatchStringCacher is an optional capability detected by MGet. Results must
// match the requested keys in length and order; Found=false denotes a miss.
// Any error invalidates the entire response (including partial results).
type BatchStringCacher interface {
	MGet(ctx context.Context, keys ...string) ([]Result[string], error)
}
