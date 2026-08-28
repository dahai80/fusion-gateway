package server

// R9 (per-request deadline) + R10 (global concurrent-stream cap) audit tests.

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
)

// r9r10TestServer builds a Server with tunable stream deadline + cap. Like
// r8TestServer but exposes MaxRequestDuration / MaxConcurrentStreams so the
// tests can pin them tight.
func r9r10TestServer(maxDur time.Duration, maxConcurrent int) *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.Server.Port = 0
    cfg.Config.Server.MaxRequestBodySize = 5 << 20
    cfg.Config.Routing.Fallback.CloudDefault = "test-cloud"
    cfg.Config.Routing.CircuitBreaker.FailureThreshold = 5
    cfg.Config.Routing.Stream.MaxRequestDuration = maxDur
    cfg.Config.Routing.Stream.MaxConcurrentStreams = maxConcurrent
    cfg.Config.OIDC.Enabled = false

    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    routerEngine := router.NewEngine(cfg, hwCollector)
    pool := adapter.NewPool()
    tokEngine := tokenizer.NewEngine(&cfg.Config.Tokenizer, "")
    oidcAuth, _ := middleware.NewOIDCAuthenticator(middleware.OIDCConfig{Enabled: false})

    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
    s := &Server{
        cfg:               cfg,
        hwCollector:       hwCollector,
        router:            routerEngine,
        pool:              pool,
        tokEngine:         tokEngine,
        startTime:         time.Now(),
        store:             memorystore.NewMemoryStoreWithConfig(1000, cfg.Config.Batch),
        cache:             cache.New(cfg.Config.Cache),
        semanticCache:     cache.NewSemanticCache(cfg.Config.SemanticCache, nil),
        connectorRegistry: newConnectorRegistry(cfg),
        oidcAuth:          oidcAuth,
        rateLimiter:       middleware.NewRateLimiter(),
        shutdownCtx:       shutdownCtx,
        shutdownCancel:    shutdownCancel,
    }
    s.buildMiddlewareChain()
    // Mirror server.New: size the global stream semaphore from config. The
    // &Server{} literal above does NOT run server.New's init block, so without
    // this acquireStreamSlot would see a nil channel (no-op cap) and R10
    // would be unenforced.
    if n := cfg.Config.Routing.Stream.MaxConcurrentStreams; n > 0 {
        s.streamSem = make(chan struct{}, n)
    }
    return s
}

// TestR9_DeadlineCancelsStalledStream: a provider that never sends a chunk and
// never closes the channel would block the handler indefinitely under a
// Timeout:0 stream client. R9's per-request deadline must cancel the ctx so the
// handler returns within ~maxDur, not hang.
func TestR9_DeadlineCancelsStalledStream(t *testing.T) {
    // Channel that never sends and never closes — simulates a wedged upstream.
    stallCh := make(chan adapter.StreamChunk)
    s := r9r10TestServer(150*time.Millisecond, 0)
    s.pool.Register("test-cloud", &mockProvider{
        name:     "test-cloud",
        healthy:  true,
        streamCh: stallCh,
    }, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://localhost:0", Enabled: true})

    chatBody := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    rec := httptest.NewRecorder()

    done := make(chan struct{})
    go func() {
        s.handleChatCompletions(rec, req)
        close(done)
    }()

    select {
    case <-done:
        // Handler returned — deadline fired and unblocked the forward loop.
    case <-time.After(3 * time.Second):
        t.Fatalf("R9: stalled stream did not return within 3s; deadline not applied")
    }
}

// TestR10_ConcurrentStreamCapRejectsExcess: with a cap of 4, 4 concurrent
// streams hold all slots (provider channels block, keeping slots held); further
// requests must get 429. Asserts the cap is enforced and the count of 429s
// matches the excess.
func TestR10_ConcurrentStreamCapRejectsExcess(t *testing.T) {
    const cap = 4
    const total = 12
    // Per-request provider channel: one content chunk then BLOCK (never close)
    // so the slot stays held for the life of each handler call.
    stallCh := make(chan adapter.StreamChunk, 1)
    stallCh <- adapter.StreamChunk{
        ID: "blocked", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: "gpt-4",
        Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "x"}}},
    }
    // R9 deadline short so the 4 slot-holders release fast (they block on the
    // 2nd read since the channel never closes); we count BEFORE the deadline
    // by having the rejected 429s return instantly while the 4 holders wait.
    s := r9r10TestServer(500*time.Millisecond, cap)
    s.pool.Register("test-cloud", &mockProvider{
        name: "test-cloud", healthy: true, streamCh: stallCh,
    }, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://localhost:0", Enabled: true})

    var accepted, rejected int32
    var wg sync.WaitGroup
    start := make(chan struct{})
    for i := 0; i < total; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            <-start
            chatBody := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`
            req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
            rec := httptest.NewRecorder()
            s.handleChatCompletions(rec, req)
            if rec.Code == http.StatusTooManyRequests {
                atomic.AddInt32(&rejected, 1)
            } else if rec.Code == http.StatusOK {
                atomic.AddInt32(&accepted, 1)
            }
        }()
    }
    close(start)
    wg.Wait()

    if int(accepted) != cap {
        t.Errorf("R10: accepted streams = %d, want %d (the cap)", accepted, cap)
    }
    if int(rejected) != total-cap {
        t.Errorf("R10: rejected streams = %d, want %d (excess over cap)", rejected, total-cap)
    }
}
