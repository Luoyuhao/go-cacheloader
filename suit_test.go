package cacheloader

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/Luoyuhao/go-cacheloader/internal"
)

// Define the suite, and absorb the built-in basic suite
// functionality from testify - including a T() method which
// returns the current testing context
type Suite struct {
	suite.Suite
	cacher      Cacher
	loader      Loader
	locker      Locker
	cancel      context.CancelFunc
	panicCacher Cacher
	sleepLoader Loader
}

// Make sure that VariableThatShouldStartAtFive is set to five
// before each test
func (suite *Suite) SetupSuite() {
	internal.DebugOn()
	cImpl := &cacherImpl{KVMap: &sync.Map{}, mutex: &sync.Mutex{}}
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
					rawVal := value.(*cacheVal)
					if rawVal.dueTime.Before(time.Now()) {
						cImpl.mutex.Lock()
						defer cImpl.mutex.Unlock()
						if value, ok := cImpl.KVMap.Load(key); ok {
							rawVal := value.(*cacheVal)
							if rawVal.dueTime.Before(time.Now()) {
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
	suite.sleepLoader = &sleepLoaderImpl{KVMap: &sync.Map{}, rwLock: &sync.RWMutex{}}
	suite.panicCacher = &panicCacherImpl{}
}

// The SetupTest method will be run before every test in the suite.
func (suite *Suite) SetupTest() {
}

// The TearDownTest method will be run after every test in the suite.
func (suite *Suite) TearDownTest() {
}

func (suite *Suite) TearDownSuite() {
	suite.cancel()
}

// In order for 'go test' to run this suite, we need to create
// a normal test function and pass our suite to suite.Run
func TestSuite(t *testing.T) {
	suite.Run(t, new(Suite))
}

/************************************************ cacherImpl structure ************************************************/
type cacherImpl struct {
	KVMap *sync.Map
	mutex *sync.Mutex
}

type cacheVal struct {
	val     []byte
	dueTime time.Time
}

func (c *cacherImpl) TimeLock(_ context.Context, key string, duration time.Duration) (bool, error) {
	_, locked := c.KVMap.LoadOrStore("lock-"+key, &cacheVal{
		val:     nil,
		dueTime: time.Now().Add(duration),
	})
	return !locked, nil
}

func (c *cacherImpl) Get(ctx context.Context, key string) ([]byte, error) {
	resultList, _ := c.MGet(ctx, key)
	return resultList[0], nil
}

func (c *cacherImpl) MGet(_ context.Context, keys ...string) ([][]byte, error) {
	resultList := make([][]byte, 0, len(keys))
	for _, key := range keys {
		if val, ok := c.KVMap.Load(key); ok {
			rawVal := val.(*cacheVal)
			resultList = append(resultList, rawVal.val)
			continue
		}
		resultList = append(resultList, nil)
	}
	return resultList, nil
}

func (c *cacherImpl) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.KVMap.Store(key, &cacheVal{
		val:     val,
		dueTime: time.Now().Add(ttl),
	})
	return nil
}

func (c *cacherImpl) TTL(_ context.Context, key string) (time.Duration, error) {
	if val, ok := c.KVMap.Load(key); ok {
		rawVal := val.(*cacheVal)
		return time.Until(rawVal.dueTime), nil
	}
	return 0, nil
}

/************************************************ loaderImpl structure ************************************************/
type loaderImpl struct {
	KVMap *sync.Map
}

func (l *loaderImpl) Load(ctx context.Context, key string) ([]byte, error) {
	resultList, _ := l.Loads(ctx, key)
	return resultList[0], nil
}

func (l *loaderImpl) Loads(_ context.Context, keys ...string) ([][]byte, error) {
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

func (l *loaderImpl) Store(_ context.Context, key string, val []byte) error {
	l.KVMap.Store(key, val)
	return nil
}

/************************************************ sleepLoaderImpl structure ************************************************/
type sleepLoaderImpl struct {
	KVMap        *sync.Map
	notFirstLoad bool
	rwLock       *sync.RWMutex
}

func (l *sleepLoaderImpl) Load(ctx context.Context, key string) ([]byte, error) {
	resultList, err := l.Loads(ctx, key)
	if err != nil {
		return nil, err
	}
	return resultList[0], nil
}

func (l *sleepLoaderImpl) Loads(ctx context.Context, keys ...string) ([][]byte, error) {
	complete := make(chan struct{})
	done := ctx.Done()
	if done == nil {
		done = make(chan struct{})
	}

	var (
		resultList [][]byte
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

func (l *sleepLoaderImpl) loads(_ context.Context, keys ...string) ([][]byte, error) {
	resultList := make([][]byte, 0, len(keys))
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

			rawVal := val.([]byte)
			resultList = append(resultList, rawVal)
			continue
		}
		resultList = append(resultList, nil)
	}

	return resultList, nil
}

func (l *sleepLoaderImpl) Store(_ context.Context, key string, val []byte) error {
	l.KVMap.Store(key, val)
	return nil
}

/************************************************ panicCacherImpl structure ************************************************/
type panicCacherImpl struct{}

func (c *panicCacherImpl) TimeLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func (c *panicCacherImpl) Get(_ context.Context, _ string) ([]byte, error) {
	return []byte{}, nil
}

func (c *panicCacherImpl) MGet(_ context.Context, _ ...string) ([][]byte, error) {
	return [][]byte{}, nil
}

func (c *panicCacherImpl) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

func (c *panicCacherImpl) TTL(_ context.Context, _ string) (time.Duration, error) {
	panic("unimplemented")
}
