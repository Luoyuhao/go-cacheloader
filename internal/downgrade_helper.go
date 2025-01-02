package internal

import (
	"sync"
	"time"

	"go.uber.org/atomic"
)

var (
	DowngradeThreshold = uint64(0)
	qps                = &QPS{CurrentSec: time.Now().Second()} // 持有当前秒的qps信息，用于限流降级判断。
	rwLock             = &sync.RWMutex{}
)

type QPS struct {
	CurrentSec int
	CurrentQPS atomic.Uint64
}

// 判断是否达到限流降级阈值
func Downgraded() bool {
	if DowngradeThreshold == 0 {
		// 阈值为0时不限流
		return false
	}

	return rwLockAndCheck()
}

func rwLockAndCheck() bool {
	rwLock.RLock()
	if qps.CurrentSec == time.Now().Second() {
		defer rwLock.RUnlock()
		// 秒未切换直接判断降级
		return downgraded()
	}
	rwLock.RUnlock()
	return wLockAndCheck()
}

func wLockAndCheck() bool {
	rwLock.Lock()
	defer rwLock.Unlock()
	if qps.CurrentSec == time.Now().Second() {
		// 秒未切换直接判断降级
		return downgraded()
	}
	qps.CurrentSec = time.Now().Second()
	qps.CurrentQPS.Store(0)
	return downgraded()
}

func downgraded() bool {
	return qps.CurrentQPS.Inc() >= DowngradeThreshold
}
