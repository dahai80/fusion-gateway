package server

import (
    "sync"
    "sync/atomic"
    "testing"
    "time"
)

// TestR2_Coalescer_SingleFetchPerKey: N concurrent same-key Do calls run fn
// exactly ONCE — the leader runs fn, the N-1 waiters block and return without
// running it. This is the R2 stampede fix: before coalescing, all N would
// have fetched upstream independently. Revert (remove the waiter early-return
// + per-key mutex): fetchCount == N → FAIL.
func TestR2_Coalescer_SingleFetchPerKey(t *testing.T) {
    c := newCoalescer()
    var fetchCount int64
    key := "cold-key-stampede"

    var wg sync.WaitGroup
    const callers = 32
    // Barrier so all callers race into Do near-simultaneously.
    start := make(chan struct{})
    for i := 0; i < callers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            <-start
            c.Do(key, func() {
                atomic.AddInt64(&fetchCount, 1)
                // Hold the leader slot so waiters pile up behind it and
                // can't sneak through as independent leaders.
                time.Sleep(20 * time.Millisecond)
            })
        }()
    }
    close(start)
    wg.Wait()

    if got := atomic.LoadInt64(&fetchCount); got != 1 {
        t.Errorf("R2: expected 1 upstream fetch for %d concurrent same-key callers, got %d (stampede — coalescer not deduping)", callers, got)
    }
}

// TestR2_Coalescer_DistinctKeysIndependent: different keys run fn in parallel
// (no false serialization). A waiter on key A must not block key B.
func TestR2_Coalescer_DistinctKeysIndependent(t *testing.T) {
    c := newCoalescer()
    var inflight atomic.Int64
    var maxInflight atomic.Int64

    run := func(key string) {
        c.Do(key, func() {
            cur := inflight.Add(1)
            for {
                prev := maxInflight.Load()
                if cur <= prev || maxInflight.CompareAndSwap(prev, cur) {
                    break
                }
            }
            time.Sleep(15 * time.Millisecond)
            inflight.Add(-1)
        })
    }

    var wg sync.WaitGroup
    keys := []string{"k-A", "k-B", "k-C", "k-D"}
    start := make(chan struct{})
    for _, k := range keys {
        wg.Add(1)
        go func(key string) {
            defer wg.Done()
            <-start
            run(key)
        }(k)
    }
    close(start)
    wg.Wait()

    // 4 distinct keys should overlap (run concurrently). If coalescer
    // serialized ALL keys under one global lock, maxInflight would be 1.
    if got := maxInflight.Load(); got < 2 {
        t.Errorf("R2: distinct keys should run concurrently (maxInflight>=2), got %d — coalescer over-serializing distinct keys", got)
    }
}

// TestR2_Coalescer_EntryRemovedAfterRun: the lock entry is cleaned up so the
// map does not grow unbounded across distinct keys over time.
func TestR2_Coalescer_EntryRemovedAfterRun(t *testing.T) {
    c := newCoalescer()
    for i := 0; i < 100; i++ {
        key := "key-" + string(rune('a'+(i%26))) + string(rune('0'+(i%10)))
        c.Do(key, func() {})
    }
    c.mu.Lock()
    leftover := len(c.calls)
    c.mu.Unlock()
    if leftover != 0 {
        t.Errorf("R2: expected 0 leftover lock entries after runs, got %d (map grows unbounded — leak)", leftover)
    }
}

// TestR2_Coalescer_SequentialSameKeyRefetches: after a leader finishes, a
// later caller for the same key runs fn again (it is a NEW coalescing group,
// not permanently suppressed). Confirms entry cleanup enables re-fetch.
func TestR2_Coalescer_SequentialSameKeyRefetches(t *testing.T) {
    c := newCoalescer()
    var count int64
    key := "re-run-key"
    c.Do(key, func() { count++ })
    c.Do(key, func() { count++ })
    c.Do(key, func() { count++ })
    if count != 3 {
        t.Errorf("R2: sequential same-key calls should each run fn (3 groups), got %d", count)
    }
}
