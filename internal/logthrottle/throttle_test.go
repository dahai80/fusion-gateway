package logthrottle

import (
    "testing"
    "time"
)

func TestThrottle_FirstOccurrenceAllowed(t *testing.T) {
    th := New(time.Minute)
    if !th.Allow("fusion-agent-studio|invalid") {
        t.Fatal("first occurrence must be allowed")
    }
}

func TestThrottle_RepeatsWithinIntervalSuppressed(t *testing.T) {
    th := New(time.Minute)
    th.Allow("k")
    for i := 0; i < 10; i++ {
        if th.Allow("k") {
            t.Fatalf("repeat %d inside interval must be suppressed", i)
        }
    }
}

func TestThrottle_DistinctKeysIndependent(t *testing.T) {
    th := New(time.Minute)
    th.Allow("a")
    if !th.Allow("b") {
        t.Fatal("distinct key must not be suppressed by another key's state")
    }
}

func TestThrottle_IntervalElapseAllowsAgain(t *testing.T) {
    th := New(10 * time.Millisecond)
    th.Allow("k")
    time.Sleep(15 * time.Millisecond)
    if !th.Allow("k") {
        t.Fatal("occurrence after interval must be allowed")
    }
}

func TestThrottle_NonPositiveIntervalDefaults(t *testing.T) {
    th := New(0)
    th.Allow("k")
    if th.Allow("k") {
        t.Fatal("zero interval must fall back to a positive default, not allow-all")
    }
}

func TestThrottle_KeyOverflowResetsTable(t *testing.T) {
    th := New(time.Hour)
    th.Allow("seed")
    for i := 0; i < maxKeys; i++ {
        th.Allow(string(rune('a'+i%26)) + "-" + time.Now().Format("150405.000000000") + "-" + string(rune(i)))
    }
    th.mu.Lock()
    size := len(th.last)
    th.mu.Unlock()
    if size > maxKeys {
        t.Fatalf("key table exceeded cap: %d > %d", size, maxKeys)
    }
    if !th.Allow("fresh-key-after-overflow") {
        t.Fatal("post-overflow key must be allowed")
    }
}
