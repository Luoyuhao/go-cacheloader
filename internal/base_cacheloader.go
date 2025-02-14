package internal

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Luoyuhao/go-cacheloader"
	"github.com/Luoyuhao/go-cacheloader/helper"
	"github.com/golang/groupcache/lru"
)

const (
	lockDuration = 300 * time.Millisecond
)

func Zero[T any]() T {
	var zero T
	return zero
}

type CacheSetter interface {
	SetCache(ctx context.Context, key string, val interface{}, touchSource bool) error
}

type BaseCacheLoader[T comparable] struct {
	CacheSetter
	LoadHandler          cacheloader.Loader // 回源处理器
	LockHandler          cacheloader.Locker // 分布式锁处理器
	RefreshAfterWriteSec uint64             // 距离上次写缓存后触发自动更新的最小时间间隔（单位：s）：0代表不执行自动更新
	RefreshTimeout       time.Duration      // 自动更新timeout设置（默认为2s）
	TTLSec4Invalid       uint64             // 无效值的缓存有效时长(单位：s)
	MetaCache            *lru.Cache         // 带LRU淘汰机制的cache, 存储上次写缓存的元数据
	RWLock               sync.RWMutex       // 用于控制metaMap读写
	Debug                bool               // debug模式
}

type lastWriteMeta struct {
	LastWriteTime time.Time // 上次写缓存时间
}

func NewBaseCacheLoader[T comparable](
	loadHandler cacheloader.Loader,
	lockHandler cacheloader.Locker,
	refreshAfterWriteSec uint64,
	refreshTimeout time.Duration,
	ttlSec4Invalid uint64,
	metaCacheMaxLen int,
	debug bool,
) *BaseCacheLoader[T] {
	bcl := &BaseCacheLoader[T]{
		LoadHandler:          loadHandler,
		LockHandler:          lockHandler,
		RefreshAfterWriteSec: refreshAfterWriteSec,
		RefreshTimeout:       refreshTimeout,
		TTLSec4Invalid:       ttlSec4Invalid,
		MetaCache:            lru.New(metaCacheMaxLen),
		Debug:                debug,
	}
	bcl.CacheSetter = bcl
	return bcl
}

// TriggerCacheRefresh trigger cache refresh
func (cl *BaseCacheLoader[T]) TriggerCacheRefresh(ctx context.Context, key string) {
	if cl.RefreshAfterWriteSec <= 0 { // RefreshAfterWriteSec为零值时不自动更新缓存
		if cl.Debug {
			helper.Debug(ctx, "CLR no need to trigger cache refresh")
		}
		return
	}

	go func(ctx context.Context, key string) {
		timeoutCtx, cancel := context.WithTimeout(ctx, cl.RefreshTimeout)
		helper.AskForToken()
		defer helper.RecoverAndLog(timeoutCtx)
		defer func() {
			cancel()
			helper.ReturnToken()
		}()
		ok := cl.RWLockAndResetMeta(key)
		if !ok {
			return
		}
		err := cl.LockAndCache(timeoutCtx, key, Zero[T](), true)
		if err != nil {
			helper.Warn(timeoutCtx, fmt.Errorf("CacheLoader fail to set cache when triggerCacheRefresh, key: %s, err: %v", key, err))
		}
	}(context.Background(), key)
}

