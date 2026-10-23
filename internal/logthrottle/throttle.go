package logthrottle

import (
    "sync"
    "time"
)

// Throttle rate-limits recurring log emissions per key (e.g. "id|reason") so
// a permanently failing subsystem cannot flood the log (#281 heartbeat
// denials, #283 OTel export errors): the first occurrence passes immediately;
// repeats inside the interval are dropped. The caller still increments its
// own metric counter for every occurrence, so the condition stays observable
// in /metrics even while the log line is suppressed.
type Throttle struct {
    interval time.Duration
    mu       sync.Mutex
    last     map[string]time.Time
}

// maxKeys bounds the key set. Keys are built from bounded inputs (module ids,
// error strings), but a path-sourced id or a churning upstream error message
// could grow the map without bound — on overflow the cheapest safe policy is
// to drop the table and start a fresh interval.
const maxKeys = 1024

func New(interval time.Duration) *Throttle {
    if interval <= 0 {
        interval = 5 * time.Minute
    }
    return &Throttle{interval: interval, last: make(map[string]time.Time)}
}

// Allow reports whether the caller should emit its log line for key now.
func (t *Throttle) Allow(key string) bool {
    now := time.Now()
    t.mu.Lock()
    defer t.mu.Unlock()
    if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
        return false
    }
    if len(t.last) >= maxKeys {
        t.last = make(map[string]time.Time)
    }
    t.last[key] = now
    return true
}
