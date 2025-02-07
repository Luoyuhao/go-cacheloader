package cacheloader

import "github.com/Luoyuhao/go-cacheloader/helper"

// SetMaxConcurrency set max concurrency for CacheLoader, default is 500, not support dynamic adjustment
func SetMaxConcurrency(maxConcurrency uint32) {
	helper.DefaultMaxConcurrency = maxConcurrency
}

// SetDowngradeThreshold set downgrade threshold for CacheLoader, 0 (default value) means no downgrade, support dynamic adjustment
func SetDowngradeThreshold(downgradeThreshold uint64) {
	helper.DowngradeThreshold = downgradeThreshold
}
