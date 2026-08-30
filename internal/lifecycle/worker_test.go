package lifecycle

import (
    "context"
    "sync/atomic"
    "testing"
    "time"
)

// EI10: Stop must block until the goroutine has actually exited, not just
// until ctx is cancelled. On the bug (cancel-without-wait) Stop returned while
// the goroutine was still mid-work — shutdown landed on a live writer. This
// guard proves Stop is a true join.
func TestWorker_EI10_StopWaitsForExit(t *testing.T) {
    t.Parallel()

    var exited atomic.Bool
    var workStarted atomic.Bool

    w := Start(context.Background(), "test_slow_exit", func(ctx context.Context) {
        workStarted.Store(true)
        <-ctx.Done()
        // simulate cleanup work after ctx cancel — Stop must wait for this
        time.Sleep(50 * time.Millisecond)
        exited.Store(true)
    })

    // ensure goroutine has entered before we stop
    if !waitCond(time.Second, func() bool { return workStarted.Load() }) {
        t.Fatal("worker goroutine never started")
    }

    start := time.Now()
    w.Stop()
    elapsed := time.Since(start)

    if !exited.Load() {
        t.Fatalf("EI10: Stop returned before goroutine exited — cancel-without-wait bug (mid-write stop risk)")
    }
    // Stop must have blocked at least through the 50ms cleanup, proving a join
    if elapsed < 50*time.Millisecond {
        t.Errorf("EI10: Stop returned in %v without waiting for post-cancel cleanup (want >=50ms join)", elapsed)
    }
}

// Stop must be idempotent — the shutdown path may be reached twice (e.g. signal
// + Server.Shutdown). A non-idempotent Stop double-closes / double-waits.
func TestWorker_EI10_StopIdempotent(t *testing.T) {
    t.Parallel()

    var stops int32
    w := Start(context.Background(), "test_idempotent", func(ctx context.Context) {
        <-ctx.Done()
    })

    for i := 0; i < 5; i++ {
        w.Stop()
        stops++
    }
    // no panic, no hang — the 5 Stops all returned cleanly
}

// The goroutine must receive a cancellable context — a Worker that forgot to
// derive a child ctx would have no way to signal exit, so Stop could never join.
func TestWorker_EI10_ContextCancellable(t *testing.T) {
    t.Parallel()

    var sawCancel atomic.Bool
    w := Start(context.Background(), "test_ctx_cancel", func(ctx context.Context) {
        <-ctx.Done()
        sawCancel.Store(true)
    })

    w.Stop()
    if !sawCancel.Load() {
        t.Fatal("EI10: goroutine never observed ctx.Done — child context not cancellable")
    }
}

// A nil parent must not panic (callers may pass nil when there is no parent).
func TestWorker_EI10_NilParentSafe(t *testing.T) {
    t.Parallel()
    var ran atomic.Bool
    w := Start(nil, "test_nil_parent", func(ctx context.Context) {
        ran.Store(true)
        <-ctx.Done()
    })
    w.Stop()
    if !ran.Load() {
        t.Fatal("EI10: nil parent should fall back to Background, goroutine never ran")
    }
}

// TestWorker_H3_RestartsOnPanic: a single panic must NOT permanently kill the
// worker — H3 restarts it. A counter increments across restarts; a non-
// restarting worker would observe exactly 1 invocation.
func TestWorker_H3_RestartsOnPanic(t *testing.T) {
    t.Parallel()
    var invocations atomic.Int32
    w := Start(context.Background(), "test_restart_panic", func(ctx context.Context) {
        n := invocations.Add(1)
        if n <= 2 {
            // First two invocations panic immediately (ran < gracePeriod, so
            // consecutive counter climbs toward the breaker).
            panic("test panic: restart me")
        }
        // Third invocation: run cleanly until cancelled.
        <-ctx.Done()
    })
    // Wait for the third (clean) invocation to start, then Stop. Allow time for
    // the two backoffs (100ms, 200ms) before the clean run.
    if !waitCond(3*time.Second, func() bool { return invocations.Load() >= 3 }) {
        w.Stop()
        t.Fatalf("H3: worker did not restart after panic — invocations=%d, want >=3", invocations.Load())
    }
    w.Stop()
    if got := invocations.Load(); got < 3 {
        t.Fatalf("H3: worker panicked then did not restart — invocations=%d, want >=3", got)
    }
}

// TestWorker_H3_BackoffInterruptedByShutdown: when a panic is followed by a
// backoff sleep, a Stop during that backoff must exit the loop via the
// ctx.Done() select arm — not wait out the full backoff. Proves the
// backoff-during-shutdown branch.
func TestWorker_H3_BackoffInterruptedByShutdown(t *testing.T) {
    t.Parallel()
    var invocations atomic.Int32
    w := Start(context.Background(), "test_backoff_shutdown", func(ctx context.Context) {
        invocations.Add(1)
        // Panic once to enter the backoff path (next backoff = 100ms).
        panic("test panic: then stop during backoff")
    })
    // Give the first panic + the start of the 100ms backoff time to land.
    if !waitCond(time.Second, func() bool { return invocations.Load() >= 1 }) {
        w.Stop()
        t.Fatal("H3: worker never entered its first (panicking) invocation")
    }
    // Stop NOW — during the backoff window. The select must take ctx.Done(),
    // not time.After(backoff), so Stop returns promptly.
    start := time.Now()
    w.Stop()
    if elapsed := time.Since(start); elapsed > time.Second {
        t.Fatalf("H3: Stop during backoff took %s — backoff not interrupted by ctx.Done (expected fast exit)", elapsed)
    }
}

func waitCond(max time.Duration, cond func() bool) bool {
    deadline := time.Now().Add(max)
    for time.Now().Before(deadline) {
        if cond() {
            return true
        }
        time.Sleep(2 * time.Millisecond)
    }
    return false
}
