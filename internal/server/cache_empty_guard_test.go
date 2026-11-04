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

// #510 regression: an empty-content response (thinking-only model burned the
// whole max_tokens budget before any visible output) must pass through to the
// client but never land in the exact or semantic cache — otherwise repeats
// get an instant empty reply for the full TTL (live repro: glm5.2 with
// max_tokens=20, first call empty, second call X-Cache: HIT 0.0s).

func runEmptyGuardChat(s *Server, body string) *httptest.ResponseRecorder {
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    s.handleChatCompletions(rec, req)
    return rec
}

func TestEmptyContentResponse_NotCached(t *testing.T) {
    s := newTestServerR2()
    defer s.cache.Close()

    provider := &countingProvider{
        mockProvider: mockProvider{
            name:    "fusion-mlx",
            healthy: true,
            chatResp: &adapter.ChatResponse{
                ID: "cmpl-empty", Object: "chat.completion", Created: time.Now().Unix(), Model: "qwen2.5-7b",
                Choices: []adapter.ChatChoice{{Index: 0, Message: map[string]interface{}{"role": "assistant", "content": ""}, FinishReason: "max_tokens"}},
                Usage:   adapter.UsageResponse{PromptTokens: 5, CompletionTokens: 20, TotalTokens: 25},
            },
        },
    }
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})
    s.cfg.Config.Routing.Fallback.CloudDefault = "fusion-mlx"

    body := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"relativity please"}]}`
    rec1 := runEmptyGuardChat(s, body)
    if rec1.Code != http.StatusOK {
        t.Fatalf("first call must pass the empty response through, got %d body=%s", rec1.Code, rec1.Body.String())
    }
    rec2 := runEmptyGuardChat(s, body)
    if rec2.Code != http.StatusOK {
        t.Fatalf("second call must succeed, got %d", rec2.Code)
    }
    if rec2.Header().Get("X-Cache") == "HIT" {
        t.Fatalf("#510: empty-content response was served from the exact cache on repeat (X-Cache: HIT)")
    }
    // The R2 coalescer publishes through the cache; an unpublishable (empty)
    // response makes the leader fall through to its own fetch (documented
    // coalescer contract). What matters: the second request re-fetched —
    // calls > 2 proves it was not served from any cache.
    if provider.calls.Load() < 3 {
        t.Fatalf("#510: second request appears cache-served (%d calls) — empty response leaked into a cache", provider.calls.Load())
    }
    key := s.semanticCachePrompt(&adapter.ChatRequest{
        Model:    "qwen2.5-7b",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "relativity please"}},
    })
    if key.Payload != "" {
        if _, ok := s.semanticCache.Search(key.Payload, key.SystemHash, "qwen2.5-7b"); ok {
            t.Fatalf("#510: empty-content response was stored in the semantic cache")
        }
    }
}

func TestToolCallOnlyResponse_StillCached(t *testing.T) {
    s := newTestServerR2()
    defer s.cache.Close()

    provider := &countingProvider{
        mockProvider: mockProvider{
            name:    "fusion-mlx",
            healthy: true,
            chatResp: &adapter.ChatResponse{
                ID: "cmpl-tool", Object: "chat.completion", Created: time.Now().Unix(), Model: "qwen2.5-7b",
                Choices: []adapter.ChatChoice{{Index: 0, Message: map[string]interface{}{
                    "role":       "assistant",
                    "content":    "",
                    "tool_calls": []interface{}{map[string]interface{}{"id": "call-1", "type": "function"}},
                }, FinishReason: "tool_calls"}},
                Usage: adapter.UsageResponse{PromptTokens: 5, CompletionTokens: 8, TotalTokens: 13},
            },
        },
    }
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})
    s.cfg.Config.Routing.Fallback.CloudDefault = "fusion-mlx"

    body := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"list files"}]}`
    _ = runEmptyGuardChat(s, body)
    rec2 := runEmptyGuardChat(s, body)
    if rec2.Header().Get("X-Cache") != "HIT" {
        t.Fatalf("tool-call-only response (empty text, non-empty tool_calls) must stay cacheable, second call headers=%v", rec2.Header())
    }
}
