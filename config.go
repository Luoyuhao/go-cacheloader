package cacheloader

import (
	"context"
	"fmt"
	"time"
)

// Config is copied by New. Zero durations select defaults except TTL and
// RefreshAfter: zero TTL means no expiration, zero RefreshAfter disables refresh.
type Config struct {
	TTL          time.Duration
	NegativeTTL  time.Duration
	RefreshAfter time.Duration
	// SyncRefresh waits for refreshes on cache hits and serves the observed
	// snapshot if refresh fails. RefreshAfter must be positive to enable it.
	SyncRefresh    bool
	LoadTimeout    time.Duration
	CacheTimeout   time.Duration
	MaxConcurrency int
	// BatchSize bounds each MGet cache/source batch. Zero defaults to 100; the
	// effective batch size is also capped by MaxConcurrency (in-flight keys).
	BatchSize int
	// RequestsPerSecond optionally limits public Get/MGet calls per instance; 0 disables it.
	RequestsPerSecond int
	// OnError observes cache failures and background errors. It may be called
	// concurrently, must return promptly, and must not call Close on this loader.
	OnError func(context.Context, error)
}

func (c Config) WithDefaults() (Config, error) {
	if c.TTL < 0 || c.NegativeTTL < 0 || c.RefreshAfter < 0 || c.LoadTimeout < 0 || c.CacheTimeout < 0 || c.MaxConcurrency < 0 || c.RequestsPerSecond < 0 || c.BatchSize < 0 {
		return c, fmt.Errorf("cacheloader: durations and concurrency must not be negative")
	}
	if c.NegativeTTL == 0 {
		c.NegativeTTL = time.Second
	}
	if c.LoadTimeout == 0 {
		c.LoadTimeout = 2 * time.Second
	}
	if c.CacheTimeout == 0 {
		c.CacheTimeout = 200 * time.Millisecond
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 500
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	return c, nil
}
