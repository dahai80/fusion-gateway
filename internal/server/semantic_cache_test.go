// #400 semantic cache wiring tests:
//   - buildSemanticEmbedFn derives an EmbedFunc from the adapter pool and
//     refuses (nil) on incomplete config instead of building a dead cache.
//   - the non-stream chat path Stores successful responses and serves a
//     semantic hit without calling the provider.
//   - tool-calling requests skip the semantic cache entirely.
package server

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// testEmbedFn maps each distinct prompt to a distinct axis so tests control
// cosine similarity: prompts are grouped by prefix token before the first
// space ("hello..." → e0, anything else → e1). A constant vector would make
// every prompt similarity 1.0 and defeat the miss test.
func testEmbedFn(prompt string) ([]float64, error) {
    if len(prompt) > 0 && prompt[0] == 'h' {
        return []float64{1, 0, 0}, nil
    }
    return []float64{0, 1, 0}, nil
}

func newSemanticTestServer(t *testing.T, embed cache.EmbedFunc) (*Server, *mockProvider) {
    t.Helper()
    s := newTestServer()
    s.cache = nil // isolate the semantic layer from the exact-key cache
    s.semanticCache = cache.NewSemanticCache(config.SemanticCacheConfig{
        Enabled:             true,
        SimilarityThreshold: 0.95,
        MaxEntries:          100,
    }, embed)
    if s.semanticCache == nil {
        t.Fatal("semantic cache must be enabled in this test")
    }
    t.Cleanup(s.semanticCache.Close)
    provider := &mockProvider{
        name:    "fusion-mlx",
        healthy: true,
        chatResp: &adapter.ChatResponse{
            ID:      "chatcmpl-semantic",
            Object:  "chat.completion",
            Model:   "test-model",
            Choices: []adapter.ChatChoice{{Index: 0, Message: adapter.ChatMessage{Role: "assistant", Content: "from engine"}, FinishReason: "stop"}},
            Usage:   adapter.UsageResponse{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
        },
    }
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://localhost:0", Enabled: true})
    return s, provider
}

func semanticChatRequest() *adapter.ChatRequest {
    return &adapter.ChatRequest{
        Model:    "test-model",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "hello semantic world"}},
    }
}

func runNonStreamChat(t *testing.T, s *Server, provider adapter.Provider, req *adapter.ChatRequest) *httptest.ResponseRecorder {
    t.Helper()
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "mode_local"}
    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 1000}
    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, provider, req, decision, budget, time.Now(), "test")
    return rec
}

