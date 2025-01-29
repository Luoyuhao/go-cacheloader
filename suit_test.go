package cacheloader

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luoyuhao/go-cacheloader/helper"
	"github.com/stretchr/testify/suite"
)

// Define the suite, and absorb the built-in basic suite
// functionality from testify - including a T() method which
// returns the current testing context
type Suite struct {
	suite.Suite
	cacher Cacher
	loader Loader
	locker Locker
	cancel context.CancelFunc
}

// Make sure that VariableThatShouldStartAtFive is set to five
// before each test
func (suite *Suite) SetupSuite() {
}

// The SetupTest method will be run before every test in the suite.
func (suite *Suite) SetupTest() {
	helper.DebugOn()
	cImpl := &cacherImpl{KVMap: &sync.Map{}}
	suite.cacher = cImpl
	suite.locker = cImpl
	ctx, cancel := context.WithCancel(context.Background())
	suite.cancel = cancel

	go func(ctx context.Context) {
		milSecondTimer := time.NewTicker(5 * time.Millisecond)
		canceled := false
		for {
			select {
			case <-milSecondTimer.C:
				cImpl.KVMap.Range(func(key, value interface{}) bool {
					emptyTime := time.Time{}
					rawVal := value.(*strCacheVal)
					if rawVal.dueTime != emptyTime && rawVal.dueTime.Before(time.Now()) {
						cImpl.mutex4KVMap.Lock()
						defer cImpl.mutex4KVMap.Unlock()
						if value, ok := cImpl.KVMap.Load(key); ok {
							rawVal := value.(*strCacheVal)
							if rawVal.dueTime != emptyTime && rawVal.dueTime.Before(time.Now()) {
								cImpl.KVMap.Delete(key)
							}
						}
					}
					return true
				})
			case <-ctx.Done():
				canceled = true
			}
			if canceled {
				break
			}
		}
	}(ctx)
	suite.loader = &loaderImpl{KVMap: &sync.Map{}}
	// suite.sleepLoader = &sleepLoaderImpl{KVMap: &sync.Map{}, rwLock: &sync.RWMutex{}}
	// suite.panicCacher = &panicCacherImpl{}
}

// The TearDownTest method will be run after every test in the suite.
func (suite *Suite) TearDownTest() {
	helper.Err4Debug.Store(ErrEmpty)
	suite.cancel()
}

func (suite *Suite) TearDownSuite() {
}

// In order for 'go test' to run this suite, we need to create
// a normal test function and pass our suite to suite.Run
func TestSuite(t *testing.T) {
	suite.Run(t, new(Suite))
}

/************************************************ cacherImpl structure ************************************************/
type cacherImpl struct {
	KVMap          *sync.Map
	mutex4KVMap    sync.Mutex
	hitCnt, reqCnt atomic.Int32
}

type strCacheVal struct {
	val     string
	dueTime time.Time
}

func (c *cacherImpl) TimeLock(_ context.Context, key string, duration time.Duration) (bool, error) {
	_, locked := c.KVMap.LoadOrStore("lock-"+key, &strCacheVal{
		val:     "",
		dueTime: time.Now().Add(duration),
	})
	return !locked, nil
}

func (c *cacherImpl) Get(ctx context.Context, key string) (string, error) {
	c.reqCnt.Add(1)
	if val, ok := c.KVMap.Load(key); ok {
		c.hitCnt.Add(1)
		rawVal := val.(*strCacheVal)
		return rawVal.val, nil
	}
	return "", ErrNotFound
}

func (c *cacherImpl) MGet(_ context.Context, keys ...string) ([]interface{}, error) {
	c.reqCnt.Add(int32(len(keys)))
	resultList := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		if val, ok := c.KVMap.Load(key); ok {
			c.hitCnt.Add(1)
			rawVal := val.(*strCacheVal)
			resultList = append(resultList, rawVal.val)
			continue
		}
		resultList = append(resultList, nil)
	}
	return resultList, nil
}

func (c *cacherImpl) Set(_ context.Context, key string, val string, ttl time.Duration) error {
	c.mutex4KVMap.Lock()
	defer c.mutex4KVMap.Unlock()

	if ttl == 0 {
		c.KVMap.Store(key, &strCacheVal{
			val:     val,
			dueTime: time.Time{},
		})
		return nil
	}
	c.KVMap.Store(key, &strCacheVal{
		val:     val,
		dueTime: time.Now().Add(ttl),
	})
	return nil
}

