package string_cacheloader

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/Luoyuhao/go-cacheloader/helper"
)

type TestStruct struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

var ErrEmpty = fmt.Errorf("")

func (suite *Suite) Test_TTLSec4Invalid() {
	ctx := context.Background()
	clr, err := NewBuilder[*TestStruct]().
		RefreshAfterWriteSec(uint64(1)).
		TTLSec(uint64(3)).
		TTLSec4Invalid(uint64(2)). // set ttlSec4Invalid to leverage cache invalid value, in case of cache miss
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.loader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key1 := "concurrent_test1"
	key2 := "concurrent_test2"

	var wg sync.WaitGroup
	done := make(chan struct{})

	// 启动两个goroutine持续访问数据
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := key1
			if idx == 1 {
				key = key2
			}

			for {
				select {
				case <-done:
					return
				default:
					if rand.Intn(2) == 0 {
						result, err := clr.Get(ctx, key)
						suite.NoError(err)
						suite.Nil(result)
					} else {
						results, err := clr.MGet(ctx, key1, key2)
						suite.NoError(err)
						suite.Equal(2, len(results))
						suite.Nil(results[0])
						suite.Nil(results[1])
					}
					time.Sleep(50 * time.Millisecond) // 稍微降低访问频率
				}
			}
		}(i)
	}

	// 运行1秒后验证缓存值
	time.Sleep(1 * time.Second)
	val1, err := suite.cacher.Get(ctx, key1)
	suite.NoError(err)
	suite.Equal(invalidValue, val1)

	val2, err := suite.cacher.Get(ctx, key2)
	suite.NoError(err)
	suite.Equal(invalidValue, val2)

	// 继续运行4秒
	time.Sleep(4 * time.Second)
	close(done)
	wg.Wait()

	// 检查命中率
	suite.True(suite.cacher.(*cacherImpl).HitRate() > 0.9)
}

func (suite *Suite) Test_TTLSec() {
	ctx := context.Background()
	clr, err := NewBuilder[*TestStruct]().
		TTLSec(uint64(2)). // set ttlSec but not set refreshAfterWriteSec, making the cache behave according to ttlSec
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.loader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key1 := "test1"
	key2 := "test2"
	val1 := &TestStruct{
		Name:  key1,
		Value: int64(1),
	}
	val2 := &TestStruct{
		Name:  key2,
		Value: int64(2),
	}

	// 先存入初始数据
	loader := (suite.loader).(*loaderImpl)
	err = loader.Store(ctx, key1, val1)
	suite.NoError(err)
	err = loader.Store(ctx, key2, val2)
	suite.NoError(err)

	// 启动一个慢速生产者和多个快速消费者
	var wg sync.WaitGroup
	done := make(chan struct{})

	// 慢速生产者 - 每秒更新一次源数据
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := int64(0)
		for {
			select {
			case <-done:
				return
			default:
				count++
				val1.Value = count
				val2.Value = count * 2
				err := loader.Store(ctx, key1, val1)
				suite.NoError(err)
				err = loader.Store(ctx, key2, val2)
				suite.NoError(err)
				time.Sleep(1 * time.Second)
			}
		}
	}()

	// 5个快速消费者
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					if id%2 == 0 {
						result, err := clr.Get(ctx, key1)
						suite.NoError(err)
						suite.NotNil(result)
					} else {
						results, err := clr.MGet(ctx, key1, key2)
						suite.NoError(err)
						suite.Equal(2, len(results))
						suite.NotNil(results[0])
						suite.NotNil(results[1])
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}(i)
	}

	// 运行5秒后检查
	time.Sleep(5 * time.Second)
	close(done)
	wg.Wait()

	// 验证缓存命中率
	suite.True(suite.cacher.(*cacherImpl).HitRate() > 0.9)
}

