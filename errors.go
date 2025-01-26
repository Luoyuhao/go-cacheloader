package cacheloader

import "errors"

var (
	ErrDowngraded   = errors.New("CacheLoader downgraded")
	ErrNotSupported = errors.New("CacheLoader not supported")
	ErrNotFound     = errors.New("CacheLoader not found")

	errInvalidValue = errors.New("CacheLoader-invalid-cache-value")
)

func ThrowErrWhenInvalidValue[T any]() error {
	return errInvalidValue
}
