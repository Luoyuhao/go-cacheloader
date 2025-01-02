package cacheloader

import "github.com/Luoyuhao/go-cacheloader/internal"

// 设置CacheLoader全局最大并发数（多个实例的最大并发数），默认最大并发数为500，不支持动态调整。
func SetMaxConcurrency(maxConcurrency uint32) {
	internal.DefaultMaxConcurrency = maxConcurrency
}

// 设置CacheLoader全局限流降级阈值，0（默认值）表示不限流，支持动态调整。
func SetDowngradeThreshold(downgradeThreshold uint64) {
	internal.DowngradeThreshold = downgradeThreshold
}
