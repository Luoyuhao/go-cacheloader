package cacheloader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang/groupcache/lru"

	"github.com/Luoyuhao/go-cacheloader/internal"
)

const (
	lockDuration = 300 * time.Millisecond
	invalidValue = "CacheLoader-invalid-cache-value" // 源数据不存在时，为防雪崩在cache中缓存的无效值
)

var ErrDowngraded = errors.New("CacheLoader downgraded")

/************************ CacheLoader structure ************************/
type CacheLoader struct {
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

// 查询数据。对应某个key数据不存在时，返回值为nil。
// @ctx：上下文
// @key：缓存keys
func (cl *CacheLoader) MGet(ctx context.Context, keys ...string) ([][]byte, error) {
	if len(keys) == 0 {
		return [][]byte{}, nil
	}
	if internal.Downgraded() {
		return nil, ErrDowngraded
	}
	// 1 查询缓存
	resultList, err := cl.cacheHandler.MGet(ctx, keys...)
	if err != nil {
		internal.Warn(ctx, fmt.Errorf("CacheLoader fail to execute MGet in Cacher implement, keys: %v, err: %v", keys, err))
		resultList = make([][]byte, len(keys))
	}
	if len(resultList) != len(keys) {
		return nil, fmt.Errorf("lenght of result list, returned by MGet in Cacher implement, did not match length of keys param, please checkout implement gideline")
	}
	missCacheKeys := make([]string, 0, len(resultList))
	missCacheIndexes := make([]int, 0, len(resultList))
	for i := 0; i < len(resultList); i++ {
		if resultList[i] != nil {
			// 无效值过滤：无效值返回给用户nil
			if fmt.Sprintf("%s", resultList[i]) == invalidValue {
				resultList[i] = nil
			}
			// 2 缓存命中则触发自动更新
			cl.triggerCacheRefresh(ctx, keys[i])
			continue
		}
		missCacheKeys = append(missCacheKeys, keys[i])
		missCacheIndexes = append(missCacheIndexes, i)
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

// 查询数据。数据不存在时，返回值为nil。
// @ctx：上下文
// @key：缓存key
func (cl *CacheLoader) Get(ctx context.Context, key string) ([]byte, error) {
	if internal.Downgraded() {
		return nil, ErrDowngraded
	}
	// 1 查询缓存
	data, err := cl.cacheHandler.Get(ctx, key)
	if err != nil {
		internal.Warn(ctx, fmt.Errorf("CacheLoader fail to execute Get in Cacher implement, key: %s, err: %v", key, err))
	}
	if data != nil {
		// 无效值过滤: 无效值返回给用户nil
		if fmt.Sprintf("%s", data) == invalidValue {
			data = nil
		}
		// 2 缓存命中则触发自动更新
		cl.triggerCacheRefresh(ctx, key)
		return data, nil
	}

	// 3 未命中缓存则回源
	dataList, err := cl.triggerLoadAndCache(ctx, key)
	if err != nil {
		return nil, err
	}
	return dataList[0], nil
}

// 触发自动更新缓存。
// @ctx：上下文
// @key：缓存key
func (cl *CacheLoader) triggerCacheRefresh(ctx context.Context, key string) {
	if cl.refreshAfterWriteSec == 0 {
		if cl.debug {
			internal.Debug(ctx, fmt.Sprintf("CLR no need to trigger cache refresh, cl: %v", *cl))
		}
		// refreshAfterWriteSec为零值时不自动更新缓存
		return
	}

	go func(ctx context.Context, key string) {
		timeoutCtx, cancel := context.WithTimeout(ctx, cl.refreshTimeout)
		internal.AskForToken()
		defer internal.RecoverAndLog(timeoutCtx)
		defer func() {
			cancel()
			internal.ReturnToken()
		}()
		ok := cl.rwLockAndResetMeta(key)
		if !ok {
			return
		}
		err := cl.lockAndCache(timeoutCtx, key, nil, true)
		if err != nil {
			internal.Warn(timeoutCtx, fmt.Errorf("CacheLoader fail to set cache when triggerCacheRefresh, key: %s, err: %v", key, err))
		}
	}(context.Background(), key)
}

// 触发回源并设置缓存。
// @ctx：上下文
// @keys：缓存keys
func (cl *CacheLoader) triggerLoadAndCache(ctx context.Context, keys ...string) ([][]byte, error) {
	if len(keys) == 0 {
		return [][]byte{}, nil
	}
	// 回源
	sourceDataList := make([][]byte, 0)
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
			return nil, fmt.Errorf("lenght of result list, returned by Loads in Loader implement, did not match length of keys param, please checkout implement gideline")
		}
	}

	// 异步设置缓存
	for i := 0; i < len(sourceDataList); i++ {
		go func(ctx context.Context, key string, val []byte) {
			internal.AskForToken()
			defer internal.RecoverAndLog(ctx)
			defer internal.ReturnToken()
			ok := cl.wLockAndResetMeta(key)
			if !ok {
				return
			}
			err := cl.lockAndCache(ctx, key, val, false)
			if err != nil {
				internal.Warn(ctx, fmt.Errorf("CacheLoader fail to set cache when triggerLoadAndCache, key: %s, err: %v", key, err))
			}
		}(context.Background(), keys[i], sourceDataList[i])
	}

	return sourceDataList, nil
}

// 获取本地写锁，若超过更新间隔则重设本地元数据。（用于缓存不存在时 主动回源场景）
// @key：缓存key
func (cl *CacheLoader) wLockAndResetMeta(key string) bool {
	cl.rwLock.Lock()
	defer cl.rwLock.Unlock()
	if cl.canWMeta(key) {
		cl.metaCache.Add(key, &lastWriteMeta{LastWriteTime: time.Now()})
		return true
	}
	return false
}

// 优先获取本地读锁，若超过更新间隔，则升级为写锁重设本地元数据。（用于缓存存在时 自动回源场景）
// @key：缓存key
func (cl *CacheLoader) rwLockAndResetMeta(key string) bool {
	//cl.rwLock.RLock()
	//if cl.canWMeta(key) {
	//	cl.rwLock.RUnlock()
	//	return cl.wLockAndResetMeta(key)
	//}
	//cl.rwLock.RUnlock()
	//return false
	return cl.wLockAndResetMeta(key)
}

// 判断当前key是否满足重设本地元数据的条件
// @key：缓存key
func (cl *CacheLoader) canWMeta(key string) bool {
	meta, ok := cl.metaCache.Get(key)
	if !ok {
		if cl.debug {
			internal.Debug(context.Background(), "CLR can write meta, meta not exist")
		}
		return true
	}

	m, ok := meta.(*lastWriteMeta)
	if ok && time.Since(m.LastWriteTime) < time.Duration(cl.refreshAfterWriteSec)*time.Second {
		if cl.debug {
			internal.Debug(context.Background(), fmt.Sprintf("CLR can not write meta, within time, since: %v, thredsold: %v", time.Since(m.LastWriteTime), time.Duration(cl.refreshAfterWriteSec)*time.Second))
		}
		return false
	}
	if cl.debug {
		internal.Debug(context.Background(), "CLR can write meta")
	}

	return true
}

// 设置分布式锁（时间段内互斥），设置成功则更新缓存，否则直接返回。
// @ctx：上下文
// @key：缓存key
// @val：缓存值
// @touchSource：是否需要回源
func (cl *CacheLoader) lockAndCache(ctx context.Context, key string, val []byte, touchSource bool) error {
	// 1 抢分布式锁（时间段内互斥）
	locked, err := cl.lockHandler.TimeLock(ctx, key, lockDuration)
	if err != nil {
		return fmt.Errorf("fail to execute time lock, err:%v", err)
	}
	// 2 抢锁成功则更新缓存
	if locked {
		// cache无命中时，ttl取配置值
		ttl := cl.getConfigTTL(val)
		if touchSource {
			// 3 自动更新过程需要回源
			val, ttl, err = cl.loadAndGetTTL(ctx, key)
			if err != nil {
				return err
			}
			// ttl == 0 and err == nil，表示缓存已过期
			if ttl == 0 {
				return nil
			}
			// 如果回源拿不到值，则写入无效值nil并设置过期时间为配置值
			if val == nil {
				ttl = cl.getConfigTTL(val)
			}
		}
		// 无效值过滤：待写入cache的值，如果为nil，则认为是无效值
		if val == nil {
			val = []byte(invalidValue)
		}
		err = cl.cacheHandler.Set(ctx, key, val, ttl)
		if err != nil {
			return fmt.Errorf("fail to execute set, err:%v", err)
		}
	}
	return nil
}

// 加载数据源并获取对应缓存的TTL。
// @ctx：上下文
// @key：缓存key
func (cl *CacheLoader) loadAndGetTTL(ctx context.Context, key string) ([]byte, time.Duration, error) {
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
	return val, ttl, nil
}

func (cl *CacheLoader) getConfigTTL(val []byte) time.Duration {
	if len(val) == 0 {
		return time.Duration(cl.ttlSec4Invalid) * time.Second // 无效值的过期时间
	}
	return time.Duration(cl.ttlSec) * time.Second // 有效值的过期时间
}
