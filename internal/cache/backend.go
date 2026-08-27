package cache

import (
    "log/slog"
    "sync"
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
    // mu guards the L2→L1 refill in Get against a concurrent Delete. E3
    // (audit): without it, Delete(L1) then Delete(L2) had a window where a
    // concurrent Get saw L1 miss → L2 hit (not yet deleted) → refilled L1
    // with the just-invalidated value = stale revival. The lock makes Delete
    // atomic w.r.t. the refill: a Delete in progress blocks a Get from
    // repopulating L1 from L2. Coarse (one mutex for all keys) is acceptable
    // — Delete is rare (explicit invalidation / hot config reload), Get is
    // the hot path and only takes the lock on the L2-refill branch (L1 hit
    // returns before locking).
    mu sync.Mutex
}

func (t *tieredBackend) Get(key string) ([]byte, bool) {
    if val, ok := t.l1.Get(key); ok {
        return val, true
    }
    // L1 miss → must consult L2 and refill L1. Hold mu so a concurrent Delete
    // cannot leave a stale L2 value visible mid-refill (E3).
    t.mu.Lock()
    defer t.mu.Unlock()
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
    // E3 (audit): hold mu across both deletes so no concurrent Get can slip
    // into the L2→L1 refill window and revive the value being deleted. L1
    // first (fast local), then L2 (network) — under the lock both are atomic
    // w.r.t. Get's refill.
    t.mu.Lock()
    defer t.mu.Unlock()
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
