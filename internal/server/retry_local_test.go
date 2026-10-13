package server

import (
    "context"
    "errors"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// countingChatProvider wraps mockProvider and counts Chat invocations. The
// chat error mimics the #208 repro: a local fusion-mlx header timeout
// ("timeout awaiting response headers") on a long-generation request.
type countingChatProvider struct {
    mockProvider
    calls atomic.Int64
}

func (c *countingChatProvider) Chat(_ context.Context, _ *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    c.calls.Add(1)
    return nil, errors.New(`chat request failed: Post "http://127.0.0.1:11434/v1/chat/completions": net/http: timeout awaiting response headers`)
}

// newRetryTestServer builds a server with a fast retry config (5ms backoff)
// and the counting provider registered under "test-backend".
func newRetryTestServer(p *countingChatProvider) *Server {
    s := newTestServer()
    s.cfg.Config.Routing.Retry = config.RetryConfig{
        MaxRetries:        3,
        InitialBackoff:    5 * time.Millisecond,
        MaxBackoff:        10 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    s.pool.Register("test-backend", p, config.BackendConfig{
        Type:    "openai-compatible",
        BaseURL: "http://localhost:0",
        Enabled: true,
    })
    return s
}

func retryTestRequest() *adapter.ChatRequest {
    return &adapter.ChatRequest{
        Model: "test-model",
        Messages: []adapter.ChatMessage{
            {Role: "user", Content: "rewrite this 30KB novel as a script"},
        },
    }
}

// #208: a LOCAL backend must NOT retry on header timeout — retrying a local
// generation re-sends the entire prompt and re-triggers the full prefill,
// guaranteeing the same timeout while doubling engine load. One call, one
// failure, error surfaces immediately.
func TestNonStreamChat_LocalBackendNoRetryOnHeaderTimeout(t *testing.T) {
    p := &countingChatProvider{}
    p.name = "test-backend"
    s := newRetryTestServer(p)

    req := retryTestRequest()
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "mode_local"}
    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 1000}

    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, p, req, decision, budget, time.Now(), "test")

    if got := p.calls.Load(); got != 1 {
        t.Fatalf("local backend must call Chat exactly once (no retry), got %d calls", got)
    }
    if rec.Code == http.StatusOK {
        t.Fatalf("expected error status, got 200")
    }
}

// #208 counterpart: a CLOUD backend KEEPS the retry wrapper — cloud header
// timeouts are transient faults (headers precede the first token; a retry
// re-sends a cheap idempotent-ish API call, not a local prefill).
func TestNonStreamChat_CloudBackendStillRetries(t *testing.T) {
    p := &countingChatProvider{}
    p.name = "test-backend"
    s := newRetryTestServer(p)

    req := retryTestRequest()
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "fallback"}
    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 1000}

    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, p, req, decision, budget, time.Now(), "test")

    if got := p.calls.Load(); got < 2 {
        t.Fatalf("cloud backend should retry on header timeout (>=2 calls), got %d", got)
    }
    if !strings.Contains(rec.Body.String(), "timeout awaiting response headers") {
        t.Logf("cloud retry exhausted, status=%d", rec.Code)
    }
}
