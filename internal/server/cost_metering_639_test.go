package server

// #639: non-stream /v1/chat/completions must meter usage in the cost tracker
// regardless of which serve path answers. The fetch tail's recordAndCharge is
// unreachable on the cache-serve early-return paths (exact hit, semantic hit,
// coalesced re-check) and the coalescer is always wired — so with the default
// cache config every non-stream request recorded zero usage while stream
// metered fine. Regression: a fresh non-stream request must land in
// SummaryByKey, and a cache-served repeat must meter too (serving is billable).

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

func Test639_NonStreamChat_MetersUsage(t *testing.T) {
    s := newTestServerR2()
    defer s.cache.Close()
    s.costTracker = cost.NewTracker(100)

    provider := &mockProvider{
        name:    "fusion-mlx",
        healthy: true,
        chatResp: &adapter.ChatResponse{
            ID: "cmpl-639", Object: "chat.completion", Created: time.Now().Unix(), Model: "qwen2.5-7b",
            Choices: []adapter.ChatChoice{{Index: 0, Message: map[string]string{"role": "assistant", "content": "ok"}, FinishReason: "stop"}},
            Usage:   adapter.UsageResponse{PromptTokens: 25, CompletionTokens: 8, TotalTokens: 33},
        },
    }
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})
    s.cfg.Config.Routing.Fallback.CloudDefault = "fusion-mlx"

    body := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"metering-639"}]}`
    post := func() *httptest.ResponseRecorder {
        rec := httptest.NewRecorder()
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
        ctx := config.WithSnapshot(req.Context(), s.cfg)
        ctx = middleware.ContextWithPrincipal(ctx, &middleware.Principal{
            AuthMethod: "api-key",
            KeyConfig:  &config.AuthKeyConfig{Name: "fk-639-probe"},
        })
        s.handleChatCompletions(rec, req.WithContext(ctx))
        return rec
    }

    if rec := post(); rec.Code != http.StatusOK {
        t.Fatalf("first request = %d body=%s", rec.Code, rec.Body.String())
    }
    kb := s.costTracker.SummaryByKey("fk-639-probe")
    if kb.TotalRequests != 1 || kb.TotalTokens != 33 {
        t.Fatalf("non-stream fresh fetch not metered: requests=%d tokens=%d (#639: cache-serve path skipped recordAndCharge)", kb.TotalRequests, kb.TotalTokens)
    }

    // Second identical request serves from the exact cache — still billable.
    if rec := post(); rec.Code != http.StatusOK {
        t.Fatalf("second request = %d", rec.Code)
    }
    kb = s.costTracker.SummaryByKey("fk-639-probe")
    if kb.TotalRequests != 2 {
        t.Fatalf("cache-served repeat not metered: requests=%d", kb.TotalRequests)
    }

    // Aggregates line up for the own-scope /v1/cost readback.
    sum := s.costTracker.SummaryByKey("fk-639-probe")
    if sum.TotalTokens != 66 {
        t.Fatalf("total tokens = %d, want 66", sum.TotalTokens)
    }
}

func Test639_MeterCachedServed_ParsesUsage(t *testing.T) {
    s := newTestServerR2()
    defer s.cache.Close()
    s.costTracker = cost.NewTracker(10)

    cached, _ := json.Marshal(map[string]any{
        "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
    })
    s.meterCachedServed(t.Context(), cached, "local", "m1")
    sum := s.costTracker.SummaryByKey("anonymous")
    if sum.TotalRequests != 1 || sum.TotalTokens != 15 {
        t.Fatalf("meterCachedServed usage = %d req / %d tokens", sum.TotalRequests, sum.TotalTokens)
    }
    // Unparseable body: no metering, no panic.
    s.meterCachedServed(t.Context(), []byte("{bad"), "local", "m1")
    if s.costTracker.SummaryByKey("anonymous").TotalRequests != 1 {
        t.Fatal("bad body must not meter")
    }
}
