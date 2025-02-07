package cacheloader

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Luoyuhao/go-cacheloader/helper"
	"github.com/golang/groupcache/lru"
)

const (
	lockDuration = 300 * time.Millisecond
	invalidValue = "CacheLoader-invalid-cache-value" // 源数据不存在时，为防雪崩在cache中缓存的无效值
)

func zero[T any]() T {
	var zero T
	return zero
}

/************************ CacheLoader structure ************************/
type CacheLoader[T comparable] struct {
	cacheHandler         Cacher        // 缓存处理器
	loadHandler          Loader        // 回源处理器
	lockHandler          Locker        // 分布式锁处理器
	refreshAfterWriteSec uint64        // 距离上次写缓存后触发自动更新的最小时间间隔（单位：s）：0代表不执行自动更新
	ttlSec               uint64        // 缓存有效时长（单位：s）：0代表缓存不过期
	ttlSec4Invalid       uint64        // 无效值的缓存有效时长(单位：s)
	metaCache            *lru.Cache    // 带LRU淘汰机制的cache, 存储上次写缓存的元数据
	rwLock               *sync.RWMutex // 用于控制metaMap读写
	refreshTimeout       time.Duration // 自动更新timeout设置（默认为2s）
	debug                bool          // debug模式
}

type lastWriteMeta struct {
	LastWriteTime time.Time // 上次写缓存时间
}

// MGet get multiple cache data by keys
func (cl *CacheLoader[T]) MGet(ctx context.Context, keys ...string) ([]T, error) {
	if len(keys) == 0 {
		return []T{}, nil
	}
	if helper.Downgraded() {
		return nil, ErrDowngraded
	}

	var resultList []T
	// 1 查询缓存
	cacheResults, err := cl.cacheHandler.MGet(ctx, keys...)
	if err != nil {
		helper.Warn(ctx, fmt.Errorf("fail to execute MGet in Cacher implement, keys: %v, err: %v", keys, err))
		resultList = make([]T, len(keys))
	} else {
		resultList = make([]T, len(cacheResults))
	}
	if len(resultList) != len(keys) {
		err = fmt.Errorf("lenght of result list, returned by MGet in Cacher implement, did not match length of keys param, please checkout implement gideline")
		helper.Err(ctx, err)
		return nil, err
	}
	missCacheKeys := make([]string, 0, len(cacheResults))
	missCacheIndexes := make([]int, 0, len(cacheResults))
	for i := 0; i < len(cacheResults); i++ {
		if cacheResults[i] == nil {
			missCacheKeys = append(missCacheKeys, keys[i])
			missCacheIndexes = append(missCacheIndexes, i)
			continue
		}

		result, err := cl.cvtStrTypeCacheData(ctx, cacheResults[i])
		if err != nil {
			return nil, err
		}
		resultList[i] = result

		// 2 缓存命中则触发自动更新
		cl.triggerCacheRefresh(ctx, keys[i])
	}
	if len(missCacheKeys) == 0 {
		return resultList, nil
	}

	// 3 未命中缓存则回源
	sourceDataList, err := cl.triggerLoadAndCache(ctx, missCacheKeys...)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(sourceDataList); i++ {
		resultList[missCacheIndexes[i]] = sourceDataList[i]
	}
	return resultList, nil
}

// Get get cache data by key
func (cl *CacheLoader[T]) Get(ctx context.Context, key string) (T, error) {
	if helper.Downgraded() {
		return zero[T](), ErrDowngraded
	}
	// 1 查询缓存
	data, err := cl.cacheHandler.Get(ctx, key)
	if err != nil && err != ErrNotFound {
		helper.Warn(ctx, fmt.Errorf("CacheLoader fail to execute Get in Cacher implement, key: %s, err: %v", key, err))
	}

	if err == nil {
		result, err := cl.cvtStrTypeCacheData(ctx, data)
		if err != nil {
			return zero[T](), err
		}
		// 2 缓存命中则触发自动更新
		cl.triggerCacheRefresh(ctx, key)
		return result, nil
	}

	// 3 未命中缓存则回源
	dataList, err := cl.triggerLoadAndCache(ctx, key)
	if err != nil {
		return zero[T](), err
	}
	return dataList[0], nil
}

