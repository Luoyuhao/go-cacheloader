package cacheloader

import "context"

/************************ Loader：回源处理器抽象 ************************/
type Loader interface {
	// Load load data by key, return nil if not exist
	Load(ctx context.Context, key string) (interface{}, error)

	// Loads load data by keys, return nil if not exist
	Loads(ctx context.Context, keys ...string) ([]interface{}, error)
}
