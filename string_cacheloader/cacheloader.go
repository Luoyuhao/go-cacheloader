package string_cacheloader

import (
	"context"
	"fmt"
	"time"

	"github.com/Luoyuhao/go-cacheloader"
	"github.com/Luoyuhao/go-cacheloader/helper"
	"github.com/Luoyuhao/go-cacheloader/internal"
)

const (
	invalidValue = "CacheLoader-invalid-cache-value" // 源数据不存在时，为防雪崩在cache中缓存的无效值
)

/************************ CacheLoader structure ************************/
type CacheLoader[T comparable] struct {
	cacheHandler cacheloader.StringCacher // 缓存处理器
	ttlSec       uint64                   // 缓存有效时长（单位：s）：0代表缓存不过期
	*internal.BaseCacheLoader[T]
}

// MGet get multiple cache data by keys
func (cl *CacheLoader[T]) MGet(ctx context.Context, keys ...string) ([]T, error) {
	if len(keys) == 0 {
		return []T{}, nil
	}
	if helper.Downgraded() {
		return nil, cacheloader.ErrDowngraded
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
		cl.TriggerCacheRefresh(ctx, keys[i])
	}
	if len(missCacheKeys) == 0 {
		return resultList, nil
	}

	// 3 未命中缓存则回源
	sourceDataList, err := cl.TriggerLoadAndCache(ctx, missCacheKeys...)
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
		return internal.Zero[T](), cacheloader.ErrDowngraded
	}
	// 1 查询缓存
	data, err := cl.cacheHandler.Get(ctx, key)
	if err != nil && err != cacheloader.ErrNotFound {
		helper.Warn(ctx, fmt.Errorf("CacheLoader fail to execute Get in Cacher implement, key: %s, err: %v", key, err))
	}

	if err == nil {
		result, err := cl.cvtStrTypeCacheData(ctx, data)
		if err != nil {
			return internal.Zero[T](), err
		}
		// 2 缓存命中则触发自动更新
		cl.TriggerCacheRefresh(ctx, key)
		return result, nil
	}

	// 3 未命中缓存则回源
	dataList, err := cl.TriggerLoadAndCache(ctx, key)
	if err != nil {
		return internal.Zero[T](), err
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
			return internal.Zero[T](), nil
		}
		err := helper.JSONUnmarshal([]byte(raw), &data)
		if err != nil {
			helper.Err(ctx, fmt.Errorf("fail to execute JSONUnmarshal, err: %v", err))
			return internal.Zero[T](), err
		}
		return data, nil
	case []byte:
		if string(raw) == invalidValue {
			return internal.Zero[T](), nil
		}
		err := helper.JSONUnmarshal(raw, &data)
		if err != nil {
			helper.Err(ctx, fmt.Errorf("fail to execute JSONUnmarshal, err: %v", err))
			return internal.Zero[T](), err
		}
		return data, nil
	}
	err := fmt.Errorf("unsupported cache data type: %T", cacheData)
	helper.Err(ctx, err)
	return internal.Zero[T](), err
}

func (cl *CacheLoader[T]) SetCache(ctx context.Context, key string, val4Cache interface{}, touchSource bool) error {
	var err error
	// 根据Cache值设置TTL
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
	return nil
}

// loadAndGetTTL load source data and get corresponding cache ttl
func (cl *CacheLoader[T]) loadAndGetTTL(ctx context.Context, key string) (interface{}, time.Duration, error) {
	val, err := cl.LoadHandler.Load(ctx, key)
	if err != nil {
		return nil, 0, fmt.Errorf("fail to execute load, err:%v", err)
	}
	ttl, err := cl.cacheHandler.TTL(ctx, key)
	if err != nil {
		if err != cacheloader.ErrNotSupported {
			return nil, 0, fmt.Errorf("fail to execute ttl, err:%v", err)
		}
		ttl = cl.getConfigTTL(val)
	}

	if val == nil {
		return nil, ttl, nil
	}
	result, ok := val.(T)
	if !ok {
		err = fmt.Errorf("source data is supposed to be type of %T, but got type of %T", internal.Zero[T](), val)
		helper.Err(ctx, err)
		return nil, 0, err
	}
	return result, ttl, nil
}

func (cl *CacheLoader[T]) getConfigTTL(val interface{}) time.Duration {
	if val == nil {
		return time.Duration(cl.TTLSec4Invalid) * time.Second
	}
	return time.Duration(cl.ttlSec) * time.Second
}
