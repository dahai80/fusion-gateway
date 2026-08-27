package server

import (
    "context"
    "encoding/json"
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

// countingProvider wraps mockProvider to count Chat calls. R2 guard measures
// how many upstream fetches N concurrent same-key non-stream requests make.
type countingProvider struct {
    mockProvider
    calls atomic.Int64
    delay time.Duration
}

func (m *countingProvider) Chat(ctx context.Context, r *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    m.calls.Add(1)
    if m.delay > 0 {
        select {
        case <-time.After(m.delay):
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    if m.chatErr != nil {
        return nil, m.chatErr
    }
    return m.chatResp, nil
}

// newTestServerR2 builds a Server with cache ENABLED + fetchCoalescer wired,
// so the R2 non-stream stampede path is exercised end-to-end.
func newTestServerR2() *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.Server.Port = 0
    cfg.Config.Server.MaxRequestBodySize = 5 << 20
    cfg.Config.Routing.Fallback.CloudDefault = "test-cloud"
    cfg.Config.OIDC.Enabled = false
    cfg.Config.Cache.Enabled = true
    cfg.Config.Cache.MaxEntries = 1000
    cfg.Config.Cache.TTL = 5 * time.Minute

    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    routerEngine := router.NewEngine(cfg, hwCollector)
    pool := adapter.NewPool()
    tokEngine := tokenizer.NewEngine(&cfg.Config.Tokenizer, "")
    oidcAuth, _ := middleware.NewOIDCAuthenticator(middleware.OIDCConfig{Enabled: false})
    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

    s := &Server{
        cfg:              cfg,
        hwCollector:      hwCollector,
        router:           routerEngine,
        pool:             pool,
        tokEngine:        tokEngine,
        startTime:        time.Now(),
        store:            memorystore.NewMemoryStoreWithConfig(1000, cfg.Config.Batch),
        cache:            cache.New(cfg.Config.Cache),
        semanticCache:    cache.NewSemanticCache(cfg.Config.SemanticCache, nil),
        connectorRegistry: newConnectorRegistry(cfg),
        oidcAuth:         oidcAuth,
        rateLimiter:      middleware.NewRateLimiter(),
        fetchCoalescer:   newCoalescer(),
        shutdownCtx:      shutdownCtx,
        shutdownCancel:   shutdownCancel,
    }
    s.buildMiddlewareChain()
    return s
}

// TestR2_NonStreamChat_NoStampede: N concurrent identical (same model+messages)
// non-stream chat requests must hit the upstream provider exactly ONCE — the
// coalescer deduplicates the cold-key fetch. Before R2, all N missed the
// cache and each fired an independent upstream call (stampede). Revert (drop
// the coalescer block in handleNonStreamChat, or nil fetchCoalescer): calls
// == N → FAIL.
func TestR2_NonStreamChat_NoStampede(t *testing.T) {
    s := newTestServerR2()
    defer s.cache.Close()

    provider := &countingProvider{
        mockProvider: mockProvider{
            name:    "fusion-mlx",
            healthy: true,
            chatResp: &adapter.ChatResponse{
                ID: "cmpl-r2", Object: "chat.completion", Created: time.Now().Unix(), Model: "qwen2.5-7b",
                Choices: []adapter.ChatChoice{{Index: 0, Message: map[string]string{"role": "assistant", "content": "ok"}, FinishReason: "stop"}},
                Usage:   adapter.UsageResponse{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
            },
        },
        delay: 20 * time.Millisecond, // hold the leader slot so waiters pile up
    }
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})
    s.cfg.Config.Routing.Fallback.CloudDefault = "fusion-mlx"

    body := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"stampede-probe"}]}`

    const callers = 16
    var wg sync.WaitGroup
    start := make(chan struct{})
    recs := make([]*httptest.ResponseRecorder, callers)
    for i := 0; i < callers; i++ {
        wg.Add(1)
        recs[i] = httptest.NewRecorder()
        go func(rec *httptest.ResponseRecorder) {
            defer wg.Done()
            <-start
            req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
            req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
            s.handleChatCompletions(rec, req)
        }(recs[i])
    }
    close(start)
    wg.Wait()

    got := provider.calls.Load()
    if got != 1 {
        t.Errorf("R2: expected 1 upstream fetch for %d concurrent same-key non-stream requests, got %d (cold-key stampede — coalescer not deduping at the handler)", callers, got)
    }
    // All callers must succeed (leader serves via cache, or each gets a response).
    for i, rec := range recs {
        if rec.Code != http.StatusOK {
            t.Errorf("R2: caller %d got status %d, want 200 (body=%s)", i, rec.Code, rec.Body.String())
        }
    }
    // At least the leader's response should be cache-serializable JSON.
    var first map[string]interface{}
    if err := json.Unmarshal(recs[0].Body.Bytes(), &first); err != nil {
        t.Errorf("R2: response body not valid JSON: %v (body=%s)", err, recs[0].Body.String())
    }
}
