package server

// R1 guard tests: the 3 server-owned reaper loops (reapExpiredTasks,
// reapExpiredStreamBuffers, evictOAuth2States) must honor ctx.Done() so
// Server.Shutdown can Stop (cancel + join) them via lifecycle.Worker instead
// of leaking. Revert a ctx.Done() branch → the matching test hangs (timeout).

import (
    "context"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// r1MinimalServer builds a Server with the minimum fields the reaper loops
// touch (cfg, streamBuffers, oauth2States, taskRegistry). It does NOT call
// server.New, so no reaper workers are auto-launched — we drive the loops
// directly with a cancellable context.
func r1MinimalServer(t *testing.T) *Server {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Routing.Stream.ResumeEnabled = true
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    routerEngine := router.NewEngine(cfg, hwCollector)
    s := &Server{
        cfg:           cfg,
        router:        routerEngine,
        tokEngine:     tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        taskRegistry:  NewTaskRegistry(),
        streamBuffers: NewStreamBufferStore(100, 1<<20, 0, 10*time.Minute),
        oauth2States:  make(map[string]oauth2StateEntry),
    }
    return s
}

// TestR1_ReapExpiredTasks_ExitsOnCtxCancel drives reapExpiredTasks with a
// cancellable context and asserts it returns promptly after cancel. Without
// the select-on-ctx.Done() branch the loop blocks on the 5m ticker.
func TestR1_ReapExpiredTasks_ExitsOnCtxCancel(t *testing.T) {
    s := r1MinimalServer(t)
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        s.reapExpiredTasks(ctx)
        close(done)
    }()
    time.Sleep(50 * time.Millisecond)
    cancel()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("R1: reapExpiredTasks did not exit within 2s of ctx cancel — ctx.Done() branch missing")
    }
}

// TestR1_ReapExpiredStreamBuffers_ExitsOnCtxCancel: same guard for the
// stream-buffer reaper (10s floor interval).
func TestR1_ReapExpiredStreamBuffers_ExitsOnCtxCancel(t *testing.T) {
    s := r1MinimalServer(t)
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        s.reapExpiredStreamBuffers(ctx)
        close(done)
    }()
    time.Sleep(50 * time.Millisecond)
    cancel()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("R1: reapExpiredStreamBuffers did not exit within 2s of ctx cancel — ctx.Done() branch missing")
    }
}

// TestR1_EvictOAuth2States_ExitsOnCtxCancel: same guard for the oauth2
// state evictor (10m interval).
func TestR1_EvictOAuth2States_ExitsOnCtxCancel(t *testing.T) {
    s := r1MinimalServer(t)
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        s.evictOAuth2States(ctx)
        close(done)
    }()
    time.Sleep(50 * time.Millisecond)
    cancel()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("R1: evictOAuth2States did not exit within 2s of ctx cancel — ctx.Done() branch missing")
    }
}
