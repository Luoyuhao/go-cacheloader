package cacheloader

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Luoyuhao/go-cacheloader/helper"
	"go.uber.org/atomic"
)

type testStruct struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

var emptyErr = fmt.Errorf("")

// 基本用法：
// CASE1、Builder模式创建CacheLoader实例。
// CASE2、Get/MGet初次回源，源值不存在，缓存无效值防雪崩。（用户无感）。
// CASE3、Get/MGet初次回源，源值存在，缓存回源值。
// CASE4、数据源变更数据，持续Get触发固定间隔后缓存与数据源同步。
func (suite *Suite) Test_Usage() {
	ctx := context.Background()
	// CASE1：builder模式创建CacheLoader实例
	clr, err := NewBuilder[*testStruct]().
		RefreshAfterWriteSec(uint64(1)).
		TTLSec(uint64(3)).
		TTLSec4Invalid(uint64(2)).
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.loader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	// CASE2：数据源不存在数据，初次回源，返回回源值，缓存无效值。
	key3 := "test3"
	key4 := "test4"
	result, err := clr.Get(ctx, key3)
	suite.NoError(err)
	suite.Nil(result)

	resultList, err := clr.MGet(ctx, key3, key4)
	suite.NoError(err)
	suite.Equal(2, len(resultList))
	suite.Nil(resultList[0]) // key3: 数据源不存在，cache无效值，返回无效值, 触发缓存自动更新
	suite.Nil(resultList[1]) // key4: 数据源不存在，cache为空，触发回源，返回回源值，并缓存无效值

	// CASE3：数据源存在数据，初次回源，返回回源值，缓存回源值。
	key1 := "test1"
	key2 := "test2"
	val1 := &testStruct{
		Name:  key1,
		Value: 1,
	}
	val2 := &testStruct{
		Name:  key2,
		Value: 2,
	}
	loader := (suite.loader).(*loaderImpl)
	err = loader.Store(ctx, key1, val1) // 存入数据
	suite.NoError(err)
	err = loader.Store(ctx, key2, val2) // 存入数据
	suite.NoError(err)

	resultList, err = clr.MGet(ctx, key1, key2) // 回源
	suite.NoError(err)
	suite.Equal(2, len(resultList))
	for _, result := range resultList {
		if result.Name == key1 {
			suite.Equal(*val1, *result)
		} else if result.Name == key2 {
			suite.Equal(*val2, *result)
		} else {
			suite.NoError(fmt.Errorf("未符合预期命中数据"))
		}
	}

	// CASE4：数据源变更数据 持续访问触发缓存自动更新（更新间隔最快为1s）
	var (
		start, end time.Time
		ended      atomic.Bool
	)
	start = time.Now()
	valUpdated := &testStruct{
		Name:  "babaaba",
		Value: 3,
	}
	err = loader.Store(ctx, key1, valUpdated) // 更新数据源中key1对应的值
	suite.NoError(err)
	group := &sync.WaitGroup{}
	// 1、启动3个并发线程持续访问key1数据（3000QPS）
	// 2、3个并发线程持续测试缓存值是否与数据源同步
	// 3、检查到缓存与数据源同步的延时是否与设置的1S吻合（RefreshAfterWriteSec=1s）
	for i := 0; i < 3; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			milSecondTimer := time.NewTicker(1 * time.Millisecond)
			for {
				<-milSecondTimer.C
				result, err := clr.Get(ctx, key1)
				suite.NoError(err)
				if *result == *val1 {
					continue
				} else if *result == *valUpdated {
					if ended.CompareAndSwap(false, true) {
						end = time.Now() // 缓存更新时设置结束时间并退出
					}
					break
				} else {
					suite.NoError(fmt.Errorf("未符合预期命中数据"))
					break
				}
			}
		}()
	}
	group.Wait()
	// 由于锁等消耗时间 误差范围在毫秒级别(50)
	suite.True(end.Sub(start) <= 1*time.Second+50*time.Millisecond)
	suite.True(end.Sub(start) >= 1*time.Second-50*time.Millisecond)
	fmt.Println(end.Sub(start))
}

// // cacher返回值语义约定：存入nil->nil，存入[]byte{}->[]byte{}
// func (suite *Suite) Test_CacherImpl() {
// 	// []byte{}
// 	ctx := context.Background()
// 	err := suite.cacher.Set(ctx, "test_1", []byte{}, 1*time.Second)
// 	suite.NoError(err)

// 	result, err := suite.cacher.Get(ctx, "test_1")
// 	suite.NoError(err)
// 	suite.Equal([]byte{}, result)

// 	// nil
// 	err = suite.cacher.Set(ctx, "test_2", nil, 1*time.Second)
// 	suite.NoError(err)

// 	result, err = suite.cacher.Get(ctx, "test_2")
// 	suite.NoError(err)
// 	suite.Nil(result)
// }

// 触发panic的资源释放顺序
func (suite *Suite) Test_Panic() {
	ctx := context.Background()
	clr, err := NewBuilder[*testStruct]().
		TTLSec(uint64(5)).
		RefreshAfterWriteSec(uint64(1)).
		RefreshTimeout(10 * time.Second).
		RegisterCacher(suite.panicCacher).
		RegisterLoader(suite.loader).
		RegisterLocker((suite.panicCacher).(*panicCacherImpl)).Build()
	suite.NoError(err)

	key := "test_panic"
	_, err = clr.Get(ctx, key)
	suite.NoError(err)

	time.Sleep(10 * time.Millisecond) // 停顿1s 若自动更新过程发生panic会记录error
	suite.NotEqual(emptyErr, (helper.Err4Debug.Load()).(error))
	helper.Err4Debug.Store(emptyErr)
}

// 设置自动回源超时
func (suite *Suite) Test_LoadTimeout() {
	ctx := context.Background()
	clr, err := NewBuilder[*testStruct]().
		TTLSec(uint64(5)).
		RefreshAfterWriteSec(uint64(1)).
		RefreshTimeout(100 * time.Millisecond). // 设置自动更新100ms超时
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.sleepLoader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	key := "Test_LoadTimeout"
	val := &testStruct{
		Name:  key,
		Value: 2,
	}
	loader := (suite.sleepLoader).(*sleepLoaderImpl)
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
	suite.NotEqual(emptyErr, (helper.Err4Debug.Load()).(error))
	helper.Err4Debug.Store(emptyErr)
}

// 测试metaCache的长度有限性
func (suite *Suite) Test_metaCache() {
	ctx := context.Background()

	clr, err := NewBuilder[*testStruct]().
		TTLSec(uint64(5)).
		RefreshAfterWriteSec(uint64(1)).
		MetaCacheMaxLen(1).
		RefreshTimeout(100 * time.Millisecond). // 设置自动更新100ms超时
		RegisterCacher(suite.cacher).
		RegisterLoader(suite.sleepLoader).
		RegisterLocker(suite.locker).Build()
	suite.NoError(err)

	loader := (suite.sleepLoader).(*sleepLoaderImpl)

	key1 := "test_metacache_limit_1"
	val1 := &testStruct{
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
	val2 := &testStruct{
		Name:  key2,
		Value: 2,
	}

	err = loader.Store(ctx, key2, val2) // 存入数据 sleepLoaderImpl KVMap
	suite.NoError(err)
	result, err := clr.Get(ctx, key2) // 触发回源，key写入值, 写入lru cache
	suite.NoError(err)
	suite.Equal(*val2, *result)

	clr.rwLock.RLock()
	suite.Equal(1, clr.metaCache.Len())
	clr.rwLock.RUnlock()
}
