// Package string_cacheloader provides typed read-only JSON snapshot caching.
package string_cacheloader

import (
	"context"
	"fmt"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/Luoyuhao/go-cacheloader/v2/internal"
)

type CacheLoader[T any] struct{ reader *internal.Reader[T] }

// Option adds optional source capabilities without changing the single-key API.
type Option[T any] func(*options[T]) error
type options[T any] struct{ batch cacheloader.BatchLoadFunc[T] }

func WithBatchLoader[T any](load cacheloader.BatchLoadFunc[T]) Option[T] {
	return func(o *options[T]) error {
		if load == nil {
			return fmt.Errorf("cacheloader: batch loader must not be nil")
		}
		o.batch = load
		return nil
	}
}

func New[T any](cache cacheloader.StringCacher, load cacheloader.LoadFunc[T], cfg cacheloader.Config, opts ...Option[T]) (*CacheLoader[T], error) {
	var o options[T]
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("cacheloader: nil option")
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	r, err := internal.NewReader(cache, load, cfg, o.batch)
	if err != nil {
		return nil, err
	}
	return &CacheLoader[T]{reader: r}, nil
}

func (cl *CacheLoader[T]) Get(ctx context.Context, key string) (T, bool, error) {
	return cl.reader.Get(ctx, key)
}
func (cl *CacheLoader[T]) MGet(ctx context.Context, keys ...string) ([]cacheloader.Result[T], error) {
	return cl.reader.MGet(ctx, keys...)
}
func (cl *CacheLoader[T]) Close() error { return cl.reader.Close() }
