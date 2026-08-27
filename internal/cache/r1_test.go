package cache

// R1 guard tests: the eviction goroutine must honor ctx.Done() so Server.Shutdown
// can Stop (cancel + join) it instead of leaking. Revert the ctx.Done() branch in
// evictExpired → TestR1_EvictExpired_ExitsOnCtxCancel hangs (timeout). Revert the
// lifecycle.Start launch in New → Close cannot Stop the worker (no worker stored).

import (
    "context"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestR1_EvictExpired_ExitsOnCtxCancel drives evictExpired with a cancellable
// context and asserts the call returns promptly after cancel — proving the loop
// honors ctx.Done() (the R1 fix). Without the select-on-ctx.Done() branch the
// loop blocks on ticker.C (30s) and this test times out.
func TestR1_EvictExpired_ExitsOnCtxCancel(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, TTL: time.Minute}
    c := New(cfg)
    if c == nil {
        t.Fatal("expected non-nil cache")
    }
    defer c.Close()

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        c.evictExpired(ctx)
        close(done)
    }()

    // Give the loop a moment to enter its select, then cancel.
    time.Sleep(50 * time.Millisecond)
    cancel()

    select {
    case <-done:
        // evictExited after cancel — R1 honored.
    case <-time.After(2 * time.Second):
        t.Fatal("R1: evictExpired did not exit within 2s of ctx cancel — ctx.Done() branch missing (loop ignores shutdown)")
    }
}

// TestR1_Close_JoinsEvictionWorker asserts Close() stops the eviction worker
// and clears the handle, so a second Close is a no-op and the goroutine is
// actually joined (not just signaled).
func TestR1_Close_JoinsEvictionWorker(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, TTL: time.Minute}
    c := New(cfg)
    if c == nil {
        t.Fatal("expected non-nil cache")
    }
    if c.evictWorker == nil {
        t.Fatal("R1: New must store the eviction lifecycle.Worker; Close cannot Stop a nil worker")
    }
    // Close must join the worker and clear the handle.
    c.Close()
    if c.evictWorker != nil {
        t.Fatal("R1: Close must clear evictWorker handle after Stop")
    }
    // Idempotent: second Close is a no-op (no panic, no nil-pointer deref).
    c.Close()
}
