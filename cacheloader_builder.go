package cacheloader

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/groupcache/lru"
)

const defaultMetaCacheMaxLen = 10000

// 生成CacheLoader的构建器。
func NewBuilder[T comparable]() *Builder[T] {
	return &Builder[T]{}
}

/************************ CacheLoader structure ************************/
type Builder[T comparable] struct {
	cacheHandler         Cacher        // 缓存处理器
	loadHandler          Loader        // 回源处理器
	lockHandler          Locker        // 分布式锁处理器
	refreshAfterWriteSec uint64        // 距离上次写缓存后触发自动更新的最小时间间隔（单位：s）：0代表不执行自动更新
	ttlSec               uint64        // 缓存有效时长（单位：s）：0代表缓存不过期
	ttlSec4Invalid       uint64        // 无效值的缓存过期时长（单位：s）
	refreshTimeout       time.Duration // 自动更新timeout设置（默认为2s）
	metaCacheMaxLen      int           // 元数据数组最大长度：数组长度越大，越能减少不必要的回源请求，但注意内存使用情况
	debug                bool          // debug模式
}

// MetaCacheMaxLen set meta cache max length
func (builder *Builder[T]) MetaCacheMaxLen(metaCacheMaxLen int) *Builder[T] {
	builder.metaCacheMaxLen = metaCacheMaxLen
	return builder
}

// RefreshAfterWriteSec set refresh after write seconds, 0 means no auto-refresh
func (builder *Builder[T]) RefreshAfterWriteSec(refreshAfterWriteSec uint64) *Builder[T] {
	builder.refreshAfterWriteSec = refreshAfterWriteSec
	return builder
}

// TTLSec set ttl seconds, 0 means cache never expires
func (builder *Builder[T]) TTLSec(ttlSec uint64) *Builder[T] {
	builder.ttlSec = ttlSec
	return builder
}

// TTLSec4Invalid set ttl seconds for invalid value
func (builder *Builder[T]) TTLSec4Invalid(ttlSec4Invalid uint64) *Builder[T] {
	builder.ttlSec4Invalid = ttlSec4Invalid
	return builder
}

// RefreshTimeout set refresh timeout, default is 2s
func (builder *Builder[T]) RefreshTimeout(timeout time.Duration) *Builder[T] {
	builder.refreshTimeout = timeout
	return builder
}

// RegisterCacher register cacher
func (builder *Builder[T]) RegisterCacher(cacher Cacher) *Builder[T] {
	builder.cacheHandler = cacher
	return builder
}

// RegisterLoader register loader
func (builder *Builder[T]) RegisterLoader(loader Loader) *Builder[T] {
	builder.loadHandler = loader
	return builder
}

// RegisterLocker register locker
func (builder *Builder[T]) RegisterLocker(locker Locker) *Builder[T] {
	builder.lockHandler = locker
	return builder
}

// Debug set debug mode
func (builder *Builder[T]) Debug() *Builder[T] {
	builder.debug = true
	return builder
}

// Build build CacheLoader
func (builder *Builder[T]) Build() (*CacheLoader[T], error) {
	if builder.refreshAfterWriteSec > 0 && builder.ttlSec > 0 &&
		builder.ttlSec <= builder.refreshAfterWriteSec {
		return nil, fmt.Errorf("refreshAfterWriteSec{%d} should be less than ttlSec{%d}",
			builder.refreshAfterWriteSec, builder.ttlSec)
	}
	if builder.cacheHandler == nil {
		return nil, fmt.Errorf("cacheHandler should not be nil")
	}
	if builder.loadHandler == nil {
		return nil, fmt.Errorf("loadHandler should not be nil")
	}
	if builder.lockHandler == nil {
		return nil, fmt.Errorf("lockHandler should not be nil")
	}

	if builder.ttlSec4Invalid == 0 {
		builder.ttlSec4Invalid = 1 // 无效值默认缓存失效时间是1s
	}
	if builder.refreshTimeout == 0 {
		builder.refreshTimeout = 2 * time.Second // 自动更新timeout设置（默认为2s）
	}
	if builder.metaCacheMaxLen == 0 {
		builder.metaCacheMaxLen = defaultMetaCacheMaxLen
	}

	return &CacheLoader[T]{
		cacheHandler:         builder.cacheHandler,
		loadHandler:          builder.loadHandler,
		lockHandler:          builder.lockHandler,
		refreshAfterWriteSec: builder.refreshAfterWriteSec,
		ttlSec:               builder.ttlSec,
		ttlSec4Invalid:       builder.ttlSec4Invalid,
		metaCache:            lru.New(builder.metaCacheMaxLen),
		rwLock:               &sync.RWMutex{},
		refreshTimeout:       builder.refreshTimeout,
		debug:                builder.debug,
	}, nil
}
