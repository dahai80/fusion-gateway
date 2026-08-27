package memory

import (
    "sync"
    "sync/atomic"
    "testing"
    "time"
)

// TestDebouncedPersister_CoalescesBurst: N Schedule calls within the window
// fire cb exactly once after the debounce elapses.
// Guard: if Schedule did not reset an armed timer (fired once per Schedule),
// cb fires N times.
func TestDebouncedPersister_CoalescesBurst(t *testing.T) {
    var calls int32
    p := newDebouncedPersister("test", 30*time.Millisecond, func() {
        atomic.AddInt32(&calls, 1)
    })
    for i := 0; i < 50; i++ {
        p.Schedule()
        time.Sleep(2 * time.Millisecond)
    }
    if !p.Pending() {
        t.Fatal("timer not armed after Schedule burst")
    }
    time.Sleep(80 * time.Millisecond)
    if got := atomic.LoadInt32(&calls); got != 1 {
        t.Fatalf("cb fired %d times, want 1 (burst must coalesce)", got)
    }
    if p.Pending() {
        t.Fatal("timer still armed after fire")
    }
}

// TestDebouncedPersister_FlushDrainsSynchronously: Flush stops the pending
// timer and runs cb immediately, so the last burst reaches disk before return.
// Guard: if Flush only stopped the timer without running cb, calls stays 0.
func TestDebouncedPersister_FlushDrainsSynchronously(t *testing.T) {
    var calls int32
    p := newDebouncedPersister("test", 5*time.Second, func() {
        atomic.AddInt32(&calls, 1)
    })
    p.Schedule()
    if !p.Pending() {
        t.Fatal("timer not armed")
    }
    start := time.Now()
    p.Flush()
    if time.Since(start) > time.Second {
        t.Fatalf("Flush blocked %v, should be synchronous (cb already armed not awaited)", time.Since(start))
    }
    if got := atomic.LoadInt32(&calls); got != 1 {
        t.Fatalf("cb fired %d times after Flush, want 1", got)
    }
    if p.Pending() {
        t.Fatal("timer armed after Flush")
    }
}

// TestDebouncedPersister_NilCallbackNoOp: a nil-cb persister is memory-only;
// Schedule/Flush never panic and never fire. We let the timer fire (longer than
// the debounce window) so a missing nil guard dereferences the nil cb and panics.
// Guard: if Schedule skipped the nil-cb guard it would arm a timer whose cb is
// nil → panic on fire.
func TestDebouncedPersister_NilCallbackNoOp(t *testing.T) {
    p := newDebouncedPersister("test", 15*time.Millisecond, nil)
    p.Schedule()
    if p.Pending() {
        t.Fatal("nil-cb persister should never arm a timer")
    }
    // Let the (not-armed) window elapse; with the guard this is a no-op, without
    // it the armed timer fires and dereferences nil cb → panic caught below.
    time.Sleep(40 * time.Millisecond)
    p.Flush()
    if p.Pending() {
        t.Fatal("nil-cb persister should never arm a timer")
    }
}

// TestDebouncedPersister_ReArmsAfterFire: after the timer fires, a new Schedule
// arms a fresh timer (the post-fire nil-timer path must allow re-arming).
// Guard: if the timer were not reset to nil after firing, the second Schedule
// would Reset a fired timer and cb never runs again.
func TestDebouncedPersister_ReArmsAfterFire(t *testing.T) {
    var calls int32
    p := newDebouncedPersister("test", 20*time.Millisecond, func() {
        atomic.AddInt32(&calls, 1)
    })
    p.Schedule()
    time.Sleep(50 * time.Millisecond)
    if got := atomic.LoadInt32(&calls); got != 1 {
        t.Fatalf("first fire: %d, want 1", got)
    }
    p.Schedule()
    if !p.Pending() {
        t.Fatal("second Schedule did not re-arm after first fire")
    }
    time.Sleep(50 * time.Millisecond)
    if got := atomic.LoadInt32(&calls); got != 2 {
        t.Fatalf("second fire: %d, want 2", got)
    }
}

// TestDebouncedPersister_DefaultWindow: d<=0 falls back to the 2s default.
// Guard: a zero window would fire instantly (AfterFunc(0)) and coalescing breaks.
func TestDebouncedPersister_DefaultWindow(t *testing.T) {
    p := newDebouncedPersister("test", 0, func() {})
    if p.d != 2*time.Second {
        t.Fatalf("d<=0 did not fall back to 2s default, got %v", p.d)
    }
}

// TestDebouncedPersister_ConcurrentSchedule: concurrent Schedule calls are safe
// (no race on the timer pointer) and still coalesce to one fire.
func TestDebouncedPersister_ConcurrentSchedule(t *testing.T) {
    var calls int32
    p := newDebouncedPersister("test", 30*time.Millisecond, func() {
        atomic.AddInt32(&calls, 1)
    })
    var wg sync.WaitGroup
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            p.Schedule()
        }()
    }
    wg.Wait()
    time.Sleep(80 * time.Millisecond)
    if got := atomic.LoadInt32(&calls); got != 1 {
        t.Fatalf("concurrent burst fired %d times, want 1", got)
    }
}