// cvtStrTypeCacheData convert string type cache data to T type, return zero of T type if cache data is nil
func (cl *CacheLoader[T]) cvtStrTypeCacheData(ctx context.Context, cacheData interface{}) (T, error) {
	var data T
	switch raw := cacheData.(type) {
	case string:
		// 无效值过滤：无效值返回给用户nil
		if raw == invalidValue {
			return zero[T](), nil
		}
		err := helper.JSONUnmarshal([]byte(raw), &data)
		if err != nil {
			helper.Err(ctx, fmt.Errorf("fail to execute JSONUnmarshal, err: %v", err))
			return zero[T](), err
		}
		return data, nil
	case []byte:
		if string(raw) == invalidValue {
			return zero[T](), nil
		}
		err := helper.JSONUnmarshal(raw, &data)
		if err != nil {
			helper.Err(ctx, fmt.Errorf("fail to execute JSONUnmarshal, err: %v", err))
			return zero[T](), err
		}
		return data, nil
	}
	err := fmt.Errorf("unsupported cache data type: %T", cacheData)
	helper.Err(ctx, err)
	return zero[T](), err
}

// triggerCacheRefresh trigger cache refresh
func (cl *CacheLoader[T]) triggerCacheRefresh(ctx context.Context, key string) {
	if cl.refreshAfterWriteSec <= 0 { // refreshAfterWriteSec为零值时不自动更新缓存
		if cl.debug {
			helper.Debug(ctx, fmt.Sprintf("CLR no need to trigger cache refresh, cl: %v", *cl))
		}
		return
	}

	go func(ctx context.Context, key string) {
		timeoutCtx, cancel := context.WithTimeout(ctx, cl.refreshTimeout)
		helper.AskForToken()
		defer helper.RecoverAndLog(timeoutCtx)
		defer func() {
			cancel()
			helper.ReturnToken()
		}()
		ok := cl.rwLockAndResetMeta(key)
		if !ok {
			return
		}
		err := cl.lockAndCache(timeoutCtx, key, zero[T](), true)
		if err != nil {
			helper.Warn(timeoutCtx, fmt.Errorf("CacheLoader fail to set cache when triggerCacheRefresh, key: %s, err: %v", key, err))
		}
	}(context.Background(), key)
}

// triggerLoadAndCache trigger load and cache
func (cl *CacheLoader[T]) triggerLoadAndCache(ctx context.Context, keys ...string) ([]T, error) {
	if len(keys) == 0 {
		return []T{}, nil
	}
	// 回源
	sourceDataList := make([]interface{}, 0)
	var err error
	if len(keys) == 1 {
		sourceData, err := cl.loadHandler.Load(ctx, keys[0])
		if err != nil {
			return nil, err
		}
		sourceDataList = append(sourceDataList, sourceData)
	} else {
		sourceDataList, err = cl.loadHandler.Loads(ctx, keys...)
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
			ok := cl.wLockAndResetMeta(key)
			if !ok {
				return
			}
			err := cl.lockAndCache(ctx, key, val, false)
			if err != nil {
				helper.Warn(ctx, fmt.Errorf("CacheLoader fail to set cache when triggerLoadAndCache, key: %s, err: %v", key, err))
			}
		}(context.Background(), keys[i], sourceDataList[i])
	}

	resultList := make([]T, 0, len(sourceDataList))
	for _, val := range sourceDataList {
		if val == nil {
			resultList = append(resultList, zero[T]())
			continue
		}
		result, ok := val.(T)
		if !ok {
			err = fmt.Errorf("source data is supposed to be type of %T, but got type of %T", zero[T](), val)
			helper.Err(ctx, err)
			return nil, err
		}
		resultList = append(resultList, result)
	}
	return resultList, nil
}

// wLockAndResetMeta get local write lock, reset local meta if exceed update interval. (for access source scenario when cache not exist)
func (cl *CacheLoader[T]) wLockAndResetMeta(key string) bool {
	cl.rwLock.Lock()
	defer cl.rwLock.Unlock()
	if cl.canWMeta(key) {
		cl.metaCache.Add(key, &lastWriteMeta{LastWriteTime: time.Now()})
		return true
	}
	return false
}

