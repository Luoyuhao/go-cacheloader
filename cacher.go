package cacheloader

import (
	"context"
	"errors"
	"time"
)

var ErrNotSupported = errors.New("not supported")

/************************ Cacher：缓存处理器抽象 ************************/
type Cacher[T any] interface {
	// 查询缓存。（1.不存在时返回nil 2.存在但无效值时返回[]byte{}）
	// @ctx：上下文
	// @key：缓存key
	Get(ctx context.Context, key string) (T, error)

	// 查询多个缓存，结果集长度与keys参数长度一致。（对于单个元素：1.不存在时返回nil 2.存在但无效值时返回[]byte{}）
	// @ctx：上下文
	// @key：缓存key
	MGet(ctx context.Context, keys ...string) ([]T, error)

	// 设置缓存。
	// @ctx：上下文
	// @key：缓存key
	// @val：缓存值
	// @ttl：过期时间
	Set(ctx context.Context, key string, val T, ttl time.Duration) error

	// 查询过期时间。 自动更新过程原则上只更新缓存内容而不更新原有过期时间， 因此会以查询出的剩余过期时间并以此设置新的缓存（若自动更新频繁 真实过期时间理论上会比最初设置的ttl有毫秒级延迟）。
	// time.Duration==0：代表无需自动更新（缓存已经过期）。
	// error==ErrNotSupported：抛出该异常时（底层中间件不支持实现等原因），自动更新过程会以CacheLoader的ttl设置缓存（缓存过期前请求持续到达时 缓存将永远不会过期）。
	// @ctx：上下文
	// @key：缓存key
	TTL(ctx context.Context, key string) (time.Duration, error)
}
