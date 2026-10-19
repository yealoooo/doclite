// Package memo 是进程内的短时缓存（硬性规则 16：单个 server 进程）：同一个键在 TTL 内只算一次，同时来的请求合并成
// 一次（single-flight）。给"实时事件一来、几个客户端同时重新取"的聚合查询用（看板、审核计数）。
//
// 键里要带上影响结果的全部东西；调用方用 events.Bus 的 Version 作为键的一部分，项目里一有变化（发了事件）就换键，
// 所以不会读到事件之前的旧结果，TTL 只兜底没发事件的变化（改设置、改显示名）。出错的结果不缓存。
package memo

import (
	"context"
	"errors"
	"sync"
	"time"
)

// maxEntries：条目多于这么多时，插入新条目前清掉过期的。
const maxEntries = 256

// Cache 是一个缓存。零值不能用，用 [New]。
type Cache[K comparable, V any] struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[K]*entry[V]
}

type entry[V any] struct {
	done chan struct{} // 算完时关闭
	at   time.Time     // 算完的时间
	v    V
	err  error
}

// New 建缓存：结果保留 ttl。
func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{ttl: ttl, m: map[K]*entry[V]{}}
}

// Get 取键 k 的结果：TTL 内算过的直接返回；正在算的等它算完；否则调 fn 算。返回的值是共用的，调用方不能改。
// 等别人算时那一次出错（比如发起的请求断开了），自己再算一次（不缓存）。
func (c *Cache[K, V]) Get(ctx context.Context, k K, fn func() (V, error)) (V, error) {
	c.mu.Lock()
	e := c.m[k]
	if e != nil {
		select {
		case <-e.done:
			if e.err != nil || time.Since(e.at) > c.ttl {
				e = nil
			}
		default:
		}
	}
	if e != nil {
		c.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			var zero V
			return zero, context.Cause(ctx)
		}
		if e.err == nil {
			return e.v, nil
		}
		return fn()
	}
	if len(c.m) >= maxEntries {
		c.sweep()
	}
	e = &entry[V]{done: make(chan struct{})}
	c.m[k] = e
	c.mu.Unlock()

	e.err = errPanicked // fn panic 时等着的请求自己再算
	defer func() {
		c.mu.Lock()
		e.at = time.Now()
		close(e.done)
		if e.err != nil && c.m[k] == e {
			delete(c.m, k)
		}
		c.mu.Unlock()
	}()
	e.v, e.err = fn()
	return e.v, e.err
}

var errPanicked = errors.New("memo: 计算时 panic")

// sweep 清掉算完并且过期的条目（在 c.mu 下调用）。
func (c *Cache[K, V]) sweep() {
	for k, e := range c.m {
		select {
		case <-e.done:
			if time.Since(e.at) > c.ttl {
				delete(c.m, k)
			}
		default:
		}
	}
}
