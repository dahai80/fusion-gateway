package server

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

type syncBuf363 struct {
    mu sync.Mutex
    b  bytes.Buffer
}

func (s *syncBuf363) Write(p []byte) (int, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.b.Write(p)
}

func (s *syncBuf363) String() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.b.String()
}

func capture363Logs(t *testing.T) *syncBuf363 {
    t.Helper()
    buf := &syncBuf363{}
    old := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
    t.Cleanup(func() { slog.SetDefault(old) })
    return buf
}

func TestStreamChat_DONE_SentinelLabelsCleanEnd(t *testing.T) {
    logs := capture363Logs(t)
    s := newTestServer()
    ch := make(chan adapter.StreamChunk, 4)
    ch <- adapter.StreamChunk{
        ID: "c1", Object: "chat.completion.chunk", Created: 1, Model: "gpt-4",
        Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "hi"}}},
    }
    ch <- adapter.StreamChunk{Raw: json.RawMessage("[DONE]")}
    close(ch)
    s.pool.Register("test-cloud", &mockProvider{name: "test-cloud", healthy: true, streamCh: ch}, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://localhost:0", Enabled: true})
    p, _ := s.pool.Get("test-cloud")
    req := &adapter.ChatRequest{Model: "gpt-4", Stream: true}
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 100}
    rec := httptest.NewRecorder()
    s.handleStreamChat(context.Background(), rec, p, req, decision, budget, time.Now())
    if !strings.Contains(logs.String(), "end_reason=done") {
        t.Fatalf("expected end_reason=done after [DONE] sentinel (#363 F3), logs:\n%s", logs.String())
    }
}

func TestStreamChat_ClosedNoSentinelStillIncomplete(t *testing.T) {
    logs := capture363Logs(t)
    s := newTestServer()
    ch := make(chan adapter.StreamChunk, 4)
    ch <- adapter.StreamChunk{
        ID: "c1", Object: "chat.completion.chunk", Created: 1, Model: "gpt-4",
        Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "partial"}}},
    }
    close(ch)
    s.pool.Register("test-cloud", &mockProvider{name: "test-cloud", healthy: true, streamCh: ch}, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://localhost:0", Enabled: true})
    p, _ := s.pool.Get("test-cloud")
    req := &adapter.ChatRequest{Model: "gpt-4", Stream: true}
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 100}
    rec := httptest.NewRecorder()
    s.handleStreamChat(context.Background(), rec, p, req, decision, budget, time.Now())
    // The summary logs the raw ch_closed_no_done before the R8 relabel; the
    // WARN is the authoritative incomplete classification marker.
    if !strings.Contains(logs.String(), "stream chat closed without finish_reason, recording incomplete") {
        t.Fatalf("expected incomplete classification after close without sentinel, logs:\n%s", logs.String())
    }
}

func TestStreamChat_EstablishErrorClientGone(t *testing.T) {
    logs := capture363Logs(t)
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, streamErr: fmt.Errorf("boom")})
    p, _ := s.pool.Get("test-cloud")
    req := &adapter.ChatRequest{Model: "gpt-4", Stream: true}
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 100}
    rec := httptest.NewRecorder()
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    s.handleStreamChat(ctx, rec, p, req, decision, budget, time.Now())
    if !strings.Contains(logs.String(), "skipping failure record and overflow") {
        t.Fatalf("expected client-disconnect establish path (#363 F4), logs:\n%s", logs.String())
    }
    if strings.Contains(rec.Body.String(), "chat failed") {
        t.Fatalf("client-gone establish must not write an error body, got: %s", rec.Body.String())
    }
}

func TestResolveCloudProvider_UnmappedLocalModelWarns(t *testing.T) {
    logs := capture363Logs(t)
    s, ts := newTestServerWithMLX(t, false, []string{})
    defer ts.Close()
    s.pool.Register("test-cloud", &mockProvider{name: "test-cloud", healthy: true}, config.BackendConfig{Type: "openai-compatible", Enabled: true})
    req := &adapter.ChatRequest{Model: "qwen-7b"}
    rec := httptest.NewRecorder()
    p := s.resolveCloudProvider(&router.RouteDecision{Backend: router.CloudBackend}, req, rec)
    if p == nil {
        t.Fatal("expected cloud provider, got nil")
    }
    if req.Model != "qwen-7b" {
        t.Fatalf("unmapped model must stay unchanged, got %s", req.Model)
    }
    if !strings.Contains(logs.String(), "local model name routed to cloud unmapped") {
        t.Fatalf("expected unmapped local model warn (#363 F5), logs:\n%s", logs.String())
    }
}

func TestResolveCloudProvider_MappedLocalModelNoWarn(t *testing.T) {
    logs := capture363Logs(t)
    s, ts := newTestServerWithMLX(t, false, []string{})
    defer ts.Close()
    s.pool.Register("test-cloud", &mockProvider{name: "test-cloud", healthy: true}, config.BackendConfig{Type: "openai-compatible", Enabled: true})
    s.cfg.Config.Routing.Fallback.Enabled = true
    s.cfg.Config.Routing.Fallback.ModelMapping = map[string]string{"qwen-7b": "glm-5"}
    req := &adapter.ChatRequest{Model: "qwen-7b"}
    rec := httptest.NewRecorder()
    p := s.resolveCloudProvider(&router.RouteDecision{Backend: router.CloudBackend}, req, rec)
    if p == nil {
        t.Fatal("expected cloud provider, got nil")
    }
    if req.Model != "glm-5" {
        t.Fatalf("mapped model = %s, want glm-5", req.Model)
    }
    if strings.Contains(logs.String(), "local model name routed to cloud unmapped") {
        t.Fatalf("mapped model must not trigger unmapped warn, logs:\n%s", logs.String())
    }
}