func TestBuildSemanticEmbedFn(t *testing.T) {
    embResp := &adapter.EmbeddingResponse{Data: []adapter.EmbeddingData{{Embedding: []float64{0.1, 0.2, 0.3}}}}
    makePool := func() (*adapter.Pool, *mockProvider) {
        pool := adapter.NewPool()
        p := &mockProvider{name: "fusion-mlx", healthy: true, embResp: embResp}
        pool.Register("fusion-mlx", p, config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://localhost:0", Enabled: true})
        return pool, p
    }

    if fn := buildSemanticEmbedFn(config.SemanticCacheConfig{}, nil); fn != nil {
        t.Fatal("disabled config must return nil EmbedFunc")
    }
    pool, _ := makePool()
    if fn := buildSemanticEmbedFn(config.SemanticCacheConfig{Enabled: true}, pool); fn != nil {
        t.Fatal("enabled config without model must return nil EmbedFunc")
    }
    if fn := buildSemanticEmbedFn(config.SemanticCacheConfig{Enabled: true, Model: "bge-m3"}, nil); fn != nil {
        t.Fatal("nil pool must return nil EmbedFunc")
    }
    emptyPool := adapter.NewPool()
    if fn := buildSemanticEmbedFn(config.SemanticCacheConfig{Enabled: true, Model: "bge-m3"}, emptyPool); fn != nil {
        t.Fatal("provider missing from pool must return nil EmbedFunc")
    }

    pool, p := makePool()
    fn := buildSemanticEmbedFn(config.SemanticCacheConfig{Enabled: true, Model: "bge-m3"}, pool)
    if fn == nil {
        t.Fatal("wired config must return an EmbedFunc")
    }
    vec, err := fn("hello")
    if err != nil {
        t.Fatalf("embed failed: %v", err)
    }
    if len(vec) != 3 {
        t.Fatalf("expected 3-dim vector, got %d", len(vec))
    }

    p.embErr = errors.New("upstream down")
    if _, err := fn("hello"); err == nil {
        t.Fatal("provider embedding error must propagate")
    }
    p.embErr = nil
    p.embResp = &adapter.EmbeddingResponse{}
    if _, err := fn("hello"); err == nil {
        t.Fatal("empty embedding response must be an error")
    }
}

func TestNonStreamChatSemanticStoreThenHit(t *testing.T) {
    s, provider := newSemanticTestServer(t, testEmbedFn)
    req := semanticChatRequest()

    rec := runNonStreamChat(t, s, provider, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("first call: expected 200, got %d: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("X-Semantic-Cache"); got != "" {
        t.Fatalf("first call must not be a semantic hit, got %q", got)
    }
    _, misses, size := s.semanticCache.Stats()
    if size != 1 {
        t.Fatalf("response must be stored in the semantic cache, size=%d misses=%d", size, misses)
    }

    // Second identical call: semantic hit, provider not consulted.
    rec = runNonStreamChat(t, s, provider, semanticChatRequest())
    if rec.Code != http.StatusOK {
        t.Fatalf("second call: expected 200, got %d", rec.Code)
    }
    if got := rec.Header().Get("X-Semantic-Cache"); got != "HIT" {
        t.Fatalf("second call: expected X-Semantic-Cache HIT, got %q", got)
    }
    var resp adapter.ChatResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatalf("hit body is not a ChatResponse: %v", err)
    }
    msg, _ := resp.Choices[0].Message.(map[string]any)
    if len(resp.Choices) != 1 || fmt.Sprint(msg["content"]) != "from engine" {
        t.Fatalf("hit body must be the stored engine response, got %s", rec.Body.String())
    }
    hits, _, _ := s.semanticCache.Stats()
    if hits != 1 {
        t.Fatalf("expected 1 semantic hit, got %d", hits)
    }
}

func TestNonStreamChatSemanticMissServesEngine(t *testing.T) {
    // Orthogonal prompt (cosine 0 vs the stored (1,0,0)) must fall through to
    // the engine, not replay the stored entry.
    s, provider := newSemanticTestServer(t, testEmbedFn)
    s.semanticCache.Store("different prompt", "test-model", mustMarshal(t, provider.chatResp))
    rec := runNonStreamChat(t, s, provider, semanticChatRequest())
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("X-Semantic-Cache"); got != "" {
        t.Fatalf("dissimilar prompt must not hit, got %q", got)
    }
}

func TestNonStreamChatSemanticSkipsTools(t *testing.T) {
    s, provider := newSemanticTestServer(t, testEmbedFn)
    req := semanticChatRequest()
    req.Tools = []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}}
    rec := runNonStreamChat(t, s, provider, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
    }
    _, _, size := s.semanticCache.Stats()
    if size != 0 {
        t.Fatalf("tool-calling request must not be stored, size=%d", size)
    }
}

func TestSemanticCachePromptGates(t *testing.T) {
    s, _ := newSemanticTestServer(t, testEmbedFn)
    if got := s.semanticCachePrompt(nil); got != "" {
        t.Fatalf("nil request must skip, got %q", got)
    }
    if got := s.semanticCachePrompt(semanticChatRequest()); got == "" {
        t.Fatal("plain text request must produce a prompt")
    }
    toolReq := semanticChatRequest()
    toolReq.Tools = []any{map[string]any{"type": "function"}}
    if got := s.semanticCachePrompt(toolReq); got != "" {
        t.Fatalf("tool request must skip, got %q", got)
    }
    imgReq := semanticChatRequest()
    imgReq.Messages[0].Content = []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}}}
    if got := s.semanticCachePrompt(imgReq); got != "" {
        t.Fatalf("image request must skip, got %q", got)
    }
    emptyReq := &adapter.ChatRequest{Model: "test-model"}
    if got := s.semanticCachePrompt(emptyReq); got != "" {
        t.Fatalf("empty text must skip, got %q", got)
    }
    s.semanticCache = nil
    if got := s.semanticCachePrompt(semanticChatRequest()); got != "" {
        t.Fatal("disabled cache must skip")
    }
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
    t.Helper()
    data, err := json.Marshal(v)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    return data
}
