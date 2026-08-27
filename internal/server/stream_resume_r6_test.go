package server

import (
    "context"
    "fmt"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// delayedStreamProvider emits chunks with a configurable delay between each,
// recording the ctx it received and signaling started/closed. Used by R6 guards
// that need a slow-but-live upstream (keepalive) or a never-emitting upstream
// (watchdog). The pump honors ctx.Done so a liveCtx cancel (shutdown/watchdog)
// unblocks it and closes the channel.
type delayedStreamProvider struct {
    name    string
    chunks  []adapter.StreamChunk
    delay   time.Duration
    started chan struct{}
    closed  chan struct{}
    sawCtx  context.Context
}

func (p *delayedStreamProvider) Name() string { return p.name }
func (p *delayedStreamProvider) HealthCheck(_ context.Context) error { return nil }
func (p *delayedStreamProvider) Chat(_ context.Context, _ *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    return nil, fmt.Errorf("not implemented")
}
func (p *delayedStreamProvider) StreamChat(ctx context.Context, _ *adapter.ChatRequest) (<-chan adapter.StreamChunk, error) {
    p.sawCtx = ctx
    if p.started != nil {
        close(p.started)
    }
    ch := make(chan adapter.StreamChunk, len(p.chunks)+1)
    safego.Go("test_delayed_pump", func() {
        defer func() {
            close(ch)
            if p.closed != nil {
                close(p.closed)
            }
        }()
        for _, c := range p.chunks {
            if p.delay > 0 {
                select {
                case <-ctx.Done():
                    return
                case <-time.After(p.delay):
                }
            }
            select {
            case <-ctx.Done():
                return
            case ch <- c:
            }
        }
    })
    return ch, nil
}
func (p *delayedStreamProvider) Embedding(_ context.Context, _ *adapter.EmbeddingRequest) (*adapter.EmbeddingResponse, error) {
    return nil, fmt.Errorf("not implemented")
}
func (p *delayedStreamProvider) Rerank(_ context.Context, _ *adapter.RerankRequest) (*adapter.RerankResponse, error) {
    return nil, fmt.Errorf("not implemented")
}
func (p *delayedStreamProvider) ListModels(_ context.Context) ([]adapter.ModelInfo, error) {
    return nil, nil
}

// resumableStreamConfig sets a fast keepalive tick + short idle timeout so the
// guards exercise the tick branch within the test budget without waiting
// minutes. Returns a cleanup restoring prior values.
func resumableStreamConfig(s *Server, keepalive, idle time.Duration) func() {
    prevKA := s.cfg.Config.Routing.Stream.KeepaliveInterval
    prevIdle := s.cfg.Config.Routing.Stream.IdleTimeout
    s.cfg.Config.Routing.Stream.KeepaliveInterval = keepalive
    s.cfg.Config.Routing.Stream.IdleTimeout = idle
    return func() {
        s.cfg.Config.Routing.Stream.KeepaliveInterval = prevKA
        s.cfg.Config.Routing.Stream.IdleTimeout = prevIdle
    }
}

// TestR6_ResumableStream_NoDataRace: a resumable stream with a live keepalive
// tick firing WHILE chunks arrive must not race. Prior code ran the watchdog in
// its own goroutine reading lastChunkAt/chunkCount written by the consumer with
// no synchronization → `go test -race` reported a data race. The folded
// single-select loop reads and writes those vars on one goroutine.
// Run with -race: `go test -race -run TestR6_ResumableStream_NoDataRace ./internal/server/`.
func TestR6_ResumableStream_NoDataRace(t *testing.T) {
    s := newResumableTestServer(t)
    cleanup := resumableStreamConfig(s, 5*time.Millisecond, 30*time.Second)
    defer cleanup()
    sid := "req-r6-race"
    provider := &delayedStreamProvider{
        name:    "fusion-mlx",
        chunks:  chunksFor(40),
        delay:   3 * time.Millisecond,
        started: make(chan struct{}),
        closed:  make(chan struct{}),
    }
    s.pool.Register("fusion-mlx", provider, configBackend("fusion-mlx"))

    ctx := middleware.InjectRequestID(context.Background(), sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    done := make(chan struct{})
    safego.Go("test_r6_race_call", func() {
        s.handleStreamChat(ctx, rec, provider, req, decision, budget, time.Now())
        close(done)
    })
    <-provider.started
    select {
    case <-done:
    case <-time.After(15 * time.Second):
        t.Fatal("handleStreamChat did not return within 15s")
    }
    // If a data race existed, -race would have failed the test before here.
    if rec.Body.Len() == 0 {
        t.Fatal("empty body — stream did not deliver any frames")
    }
}

// TestR6_ResumableStream_KeepaliveEmitted: a slow-but-live upstream (long delay
// between chunks, longer than the keepalive tick) must emit `: keepalive` lines
// so an intermediate proxy does not time the client out. Prior code defined
// keepalivePing but only referenced it via `_ = keepalivePing` → dead code, zero
// keepalive. Guard: body contains `: keepalive`.
func TestR6_ResumableStream_KeepaliveEmitted(t *testing.T) {
    s := newResumableTestServer(t)
    cleanup := resumableStreamConfig(s, 10*time.Millisecond, 30*time.Second)
    defer cleanup()
    sid := "req-r6-keepalive"
    provider := &delayedStreamProvider{
        name:    "fusion-mlx",
        chunks:  chunksFor(2),
        delay:   200 * time.Millisecond,
        started: make(chan struct{}),
        closed:  make(chan struct{}),
    }
    s.pool.Register("fusion-mlx", provider, configBackend("fusion-mlx"))

    ctx := middleware.InjectRequestID(context.Background(), sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    done := make(chan struct{})
    safego.Go("test_r6_keepalive_call", func() {
        s.handleStreamChat(ctx, rec, provider, req, decision, budget, time.Now())
        close(done)
    })
    <-provider.started
    select {
    case <-done:
    case <-time.After(15 * time.Second):
        t.Fatal("handleStreamChat did not return within 15s")
    }
    body := rec.Body.String()
    if !strings.Contains(body, ": keepalive") {
        t.Fatalf("body missing keepalive comment line (dead keepalivePing regression). body:\n%s", body)
    }
}

// TestR6_ResumableStream_ShutdownCancelsPump: Server.Shutdown cancels the
// resumable pump's liveCtx root so an in-flight slow pump stops and releases its
// slot, instead of running until the 5-min idle watchdog or a read error. Prior
// code rooted liveCtx at context.Background() → Shutdown ignored, pump kept
// reading a torn-down fusion-mlx. Guard: after Shutdown, the pump's ctx is
// canceled and the channel closes promptly.
func TestR6_ResumableStream_ShutdownCancelsPump(t *testing.T) {
    s := newResumableTestServer(t)
    cleanup := resumableStreamConfig(s, 50*time.Millisecond, 30*time.Second)
    defer cleanup()
    sid := "req-r6-shutdown"
    provider := &delayedStreamProvider{
        name:    "fusion-mlx",
        chunks:  chunksFor(100),
        delay:   100 * time.Millisecond,
        started: make(chan struct{}),
        closed:  make(chan struct{}),
    }
    s.pool.Register("fusion-mlx", provider, configBackend("fusion-mlx"))

    ctx := middleware.InjectRequestID(context.Background(), sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    done := make(chan struct{})
    safego.Go("test_r6_shutdown_call", func() {
        s.handleStreamChat(ctx, rec, provider, req, decision, budget, time.Now())
        close(done)
    })
    <-provider.started

    // Shutdown mid-stream: cancels shutdownCtx → liveCtx child → pump ctx.
    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer shutdownCancel()
    s.Shutdown(shutdownCtx)

    // The pump ctx is a child of shutdownCtx; it must be canceled now.
    select {
    case <-provider.sawCtx.Done():
    case <-time.After(2 * time.Second):
        t.Fatal("pump ctx not canceled after Shutdown — liveCtx not rooted at shutdownCtx")
    }
    // The pump goroutine honors ctx.Done and closes the channel promptly.
    select {
    case <-provider.closed:
    case <-time.After(3 * time.Second):
        t.Fatal("pump channel did not close within 3s of Shutdown — slot not released")
    }
    select {
    case <-done:
    case <-time.After(5 * time.Second):
        t.Fatal("handleStreamChat did not return within 5s after Shutdown")
    }
}

// TestR6_ResumableStream_WatchdogStillTrips: the folded loop still trips the
// idle watchdog when the upstream stalls beyond IdleTimeout, ending with
// end_reason watchdog_tripped (synth terminal, not a hang). Guard: a pump that
// never emits a chunk trips within ~IdleTimeout and the handler returns.
func TestR6_ResumableStream_WatchdogStillTrips(t *testing.T) {
    s := newResumableTestServer(t)
    cleanup := resumableStreamConfig(s, 20*time.Millisecond, 100*time.Millisecond)
    defer cleanup()
    sid := "req-r6-watchdog"
    provider := &delayedStreamProvider{
        name:    "fusion-mlx",
        chunks:  chunksFor(3),
        delay:   0, // emitted only after ctx stays live; we cancel via watchdog
        started: make(chan struct{}),
        closed:  make(chan struct{}),
    }
    // Make the pump block on sending the first chunk by giving it a tiny buffer
    // and a recipient (the consumer) that we never let drain: instead, force a
    // stall by using a provider that waits on ctx before emitting — emulate via
    // a large delay so no chunk arrives within IdleTimeout.
    provider.delay = 2 * time.Second
    s.pool.Register("fusion-mlx", provider, configBackend("fusion-mlx"))

    ctx := middleware.InjectRequestID(context.Background(), sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    done := make(chan struct{})
    start := time.Now()
    safego.Go("test_r6_watchdog_call", func() {
        s.handleStreamChat(ctx, rec, provider, req, decision, budget, start)
        close(done)
    })
    <-provider.started
    select {
    case <-done:
    case <-time.After(5 * time.Second):
        t.Fatal("handler did not return within 5s — watchdog did not trip on stalled upstream")
    }
    // Watchdog trips well under the 2s chunk delay; if it never tripped the
    // handler would block ~2s+ on the first chunk. Assert it returned fast.
    if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
        t.Fatalf("handler took %v to return — watchdog did not trip within IdleTimeout", elapsed)
    }
}

// configBackend is a small helper for the BackendConfig the pool expects.
func configBackend(typ string) config.BackendConfig {
    return config.BackendConfig{Type: typ, Enabled: true}
}
