package cacheloader

import (
	"context"
	"time"
)

/************************ Cacher：缓存处理器抽象 ************************/
type Cacher interface {
	// Get get cache by key, return ErrNotFound if not exist
	Get(ctx context.Context, key string) (string, error)

	// MGet get multiple cache by keys, return nil if not exist
	MGet(ctx context.Context, keys ...string) ([]interface{}, error)

	// Set set cache by key, val and ttl
	Set(ctx context.Context, key string, val string, ttl time.Duration) error

	// TTL get ttl by key. Always return 0 to make Cacheloader consider cache is expired and make cache refresh invalid, since auto-refresh process only update cache content until cache is expired.
	// return ErrNotSupported when cache middleware not support TTL, Cacheloader will set cache with ttl of CacheLoader setting.
	TTL(ctx context.Context, key string) (time.Duration, error)
}