func (c *cacherImpl) TTL(_ context.Context, key string) (time.Duration, error) {
	if val, ok := c.KVMap.Load(key); ok {
		rawVal := val.(*strCacheVal)
		emptyTime := time.Time{}
		if rawVal.dueTime == emptyTime {
			return 0, nil
		}
		return time.Until(rawVal.dueTime), nil
	}
	return 0, ErrNotFound
}

func (c *cacherImpl) HitCnt() int32 {
	return c.hitCnt.Load()
}

func (c *cacherImpl) ReqCnt() int32 {
	return c.reqCnt.Load()
}

func (c *cacherImpl) Reset() {
	c.hitCnt.Store(0)
	c.reqCnt.Store(0)
}

func (c *cacherImpl) HitRate() float64 {
	return float64(c.hitCnt.Load()) / float64(c.reqCnt.Load())
}

/************************************************ loaderImpl structure ************************************************/
type loaderImpl struct {
	KVMap  *sync.Map
	reqCnt atomic.Int32
}

func (l *loaderImpl) Load(ctx context.Context, key string) (interface{}, error) {
	resultList, _ := l.Loads(ctx, key)
	return resultList[0], nil
}

func (l *loaderImpl) Loads(_ context.Context, keys ...string) ([]interface{}, error) {
	l.reqCnt.Add(1)
	resultList := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		if val, ok := l.KVMap.Load(key); ok {
			rawVal := val
			resultList = append(resultList, rawVal)
			continue
		}
		resultList = append(resultList, nil)
	}
	return resultList, nil
}

func (l *loaderImpl) Store(_ context.Context, key string, val interface{}) error {
	l.KVMap.Store(key, val)
	return nil
}

/************************************************ sleepLoaderImpl structure ************************************************/
type sleepLoaderImpl struct {
	KVMap        *sync.Map
	notFirstLoad bool
	rwLock       sync.RWMutex
}

func (l *sleepLoaderImpl) Load(ctx context.Context, key string) (interface{}, error) {
	resultList, err := l.Loads(ctx, key)
	if err != nil {
		return nil, err
	}
	return resultList[0], nil
}

func (l *sleepLoaderImpl) Loads(ctx context.Context, keys ...string) ([]interface{}, error) {
	complete := make(chan struct{})
	done := ctx.Done()
	if done == nil {
		done = make(chan struct{})
	}

	var (
		resultList []interface{}
		err        error
	)
	go func() {
		resultList, err = l.loads(ctx, keys...)
		complete <- struct{}{}
	}()

	select {
	case <-complete:
		return resultList, err
	case <-done:
		return nil, ctx.Err()
	}
}

func (l *sleepLoaderImpl) loads(_ context.Context, keys ...string) ([]interface{}, error) {
	resultList := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		if val, ok := l.KVMap.Load(key); ok {
			l.rwLock.RLock()
			if l.notFirstLoad {
				time.Sleep(15 * time.Second)
			}
			l.rwLock.RUnlock()

			l.rwLock.Lock()
			l.notFirstLoad = true
			l.rwLock.Unlock()

			rawVal := val
			resultList = append(resultList, rawVal)
			continue
		}
		resultList = append(resultList, nil)
	}

	return resultList, nil
}

func (l *sleepLoaderImpl) Store(_ context.Context, key string, val interface{}) error {
	l.KVMap.Store(key, val)
	return nil
}

/************************************************ panicCacherImpl structure ************************************************/
type panicCacherImpl struct{}

func (c *panicCacherImpl) TimeLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func (c *panicCacherImpl) Get(_ context.Context, _ string) (string, error) {
	sample := &TestStruct{}
	result, err := helper.JSONMarshal(sample)
	return string(result), err
}

func (c *panicCacherImpl) MGet(_ context.Context, _ ...string) ([]interface{}, error) {
	return []interface{}{}, nil
}

func (c *panicCacherImpl) Set(_ context.Context, _ string, _ string, _ time.Duration) error {
	return nil
}

func (c *panicCacherImpl) TTL(_ context.Context, _ string) (time.Duration, error) {
	panic("unimplemented")
}