// rwLockAndResetMeta get local read lock, upgrade to write lock if exceed update interval, then reset local meta. (for auto refresh scenario when cache exist)
func (cl *CacheLoader[T]) rwLockAndResetMeta(key string) bool {
	cl.rwLock.RLock()
	if cl.canWMeta(key) {
		cl.rwLock.RUnlock()
		return cl.wLockAndResetMeta(key)
	}
	cl.rwLock.RUnlock()
	return false
}

// canWMeta judge if current key satisfies the condition to reset local meta
func (cl *CacheLoader[T]) canWMeta(key string) bool {
	meta, ok := cl.metaCache.Get(key)
	if !ok {
		if cl.debug {
			helper.Debug(context.Background(), "CLR can write meta, meta not exist")
		}
		return true
	}

	m, ok := meta.(*lastWriteMeta)
	if ok && time.Since(m.LastWriteTime) < time.Duration(cl.refreshAfterWriteSec)*time.Second {
		if cl.debug {
			helper.Debug(context.Background(), fmt.Sprintf("CLR can not write meta, within time, since: %v, thredsold: %v", time.Since(m.LastWriteTime), time.Duration(cl.refreshAfterWriteSec)*time.Second))
		}
		return false
	}
	if cl.debug {
		helper.Debug(context.Background(), "CLR can write meta")
	}

	return true
}

// lockAndCache set distributed lock (mutual exclusion within time), set success then update cache, otherwise return directly.
func (cl *CacheLoader[T]) lockAndCache(ctx context.Context, key string, val4Cache interface{}, touchSource bool) error {
	// 1 抢分布式锁（时间段内互斥）
	locked, err := cl.lockHandler.TimeLock(ctx, key, lockDuration)
	if err != nil {
		return fmt.Errorf("fail to execute time lock, err:%v", err)
	}
	// 2 抢锁成功则更新缓存
	if locked {
		// cache无命中时，ttl取配置值
		ttl := cl.getConfigTTL(val4Cache)
		if touchSource {
			// 3 自动更新过程需要回源
			val4Cache, ttl, err = cl.loadAndGetTTL(ctx, key)
			if err != nil {
				return err
			}
			// ttl == 0，表示缓存已过期
			if ttl == 0 {
				return nil
			}
			// 如果回源拿不到值，则写入无效值nil并设置过期时间为配置值
			if val4Cache == nil {
				ttl = cl.getConfigTTL(val4Cache)
			}
		}
		// 无效值过滤：待写入cache的值，如果为nil，则认为是无效值
		var val4CacheStr string
		if val4Cache == nil {
			val4CacheStr = invalidValue
		} else {
			tmp, err := helper.JSONMarshal(val4Cache)
			if err != nil {
				err = fmt.Errorf("fail to execute JSONMarshal, err:%v", err)
				helper.Err(ctx, err)
				return err
			}
			val4CacheStr = string(tmp)
		}
		err = cl.cacheHandler.Set(ctx, key, val4CacheStr, ttl)
		if err != nil {
			return fmt.Errorf("fail to execute set, err:%v", err)
		}
	}
	return nil
}

// loadAndGetTTL load source data and get corresponding cache ttl
func (cl *CacheLoader[T]) loadAndGetTTL(ctx context.Context, key string) (interface{}, time.Duration, error) {
	val, err := cl.loadHandler.Load(ctx, key)
	if err != nil {
		return nil, 0, fmt.Errorf("fail to execute load, err:%v", err)
	}
	ttl, err := cl.cacheHandler.TTL(ctx, key)
	if err != nil {
		if err != ErrNotSupported {
			return nil, 0, fmt.Errorf("fail to execute ttl, err:%v", err)
		}
		ttl = cl.getConfigTTL(val)
	}

	if val == nil {
		return nil, ttl, nil
	}
	result, ok := val.(T)
	if !ok {
		err = fmt.Errorf("source data is supposed to be type of %T, but got type of %T", zero[T](), val)
		helper.Err(ctx, err)
		return nil, 0, err
	}
	return result, ttl, nil
}

func (cl *CacheLoader[T]) getConfigTTL(val interface{}) time.Duration {
	if val == nil {
		return time.Duration(cl.ttlSec4Invalid) * time.Second
	}
	return time.Duration(cl.ttlSec) * time.Second
}
