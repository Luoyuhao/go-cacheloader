package cacheloader

import "context"

// LoadFunc reads the authoritative source. found distinguishes absence from a
// valid zero/nil value. It must be read-only and honor context cancellation.
type LoadFunc[T any] func(context.Context, string) (value T, found bool, err error)

// Result is one positional result of MGet.
type Result[T any] struct {
	Value T
	Found bool
}

// BatchLoadFunc optionally loads distinct keys in a single source operation.
// Return exactly one positional result per key, using Found=false for absence.
// An error invalidates the entire batch. Like LoadFunc, this must be read-only.
type BatchLoadFunc[T any] func(ctx context.Context, keys []string) ([]Result[T], error)
