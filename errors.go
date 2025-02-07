package cacheloader

import "errors"

var (
	ErrDowngraded   = errors.New("CacheLoader downgraded")    // Downgraded when CacheLoader max concurrency is reached
	ErrNotSupported = errors.New("CacheLoader not supported") // Not supported when cache middleware not support TTL
	ErrNotFound     = errors.New("CacheLoader not found")     // Not found when cache is not found
)
