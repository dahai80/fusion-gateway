package server

// R8 (audit) test: a stream whose upstream channel closes WITHOUT a terminal
// finish_reason must be recorded as a failure (recordOutcome(false)) so the
// circuit breaker sees the truncation — not masked as success, which left a
// repeatedly-dying backend in rotation.

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
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

// r8TestServer mirrors newTestServer but pins the cloud breaker
// FailureThreshold to 1 so a single recorded failure opens it — letting the
// test distinguish recordOutcome(false) (breaker opens) from
// recordOutcome(true) (breaker stays closed) deterministically.
func r8TestServer(name string, p *mockProvider) *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.Server.Port = 0
    cfg.Config.Server.MaxRequestBodySize = 5 << 20
    cfg.Config.Routing.Fallback.CloudDefault = "test-cloud"
    cfg.Config.Routing.CircuitBreaker.FailureThreshold = 1
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
    s.pool.Register(name, p, config.BackendConfig{
        Type:    "openai-compatible",
        BaseURL: "http://localhost:0",
        Enabled: true,
    })
    return s
}

// TestR8_IncompleteStreamRecordsFailure: provider closes the channel after a
// content chunk with NO finish_reason. R8 fix must record this as incomplete →
// recordOutcome(false) → cloud breaker opens (threshold 1).
func TestR8_IncompleteStreamRecordsFailure(t *testing.T) {
    ch := make(chan adapter.StreamChunk, 2)
    // One content chunk, no terminal finish_reason, then the channel closes.
    ch <- adapter.StreamChunk{
        ID:      "chatcmpl-inc",
        Object:  "chat.completion.chunk",
        Created: time.Now().Unix(),
        Model:   "gpt-4",
        Choices: []adapter.ChoiceDelta{
            {Index: 0, Delta: map[string]string{"content": "partial"}},
        },
    }
    close(ch)

    s := r8TestServer("test-cloud", &mockProvider{
        name:     "test-cloud",
        healthy:  true,
        streamCh: ch,
    })

    chatBody := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status: want 200 (stream started), got %d", rec.Code)
    }
    if state := s.router.CircuitBreakerState("cloud"); state != router.StateOpen {
        t.Errorf("R8: incomplete stream (no finish_reason) must open the cloud breaker, got state=%v", state)
    }
}

// TestR8_CleanStreamKeepsBreakerClosed: provider closes the channel WITH a
// terminal finish_reason. R8 fix records this as success → breaker stays
// closed. Control case proving the failure above is R8-specific, not a side
// effect of the test harness.
func TestR8_CleanStreamKeepsBreakerClosed(t *testing.T) {
    ch := make(chan adapter.StreamChunk, 2)
    finishReason := "stop"
    ch <- adapter.StreamChunk{
        ID:      "chatcmpl-clean",
        Object:  "chat.completion.chunk",
        Created: time.Now().Unix(),
        Model:   "gpt-4",
        Choices: []adapter.ChoiceDelta{
            {Index: 0, Delta: map[string]string{"content": "done"}, FinishReason: &finishReason},
        },
    }
    close(ch)

    s := r8TestServer("test-cloud", &mockProvider{
        name:     "test-cloud",
        healthy:  true,
        streamCh: ch,
    })

    chatBody := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status: want 200, got %d", rec.Code)
    }
    if state := s.router.CircuitBreakerState("cloud"); state != router.StateClosed {
        t.Errorf("R8 control: clean stream (finish_reason present) must keep breaker closed, got state=%v", state)
    }
}
