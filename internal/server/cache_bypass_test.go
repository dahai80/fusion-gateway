package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #734 regression: fusion-bench sends X-Cache: bypass on every request
// (bench hygiene #47 — a repeated-prompt bench must measure the model, not
// the cache). The exact-key cache must neither serve nor store for such
// requests, and the response must echo X-Cache: BYPASS so the caller can
// verify the contract. The semantic cache opts out too (no-store semantics).

const bypassChatBody = `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"bench prompt"}]}`

func runBypassChat(s *Server, body string, bypass bool) *httptest.ResponseRecorder {
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    if bypass {
        req.Header.Set("X-Cache", "bypass")
    }
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    s.handleChatCompletions(rec, req)
    return rec
}

func bypassTestProvider() (*countingProvider, *Server) {
    s := newTestServerR2()
    provider := &countingProvider{
        mockProvider: mockProvider{
            name:    "fusion-mlx",
            healthy: true,
            chatResp: &adapter.ChatResponse{
                ID: "cmpl-bypass", Object: "chat.completion", Created: time.Now().Unix(), Model: "qwen2.5-7b",
                Choices: []adapter.ChatChoice{{Index: 0, Message: map[string]interface{}{"role": "assistant", "content": "fresh answer"}, FinishReason: "stop"}},
                Usage:   adapter.UsageResponse{PromptTokens: 8, CompletionTokens: 12, TotalTokens: 20},
            },
        },
    }
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})
    s.cfg.Config.Routing.Fallback.CloudDefault = "fusion-mlx"
    return provider, s
}

func TestCacheBypass_NeverServesNeverStores(t *testing.T) {
    provider, s := bypassTestProvider()
    defer s.cache.Close()

    rec1 := runBypassChat(s, bypassChatBody, true)
    if rec1.Code != http.StatusOK {
        t.Fatalf("first bypass call must succeed, got %d body=%s", rec1.Code, rec1.Body.String())
    }
    if got := rec1.Header().Get("X-Cache"); got != "BYPASS" {
        t.Fatalf("#734: first bypass call must echo X-Cache: BYPASS, got %q", got)
    }
    rec2 := runBypassChat(s, bypassChatBody, true)
    if got := rec2.Header().Get("X-Cache"); got == "HIT" {
        t.Fatalf("#734: bypassed request was served from the exact cache")
    }
    if got := rec2.Header().Get("X-Cache"); got != "BYPASS" {
        t.Fatalf("#734: repeat bypass call must echo X-Cache: BYPASS, got %q", got)
    }
    if n := provider.calls.Load(); n != 2 {
        t.Fatalf("#734: model must be called once per bypass request, got %d calls", n)
    }
}

func TestCacheBypass_ControlStillCaches(t *testing.T) {
    provider, s := bypassTestProvider()
    defer s.cache.Close()

    rec1 := runBypassChat(s, bypassChatBody, false)
    if rec1.Code != http.StatusOK {
        t.Fatalf("first call must succeed, got %d", rec1.Code)
    }
    rec2 := runBypassChat(s, bypassChatBody, false)
    if got := rec2.Header().Get("X-Cache"); got != "HIT" {
        t.Fatalf("control: identical repeat must hit the exact cache, got X-Cache=%q after %d provider calls", got, provider.calls.Load())
    }
}
