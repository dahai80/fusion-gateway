package safego

// H3 guard tests: GoRestart must restart fn after a panic (so a single panic
// no longer permanently kills a long-lived worker), back off between restarts,
// and trip a circuit breaker after too many consecutive rapid panics. A clean
// return (no panic) is terminal — no restart. Revert GoRestart to recover-only
// (the old Go) → TestH3_RestartsAfterPanic fails: the worker never re-runs.

import (
    "sync/atomic"
    "testing"
    "time"
)

// TestH3_RestartsAfterPanic: a fn that panics once then runs cleanly must be
// restarted by GoRestart. The old recover-only Go exits after the first panic
// and never re-runs fn.
func TestH3_RestartsAfterPanic(t *testing.T) {
    var calls atomic.Int64
    done := make(chan struct{})
    before := TotalRestarts()
    GoRestart("h3_recover_after_one_panic", func() {
        n := calls.Add(1)
        if n == 1 {
            panic("first call panics")
        }
        // Second call runs cleanly → terminal.
        close(done)
    })
    select {
    case <-done:
    case <-time.After(5 * time.Second):
        t.Fatalf("H3: GoRestart did not re-run fn after a panic (worker permanently dead)")
    }
    if got := TotalRestarts(); got <= before {
        t.Fatalf("H3: TotalRestarts must increase after a panic-restart, got %d (before %d)", got, before)
    }
}

// TestH3_NoRestartOnCleanReturn: a fn that returns without panicking must NOT
// be restarted. GoRestart's restart is panic-only; a clean exit is terminal.
func TestH3_NoRestartOnCleanReturn(t *testing.T) {
    var calls atomic.Int64
    done := make(chan struct{})
    before := TotalRestarts()
    GoRestart("h3_clean_return", func() {
        calls.Add(1)
        close(done)
    })
    <-done
    // Give any erroneous restart a moment to fire.
    time.Sleep(100 * time.Millisecond)
    if got := calls.Load(); got != 1 {
        t.Fatalf("H3: GoRestart must not restart a cleanly-returned fn, got %d calls (want 1)", got)
    }
    if got := TotalRestarts(); got != before {
        t.Fatalf("H3: TotalRestarts must not increase on clean return, got %d (before %d)", got, before)
    }
}

// TestH3_CircuitBreakerTrips: a fn that ALWAYS panics must be stopped by the
// circuit breaker after maxConsecutive rapid panics — it must NOT loop forever
// burning CPU. Uses a tight test config (maxConsecutive=3, 1ms backoff) so the
// breaker trips in milliseconds; the default config (10 panics, exponential
// backoff to 30s) takes ~50s and is not exercised in unit tests.
func TestH3_CircuitBreakerTrips(t *testing.T) {
    var calls atomic.Int64
    // tightCfg trips after 3 consecutive panics with 1ms backoff.
    tightCfg := restartCfg{
        baseBackoff:    time.Millisecond,
        maxBackoff:     10 * time.Millisecond,
        gracePeriod:    time.Hour, // every panic counts (none runs an hour)
        maxConsecutive: 3,
    }
    goRestartWithCfg("h3_always_panics", func() {
        calls.Add(1)
        panic("always panics")
    }, tightCfg)
    // If the breaker works, the goroutine exits after ~3 panics. If it does
    // NOT, calls climbs unbounded; sample after a generous window.
    time.Sleep(500 * time.Millisecond)
    got := calls.Load()
    // Breaker trips at consecutive > maxConsecutive, so max 4 calls (3 panics
    // counted, trip on the 4th). Allow slack for scheduler.
    if got > 10 {
        t.Fatalf("H3: circuit breaker failed to trip — fn ran %d times (breaker should stop rapid panic loop at ~%d)", got, tightCfg.maxConsecutive+1)
    }
}
