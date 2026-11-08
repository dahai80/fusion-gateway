package server

import (
    "context"
    "fmt"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// #577: a caller-side timeout/cancel (client ctx dead) must not RecordFailure
// against the backend breaker — N caller timeouts on a healthy local backend
// opened the breaker and 503'd every seat behind it (pipeline round 36).
// Mirror of the stream-establish #363 guard, for the non-stream chat path.

func TestNonStreamChat_ClientCanceled_NoBreakerFailure(t *testing.T) {
    logs := capture363Logs(t)
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, chatErr: fmt.Errorf("context canceled")})
    p, _ := s.pool.Get("test-cloud")
    req := &adapter.ChatRequest{Model: "gpt-4"}
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 100}
    rec := httptest.NewRecorder()
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    s.handleNonStreamChat(ctx, rec, p, req, decision, budget, time.Now(), "")
    if !strings.Contains(logs.String(), "skipping breaker failure record") {
        t.Fatalf("expected client-cancel guard to skip RecordFailure (#577), logs:\n%s", logs.String())
    }
    if strings.Contains(rec.Body.String(), "chat failed") {
        t.Fatalf("client-gone non-stream must not write an error body, got: %s", rec.Body.String())
    }
}

func TestNonStreamChat_ClientAlive_StillRecordsFailure(t *testing.T) {
    logs := capture363Logs(t)
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, chatErr: fmt.Errorf("boom")})
    p, _ := s.pool.Get("test-cloud")
    req := &adapter.ChatRequest{Model: "gpt-4"}
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 100}
    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, p, req, decision, budget, time.Now(), "")
    if !strings.Contains(logs.String(), "chat failed") {
        t.Fatalf("real backend failure must keep the ERROR record, logs:\n%s", logs.String())
    }
    if !strings.Contains(rec.Body.String(), "boom") {
        t.Fatalf("real backend failure must surface the error, got: %s", rec.Body.String())
    }
}
