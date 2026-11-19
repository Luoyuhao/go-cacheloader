// Package memory implements a bounded, process-local snapshot cache.
package memory

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
)

type entry struct {
	key, value string
	expires    time.Time
}
type Cache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

func New(capacity int) (*Cache, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("memory: capacity must be positive")
	}
	return &Cache{capacity: capacity, items: make(map[string]*list.Element), order: list.New()}, nil
}
func (c *Cache) current(key string) *list.Element {
	e := c.items[key]
	if e == nil {
		return nil
	}
	v := e.Value.(entry)
	if !v.expires.IsZero() && !time.Now().Before(v.expires) {
		c.order.Remove(e)
		delete(c.items, key)
		return nil
	}
	return e
}
func (c *Cache) Get(ctx context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e := c.current(key)
	if e == nil {
		return "", cacheloader.ErrNotFound
	}
	c.order.MoveToFront(e)
	return e.Value.(entry).value, nil
}
func (c *Cache) CompareAndSwap(ctx context.Context, key string, previous *string, value string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ttl < 0 {
		return false, fmt.Errorf("memory: negative TTL")
	}
	e := c.current(key)
	if previous == nil {
		if e != nil {
			return false, nil
		}
	} else if e == nil || e.Value.(entry).value != *previous {
		return false, nil
	}
	v := entry{key: key, value: value}
	if ttl > 0 {
		v.expires = time.Now().Add(ttl)
	}
	if e != nil {
		e.Value = v
		c.order.MoveToFront(e)
	} else {
		c.items[key] = c.order.PushFront(v)
	}
	for len(c.items) > c.capacity {
		old := c.order.Back()
		delete(c.items, old.Value.(entry).key)
		c.order.Remove(old)
	}
	return true, nil
}

// MGet reads an ordered batch under one lock, preserving missing positions.
func (c *Cache) MGet(ctx context.Context, keys ...string) ([]cacheloader.Result[string], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]cacheloader.Result[string], len(keys))
	for i, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e := c.current(key); e != nil {
			c.order.MoveToFront(e)
			out[i] = cacheloader.Result[string]{Value: e.Value.(entry).value, Found: true}
		}
	}
	return out, nil
}
