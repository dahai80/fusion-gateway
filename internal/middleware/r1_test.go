package middleware

// R1 guard tests: cleanupIdle must honor ctx.Done() so Server.Shutdown can
// Stop (cancel + join) it instead of leaking. Revert the ctx.Done() branch →
// TestR1_CleanupIdle_ExitsOnCtxCancel hangs.

import (
    "context"
    "testing"
    "time"
)

// TestR1_CleanupIdle_ExitsOnCtxCancel drives cleanupIdle with a cancellable
// context and asserts it returns promptly after cancel. Without the
// select-on-ctx.Done() branch the loop blocks on the 5m ticker and times out.
func TestR1_CleanupIdle_ExitsOnCtxCancel(t *testing.T) {
    rl := NewRateLimiter()
    if rl.cleanupWorker == nil {
        t.Fatal("R1: NewRateLimiter must store the cleanup lifecycle.Worker")
    }
    defer rl.Close()

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        rl.cleanupIdle(ctx)
        close(done)
    }()

    time.Sleep(50 * time.Millisecond)
    cancel()

    select {
    case <-done:
        // cleanupIdle exited after cancel — R1 honored.
    case <-time.After(2 * time.Second):
        t.Fatal("R1: cleanupIdle did not exit within 2s of ctx cancel — ctx.Done() branch missing (loop ignores shutdown)")
    }
}

// TestR1_Close_JoinsCleanupWorker asserts Close() stops the worker and clears
// the handle; idempotent.
func TestR1_Close_JoinsCleanupWorker(t *testing.T) {
    rl := NewRateLimiter()
    if rl.cleanupWorker == nil {
        t.Fatal("R1: NewRateLimiter must store the cleanup lifecycle.Worker")
    }
    rl.Close()
    if rl.cleanupWorker != nil {
        t.Fatal("R1: Close must clear cleanupWorker handle after Stop")
    }
    rl.Close() // idempotent, no panic
}