// TriggerLoadAndCache trigger load and cache
func (cl *BaseCacheLoader[T]) TriggerLoadAndCache(ctx context.Context, keys ...string) ([]T, error) {
	if len(keys) == 0 {
		return []T{}, nil
	}
	// 回源
	sourceDataList := make([]interface{}, 0)
	var err error
	if len(keys) == 1 {
		sourceData, err := cl.LoadHandler.Load(ctx, keys[0])
		if err != nil {
			return nil, err
		}
		sourceDataList = append(sourceDataList, sourceData)
	} else {
		sourceDataList, err = cl.LoadHandler.Loads(ctx, keys...)
		if err != nil {
			return nil, err
		}
		if len(sourceDataList) != len(keys) {
			err = fmt.Errorf("lenght of result list, returned by Loads in Loader implement, did not match length of keys param, please checkout implement gideline")
			helper.Err(ctx, err)
			return nil, err
		}
	}

	// 异步设置缓存
	for i := 0; i < len(sourceDataList); i++ {
		go func(ctx context.Context, key string, val interface{}) {
			helper.AskForToken()
			defer helper.RecoverAndLog(ctx)
			defer helper.ReturnToken()
			ok := cl.WLockAndResetMeta(key)
			if !ok {
				return
			}
			err := cl.LockAndCache(ctx, key, val, false)
			if err != nil {
				helper.Warn(ctx, fmt.Errorf("CacheLoader fail to set cache when triggerLoadAndCache, key: %s, err: %v", key, err))
			}
		}(context.Background(), keys[i], sourceDataList[i])
	}

	resultList := make([]T, 0, len(sourceDataList))
	for _, val := range sourceDataList {
		if val == nil {
			resultList = append(resultList, Zero[T]())
			continue
		}
		result, ok := val.(T)
		if !ok {
			err = fmt.Errorf("source data is supposed to be type of %T, but got type of %T", Zero[T](), val)
			helper.Err(ctx, err)
			return nil, err
		}
		resultList = append(resultList, result)
	}
	return resultList, nil
}

// WLockAndResetMeta get local write lock, reset local meta if exceed update interval.
func (cl *BaseCacheLoader[T]) WLockAndResetMeta(key string) bool {
	cl.RWLock.Lock()
	defer cl.RWLock.Unlock()
	if cl.CanWMeta(key) {
		cl.MetaCache.Add(key, &lastWriteMeta{LastWriteTime: time.Now()})
		return true
	}
	return false
}

// RWLockAndResetMeta get local read lock, upgrade to write lock if exceed update interval, then reset local meta.
func (cl *BaseCacheLoader[T]) RWLockAndResetMeta(key string) bool {
	cl.RWLock.RLock()
	if cl.CanWMeta(key) {
		cl.RWLock.RUnlock()
		return cl.WLockAndResetMeta(key)
	}
	cl.RWLock.RUnlock()
	return false
}

// CanWMeta judge if current key satisfies the condition to reset local meta
func (cl *BaseCacheLoader[T]) CanWMeta(key string) bool {
	meta, ok := cl.MetaCache.Get(key)
	if !ok {
		if cl.Debug {
			helper.Debug(context.Background(), "CLR can write meta, meta not exist")
		}
		return true
	}

	m, ok := meta.(*lastWriteMeta)
	if ok && time.Since(m.LastWriteTime) < time.Duration(cl.RefreshAfterWriteSec)*time.Second {
		if cl.Debug {
			helper.Debug(context.Background(), fmt.Sprintf("CLR can not write meta, within time, since: %v, thredsold: %v", time.Since(m.LastWriteTime), time.Duration(cl.RefreshAfterWriteSec)*time.Second))
		}
		return false
	}
	if cl.Debug {
		helper.Debug(context.Background(), "CLR can write meta")
	}

	return true
}

// LockAndCache set distributed lock (mutual exclusion within time), set success then update cache, otherwise return directly.
func (cl *BaseCacheLoader[T]) LockAndCache(ctx context.Context, key string, val4Cache interface{}, touchSource bool) error {
	// 1 抢分布式锁（时间段内互斥）
	locked, err := cl.LockHandler.TimeLock(ctx, key, lockDuration)
	if err != nil {
		return fmt.Errorf("fail to execute time lock, err:%v", err)
	}
	// 2 抢锁成功则更新缓存
	if locked {
		err = cl.CacheSetter.SetCache(ctx, key, val4Cache, touchSource)
		if err != nil {
			return err
		}
	}
	return nil
}

func (cl *BaseCacheLoader[T]) SetCache(_ context.Context, _ string, _ interface{}, _ bool) error {
	return fmt.Errorf("to be override")
}
