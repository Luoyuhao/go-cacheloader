package cacheloader

import "errors"

var (
	ErrDowngraded = errors.New("cacheloader: request or source concurrency limit reached")
	ErrNotFound   = errors.New("cacheloader: cache not found")
	ErrClosed     = errors.New("cacheloader: closed")
)
