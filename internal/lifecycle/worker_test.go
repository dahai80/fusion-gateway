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

// Done channel must close when the goroutine exits on its own (not via Stop) —
// lets ordered-shutdown / tests observe natural exit.
func TestWorker_EI10_DoneClosesOnNaturalExit(t *testing.T) {
    t.Parallel()
    w := Start(context.Background(), "test_natural_exit", func(ctx context.Context) {
        // return immediately, no ctx wait
    })
    select {
    case <-w.Done():
        // good
    case <-time.After(time.Second):
        t.Fatal("EI10: Done channel did not close after goroutine exited naturally")
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