func (suite *Suite) Test_RefreshAfterWriteSec() {
	ctx := context.Background()
	clr, err := NewBuilder[*TestStruct]().
		RefreshAfterWriteSec(uint64(1)). // set refreshAfterWriteSec to leverage cache update when data source changed
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.loader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key1 := "test1"
	key2 := "test2"
	val1 := &TestStruct{
		Name:  key1,
		Value: int64(1),
	}
	val2 := &TestStruct{
		Name:  key2,
		Value: int64(2),
	}

	// 先存入初始数据
	loader := (suite.loader).(*loaderImpl)
	err = loader.Store(ctx, key1, val1)
	suite.NoError(err)
	err = loader.Store(ctx, key2, val2)
	suite.NoError(err)

	// 启动一个慢速生产者和多个快速消费者
	var wg sync.WaitGroup
	done := make(chan struct{})

	// 慢速生产者 - 每秒更新一次源数据
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := int64(0)
		for {
			select {
			case <-done:
				return
			default:
				count++
				val1.Value = count
				val2.Value = count * 2
				err := loader.Store(ctx, key1, val1)
				suite.NoError(err)
				err = loader.Store(ctx, key2, val2)
				suite.NoError(err)
				time.Sleep(1 * time.Second)
			}
		}
	}()

	// 5个快速消费者
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					if id%2 == 0 {
						result, err := clr.Get(ctx, key1)
						suite.NoError(err)
						suite.NotNil(result)
					} else {
						results, err := clr.MGet(ctx, key1, key2)
						suite.NoError(err)
						suite.Equal(2, len(results))
						suite.NotNil(results[0])
						suite.NotNil(results[1])
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}(i)
	}

	// 运行5秒后检查
	time.Sleep(5 * time.Second)
	close(done)
	wg.Wait()

	// 验证缓存命中率
	suite.True(suite.cacher.(*cacherImpl).HitRate() > 0.9)
}

func (suite *Suite) Test_TTLSecWithRefreshAfterWriteSec_struct() {
	ctx := context.Background()
	clr, err := NewBuilder[*TestStruct]().
		TTLSec(uint64(2)).
		RefreshAfterWriteSec(uint64(1)). // set refreshAfterWriteSec to leverage cache update when data source changed
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.loader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key1 := "test1"
	key2 := "test2"
	val1 := &TestStruct{
		Name:  key1,
		Value: int64(1),
	}
	val2 := &TestStruct{
		Name:  key2,
		Value: int64(2),
	}

	// 先存入初始数据
	loader := (suite.loader).(*loaderImpl)
	err = loader.Store(ctx, key1, val1)
	suite.NoError(err)
	err = loader.Store(ctx, key2, val2)
	suite.NoError(err)

	// 启动一个慢速生产者和多个快速消费者
	var wg sync.WaitGroup
	done := make(chan struct{})

	// 慢速生产者 - 每秒更新一次源数据
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := int64(0)
		for {
			select {
			case <-done:
				return
			default:
				count++
				val1.Value = count
				val2.Value = count * 2
				err := loader.Store(ctx, key1, val1)
				suite.NoError(err)
				err = loader.Store(ctx, key2, val2)
				suite.NoError(err)
				time.Sleep(1 * time.Second)
			}
		}
	}()

	// 5个快速消费者
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					if id%2 == 0 {
						result, err := clr.Get(ctx, key1)
						suite.NoError(err)
						suite.NotNil(result)
					} else {
						results, err := clr.MGet(ctx, key1, key2)
						suite.NoError(err)
						suite.Equal(2, len(results))
						suite.NotNil(results[0])
						suite.NotNil(results[1])
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}(i)
	}

	// 运行5秒后检查
	time.Sleep(5 * time.Second)
	close(done)
	wg.Wait()

	// 验证缓存命中率
	suite.True(suite.cacher.(*cacherImpl).HitRate() > 0.9)
}

func (suite *Suite) Test_TTLSecWithRefreshAfterWriteSec_int() {
	ctx := context.Background()
	clr, err := NewBuilder[int]().
		TTLSec(uint64(2)).
		RefreshAfterWriteSec(uint64(1)). // set refreshAfterWriteSec to leverage cache update when data source changed
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.loader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key1 := "test1"
	key2 := "test2"
	val1 := 1
	val2 := 2

	// 先存入初始数据
	loader := (suite.loader).(*loaderImpl)
	err = loader.Store(ctx, key1, val1)
	suite.NoError(err)
	err = loader.Store(ctx, key2, val2)
	suite.NoError(err)

	// 启动一个慢速生产者和多个快速消费者
	var wg sync.WaitGroup
	done := make(chan struct{})

	// 慢速生产者 - 每秒更新一次源数据
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := 0
		for {
			select {
			case <-done:
				return
			default:
				count++
				val1 += count
				val2 += count * 2
				err := loader.Store(ctx, key1, val1)
				suite.NoError(err)
				err = loader.Store(ctx, key2, val2)
				suite.NoError(err)
				time.Sleep(1 * time.Second)
			}
		}
	}()

	// 5个快速消费者
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					if id%2 == 0 {
						result, err := clr.Get(ctx, key1)
						suite.NoError(err)
						suite.NotEmpty(result)
					} else {
						results, err := clr.MGet(ctx, key1, key2)
						suite.NoError(err)
						suite.Equal(2, len(results))
						suite.NotEmpty(results[0])
						suite.NotEmpty(results[1])
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
		}(i)
	}

	// 运行5秒后检查
	time.Sleep(5 * time.Second)
	close(done)
	wg.Wait()

	// 验证缓存命中率
	suite.True(suite.cacher.(*cacherImpl).HitRate() > 0.9)
}

