package server

import (
    "log/slog"
    "sync"
)

// coalescer deduplicates concurrent identical operations per key (R2 audit:
// cold-key cache stampede — N concurrent same-key misses each fired an
// upstream fetch, N× cost/limit). The first caller (leader) for a key runs
// fn while holding a per-key mutex; concurrent same-key callers (waiters)
// block until the leader returns, then proceed. Callers MUST re-check the
// shared state fn mutates (e.g. the cache) after Do returns: the leader may
// have failed to populate it, in which case the caller falls through to its
// own independent fetch. Lock entries are removed once the leader finishes so
// the map does not grow unbounded.
type coalescer struct {
    mu    sync.Mutex
    calls map[string]*coalescedCall
}

type coalescedCall struct {
    mu sync.Mutex
}

func newCoalescer() *coalescer {
    return &coalescer{calls: make(map[string]*coalescedCall)}
}

// Do runs fn exactly once per concurrent group for key: the leader runs fn
// (with the per-key mutex held), waiters block on that mutex until the leader
// returns. The leader publishes the entry with the mutex already held, so a
// waiter that arrives after the entry exists is guaranteed to block on the
// leader (it can never acquire the per-key mutex first and race through).
func (c *coalescer) Do(key string, fn func()) {
    c.mu.Lock()
    if call, ok := c.calls[key]; ok {
        c.mu.Unlock()
        call.mu.Lock()
        call.mu.Unlock()
        return
    }
    call := &coalescedCall{}
    call.mu.Lock()
    c.calls[key] = call
    c.mu.Unlock()
    defer func() {
        c.mu.Lock()
        delete(c.calls, key)
        c.mu.Unlock()
        call.mu.Unlock()
    }()
    fn()
}

// forget drops a pending entry without running fn. Used when a caller decides
// before Do that it should not coalesce (e.g. cache disabled); kept for
// completeness — currently Do handles the skip at the call site.
func (c *coalescer) forget(key string) {
    c.mu.Lock()
    delete(c.calls, key)
    c.mu.Unlock()
    slog.Debug("coalescer forgot key", "key", key)
}
