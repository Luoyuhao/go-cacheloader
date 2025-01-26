package cacheloader

import "context"

/************************ Loader：回源处理器抽象 ************************/
type Loader interface {
	// 回源加载数据。（不存在时返回nil）
	// @ctx：上下文
	// @key：用于缓存的key（回源时自行将key映射到数据源查询）
	Load(ctx context.Context, key string) (interface{}, error)

	// 回源加载数据，结果集长度与keys参数长度一致。（对于单个元素：不存在时返回nil）
	// @ctx：上下文
	// @key：用于缓存的keys（回源时自行将keys映射到数据源查询）
	Loads(ctx context.Context, keys ...string) ([]interface{}, error)
}
