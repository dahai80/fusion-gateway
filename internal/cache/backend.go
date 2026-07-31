package cache

import (
    "log/slog"
    "time"
)

type CacheBackend interface {
    Get(key string) ([]byte, bool)
    Set(key string, value []byte, ttl time.Duration)
    Delete(key string)
    Stats() (hits, misses int64, size int)
    Close()
}

func NewBackend(local *Cache, redis CacheBackend) CacheBackend {
    if redis != nil {
        slog.Info("cache backend: redis (with local L1)")
        return &tieredBackend{l1: local, l2: redis}
    }
    if local != nil {
        slog.Info("cache backend: local LRU")
        return &localBackend{cache: local}
    }
    slog.Info("cache backend: disabled")
    return &noopBackend{}
}

type localBackend struct {
    cache *Cache
}

func (b *localBackend) Get(key string) ([]byte, bool) {
    return b.cache.Get(key)
}

func (b *localBackend) Set(key string, value []byte, ttl time.Duration) {
    b.cache.Set(key, value)
}

func (b *localBackend) Delete(key string) {
    b.cache.Delete(key)
}

func (b *localBackend) Stats() (hits, misses int64, size int) {
    return b.cache.Stats()
}

func (b *localBackend) Close() {}

type tieredBackend struct {
    l1 *Cache
    l2 CacheBackend
}

func (t *tieredBackend) Get(key string) ([]byte, bool) {
    if val, ok := t.l1.Get(key); ok {
        return val, true
    }
    if val, ok := t.l2.Get(key); ok {
        t.l1.Set(key, val)
        return val, true
    }
    return nil, false
}

func (t *tieredBackend) Set(key string, value []byte, ttl time.Duration) {
    t.l1.Set(key, value)
    t.l2.Set(key, value, ttl)
}

func (t *tieredBackend) Delete(key string) {
    t.l1.Delete(key)
    t.l2.Delete(key)
}

func (t *tieredBackend) Stats() (hits, misses int64, size int) {
    return t.l1.Stats()
}

func (t *tieredBackend) Close() {
    t.l2.Close()
}

type noopBackend struct{}

func (n *noopBackend) Get(string) ([]byte, bool)       { return nil, false }
func (n *noopBackend) Set(string, []byte, time.Duration) {}
func (n *noopBackend) Delete(string)                    {}
func (n *noopBackend) Stats() (int64, int64, int)       { return 0, 0, 0 }
func (n *noopBackend) Close()                           {}
