# go-cacheloader

适配KVStore中间件，在其缓存能力基础上支持"自动刷新"特性的工具组件。

## 技术方案
TODO

## 支持特性
* 缓存秒级自动更新
* 适配缓存中间件
* 适配本地缓存
* 访问降级
* 防穿透
* 并发度控制
* 自动回源超时控制

## 适用场景
* 源数据变更难以让项目感知的场景（典型场景，通过二方/三方服务间接依赖数据且无push机制），可使用自动更新组件定时自动pull的方式及时更新缓存数据。
* 依赖数据源多甚至异构的场景，可使用自动更新组件在项目内用统一的方式对接所有数据源的刷新缓存诉求。
* 希望减少请求击穿缓存时带来的RT尖刺的场景，可使用自动更新组件的异步刷新缓存能力，减少请求同步击穿缓存的次数，同时降低项目对数据源的可用性依赖。

## Quick Start
```go
package example

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/go-redis/redis"

	"xxx/log"

	"xxx/cacheloader"
)

var (
	localCacherImpl = &cacherImpl{}
	localLoaderImpl = &loaderImpl{}
	key             = "myKey"
)

func init() {
	localCacherImpl.Init()
	localLoaderImpl.Init()
}

type MyObject struct {
	Name    string   `json:"name"`
	Age     int      `json:"age"`
	Actions []string `json:"actions"`
}

func ExampleBasicUsage() {
	ctx := context.Background()
	clr, err := cacheloader.NewBuilder().
		TTLSec(uint64(24 * time.Hour / time.Second)). // 设置缓存24小时过期
		RefreshAfterWriteSec(uint64(1)). // 设置缓存自动刷新最小间隔1s
		RegisterCacher(localCacherImpl).
		RegisterLocker(localCacherImpl).
		RegisterLoader(localLoaderImpl).Build()
	if err != nil {
		log.Fatalf(ctx, "cache loader initialization failed, err: %v", err)
	}

	rawResult, err := clr.Get(ctx, key)
	if err != nil {
		log.Errorf(ctx, "cache loader get value failed, err: %v", err)
	}

	wanted := &MyObject{}
	if err = json.Unmarshal(rawResult, wanted); err == nil {
		log.Infof(ctx, "wanted: %v", wanted)
	}
	// Output: &{myName 18 [eat,read]}
}

/************************************************ Cacher/Locker实现实例（此处基于redis实现） ************************************************/
type cacherImpl struct {
	client *redis.Client
}

func (c *cacherImpl) Init() {
	// redis client
	c.client = redis.NewClient(&redis.Options{
		Addr:       "redis address",
		MaxRetries: 3,
		PoolSize:   10,
	})
}

func (c *cacherImpl) TimeLock(ctx context.Context, key string, duration time.Duration) (bool, error) {
	result, err := c.client.SetNX("lockPrefix"+key, nil, duration).Result()
	if err != nil {
		return false, err
	}
	return result, err
}

func (c *cacherImpl) Get(ctx context.Context, key string) ([]byte, error) {
	resultList, err := c.MGet(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(resultList) == 0 {
		return nil, nil
	}
	return resultList[0], nil
}

func (c *cacherImpl) MGet(ctx context.Context, keys ...string) ([][]byte, error) {
	resultList, err := c.client.MGet(keys...).Result()
	if err != nil {
		return nil, err
	}
	bytesList := make([][]byte, 0, len(resultList))
	for _, result := range resultList {
		if result == nil {
			bytesList = append(bytesList, nil)
			continue
		}
		if str, ok := result.(string); ok {
			// go-redis会将结构体以string返回
			bytesList = append(bytesList, ([]byte)(str))
			continue
		}
	}
	return bytesList, nil
}

func (c *cacherImpl) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	_, err := c.client.Set(key, val, ttl).Result()
	if err != nil {
		return err
	}
	return nil
}

func (c *cacherImpl) TTL(ctx context.Context, key string) (time.Duration, error) {
	// redis返回duration：未设置超时（-1s）、key不存在（-2s）
	duration, err := c.client.TTL(key).Result()
	if err != nil {
		return 0, err
	}
	if duration == -1*time.Second {
		return 1 * time.Second, nil // key未设置超时场景按照1s自动刷新处理 返回值约定见接口定义
	}
	if duration < 0 {
		duration = 0 // key不存在场景无需执行自动更新 返回值约定见接口定义
	}
	return duration, nil
}

/************************************************ Loader实现实例（实际数据源通常为持久化数据源） ************************************************/
type loaderImpl struct {
	KVMap *sync.Map
}

func (l *loaderImpl) Init() {
	l.KVMap = &sync.Map{}
	bytes, _ := json.Marshal(&MyObject{
		Name:    "myName",
		Age:     18,
		Actions: []string{"eat", "read"},
	})
	l.KVMap.Store(key, bytes)
}

func (l *loaderImpl) Load(ctx context.Context, key string) ([]byte, error) {
	resultList, _ := l.Loads(ctx, key)
	return resultList[0], nil
}

func (l *loaderImpl) Loads(ctx context.Context, keys ...string) ([][]byte, error) {
	resultList := make([][]byte, 0, len(keys))
	for _, key := range keys {
		if val, ok := l.KVMap.Load(key); ok {
			rawVal := val.([]byte)
			resultList = append(resultList, rawVal)
			continue
		}
		resultList = append(resultList, nil)
	}
	return resultList, nil
}
```
更多用例见 cacheloader_test.go