// 触发panic的资源释放顺序
func (suite *Suite) Test_Panic() {
	ctx := context.Background()
	panicCacher := &panicCacherImpl{}
	clr, err := NewBuilder[*TestStruct]().
		TTLSec(uint64(5)).
		RefreshAfterWriteSec(uint64(1)).
		RefreshTimeout(10 * time.Second).
		RegisterCacher(panicCacher).
		RegisterLoader(suite.loader).
		RegisterLocker(panicCacher).Build()
	suite.NoError(err)

	key := "test_panic"
	_, err = clr.Get(ctx, key)
	suite.NoError(err)

	time.Sleep(10 * time.Millisecond) // 停顿1s 若自动更新过程发生panic会记录error
	suite.NotEqual(ErrEmpty, (helper.Err4Debug.Load()).(error))
}

// 设置自动回源超时
func (suite *Suite) Test_LoadTimeout() {
	ctx := context.Background()
	sleepLoader := &sleepLoaderImpl{KVMap: &sync.Map{}}
	clr, err := NewBuilder[*TestStruct]().
		TTLSec(uint64(5)).
		RefreshAfterWriteSec(uint64(1)).
		RefreshTimeout(100 * time.Millisecond). // 设置自动更新100ms超时
		RegisterCacher(suite.cacher).
		RegisterLoader(sleepLoader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key := "Test_LoadTimeout"
	val := &TestStruct{
		Name:  key,
		Value: 2,
	}
	loader := sleepLoader
	err = loader.Store(ctx, key, val) // 存入数据
	suite.NoError(err)
	result, err := clr.Get(ctx, key)
	suite.NoError(err)
	suite.Equal(*val, *result)

	time.Sleep(1100 * time.Millisecond) // 停顿1.1s在下次Get的时候触发自动更新

	start := time.Now()
	result, err = clr.Get(ctx, key)
	suite.NoError(err)
	suite.Equal(*val, *result)
	suite.True(time.Since(start) < 10*time.Millisecond)

	time.Sleep(200 * time.Millisecond) // 停顿1s 若自动更新过程发生超时则会记录error
	suite.NotEqual(ErrEmpty, (helper.Err4Debug.Load()).(error))
}

// 测试metaCache的长度有限性
func (suite *Suite) Test_metaCache() {
	ctx := context.Background()
	sleepLoader := &sleepLoaderImpl{KVMap: &sync.Map{}}
	clr, err := NewBuilder[*TestStruct]().
		TTLSec(uint64(5)).
		RefreshAfterWriteSec(uint64(1)).
		MetaCacheMaxLen(1).
		RefreshTimeout(100 * time.Millisecond). // 设置自动更新100ms超时
		RegisterCacher(suite.cacher).
		RegisterLoader(sleepLoader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	loader := sleepLoader

	key1 := "test_metacache_limit_1"
	val1 := &TestStruct{
		Name:  key1,
		Value: 1,
	}

	err = loader.Store(ctx, key1, val1) // 存入数据 sleepLoaderImpl KVMap
	suite.NoError(err)
	result1, err := clr.Get(ctx, key1) // 触发回源，key写入值, 写入lru cache
	suite.NoError(err)
	suite.Equal(*val1, *result1)

	time.Sleep(1100 * time.Millisecond) // 停顿1.1s在下次Get的时候触发自动更新

	start := time.Now()
	result1, err = clr.Get(ctx, key1) // 触发自动更新, 读取lru cache
	suite.NoError(err)
	suite.Equal(*val1, *result1)
	suite.True(time.Since(start) < 10*time.Millisecond)

	key2 := "test_metacache_limit_2"
	val2 := &TestStruct{
		Name:  key2,
		Value: 2,
	}

	err = loader.Store(ctx, key2, val2) // 存入数据 sleepLoaderImpl KVMap
	suite.NoError(err)
	result, err := clr.Get(ctx, key2) // 触发回源，key写入值, 写入lru cache
	suite.NoError(err)
	suite.Equal(*val2, *result)

	clr.BaseCacheLoader.RWLock.RLock()
	suite.Equal(1, clr.BaseCacheLoader.MetaCache.Len())
	clr.BaseCacheLoader.RWLock.RUnlock()
}
