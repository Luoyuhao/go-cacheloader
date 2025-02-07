package cacheloader

import (
	"context"
	"time"
)

/************************ Locker：分布式锁处理器抽象（不限制分布式锁实现支持 参考建议见方法备注） ************************/
type Locker interface {
	/*
		TimeLock is used as a distributed lock for a cache key, the lock is used to ensure that only one process can update the cache and
		load data during the lock period (the component will control the duration to be between 200ms-1s). If the lock is not acquired, the process will immediately return without waiting.
		1. For KVStore implementation (redis/tair), use setNX command, set key and expiration time when lock is acquired. The command returns 1 when the lock is acquired, and 0 when it is not.
		2. For DB implementation, use optimistic lock to update the expiration time of the record, update lock_table set expiration = date_add(now(),
		 interval 1 second), version = n + 1 where key = ? and version = n. Check the expiration time before updating to see if the lock is expired. If the update is successful, the lock is acquired; otherwise, the lock is not acquired.
	*/
	TimeLock(ctx context.Context, key string, duration time.Duration) (bool, error)
}
