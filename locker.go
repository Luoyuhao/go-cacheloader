package cacheloader

import (
	"context"
	"time"
)

/************************ Locker：分布式锁处理器抽象（不限制分布式锁实现支持 参考建议见方法备注） ************************/
type Locker interface {
	// 针对某个缓存key抢自动超时的分布式锁，该锁用于在锁时间段内（组件会控制在200ms-1s）互斥抢锁失败的（跨进程）并发单元执行更新缓存和回源等操作，因此实现中抢锁失败无需等待，立即返回。
	// 1. KVStore实现（redis/tair）：使用setNX命令，存在时设置key，同时设置超时时间。命令返回1代表抢锁成功，命令返回0代表抢锁失败。
	// 2. DB实现：使用乐观锁更新更新记录的时间字段，update lock_table set expiration = date_add(now(), interval 1 second), version = n + 1 where key = ? and version = n。抢锁前查询出expiration查看锁是否过期，然后update成功代表成功，否则抢锁失败。
	// @ctx：上下文
	// @key：缓存key（建议在key基础上加上前/后缀 以表示key对应的锁 同时避免key和锁同名 尤其是锁和缓存都用同一个KVStore实例实现时 key和锁同名会引入逻辑错误）
	// @duration：锁时长
	TimeLock(ctx context.Context, key string, duration time.Duration) (bool, error)
}
