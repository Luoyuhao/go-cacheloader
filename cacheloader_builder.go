package cacheloader

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/groupcache/lru"
)

const defaultMetaCacheMaxLen = 10000

// 生成CacheLoader的构建器。
func NewBuilder() *Builder {
	return &Builder{}
}

/************************ CacheLoader structure ************************/
type Builder struct {
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

// 元数据数组的最大长度
func (builder *Builder) MetaCacheMaxLen(metaCacheMaxLen int) *Builder {
	builder.metaCacheMaxLen = metaCacheMaxLen
	return builder
}

// 距离上次写缓存后触发自动更新的最小时间间隔（单位：s）：0代表不执行自动更新
func (builder *Builder) RefreshAfterWriteSec(refreshAfterWriteSec uint64) *Builder {
	builder.refreshAfterWriteSec = refreshAfterWriteSec
	return builder
}

// 缓存有效时长（单位：s）：0代表缓存不过期
func (builder *Builder) TTLSec(ttlSec uint64) *Builder {
	builder.ttlSec = ttlSec
	return builder
}

func (builder *Builder) TTLSec4Invalid(ttlSec4Invalid uint64) *Builder {
	builder.ttlSec4Invalid = ttlSec4Invalid
	return builder
}

func (builder *Builder) RefreshTimeout(timeout time.Duration) *Builder {
	builder.refreshTimeout = timeout
	return builder
}

func (builder *Builder) RegisterCacher(cacher Cacher) *Builder {
	builder.cacheHandler = cacher
	return builder
}

func (builder *Builder) RegisterLoader(loader Loader) *Builder {
	builder.loadHandler = loader
	return builder
}

func (builder *Builder) RegisterLocker(locker Locker) *Builder {
	builder.lockHandler = locker
	return builder
}

func (builder *Builder) Debug() *Builder {
	builder.debug = true
	return builder
}

func (builder *Builder) Build() (*CacheLoader, error) {
	if builder.refreshAfterWriteSec != 0 && builder.ttlSec <= builder.refreshAfterWriteSec {
		return nil, fmt.Errorf("refreshAfterWriteSec{%d} should be less than ttlSec{%d}", builder.refreshAfterWriteSec, builder.ttlSec)
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

	return &CacheLoader{
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
