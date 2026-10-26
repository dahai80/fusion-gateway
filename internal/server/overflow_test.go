// #301 cloud-overflow tests. Acceptance criteria 1+2 from the issue:
//   1. local backend always-fails + cloud pool stub → request succeeds via
//      cloud, routed=cloud metadata, audit event with reason.
//   2. cost ceiling: overflow stops when the ceiling is hit, explicit error
//      surfaced (not a silent fallback to the broken local backend).
package server

import (
    "context"
    "errors"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/persist"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// eventRecorderSink is a persist.Sink stub that also satisfies the insights
// EventRecorder (like *pg.Sink does in production) and captures RecordEvent
// calls so tests can assert the audit row landed.
type eventRecorderSink struct {
    mu     sync.Mutex
    events []recordedEvent
}

type recordedEvent struct {
    level string
    event string
    detail map[string]any
}

func (e *eventRecorderSink) Enqueue(_ *persist.Entry) error { return nil }
func (e *eventRecorderSink) Close(_ context.Context) error  { return nil }

func (e *eventRecorderSink) RecordEvent(level, event string, detail map[string]any) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.events = append(e.events, recordedEvent{level: level, event: event, detail: detail})
}

func (e *eventRecorderSink) recorded() []recordedEvent {
    e.mu.Lock()
    defer e.mu.Unlock()
    return append([]recordedEvent{}, e.events...)
}

// countingCloudProvider wraps mockProvider and counts Chat invocations so
// tests can assert the cloud stub was (or was NOT) reached.
type countingCloudProvider struct {
    mockProvider
    calls atomic.Int64
}

func (c *countingCloudProvider) Chat(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    c.calls.Add(1)
    return c.mockProvider.Chat(ctx, req)
}

// newOverflowTestServer: local provider that always fails + cloud stub that
// always succeeds, registered the way resolveCloudProvider expects (cloud
// name wired as Fallback.CloudDefault).
func newOverflowTestServer(t *testing.T) (*Server, *mockProvider, *countingCloudProvider, *eventRecorderSink) {
    t.Helper()
    s := newTestServer()
    local := &mockProvider{
        name:    "fusion-mlx",
        healthy: true,
        chatErr: errors.New(`chat request failed: Post "http://127.0.0.1:11434/v1/chat/completions": net/http: timeout awaiting response headers`),
    }
    cloud := &countingCloudProvider{}
    cloud.name = "test-cloud"
    cloud.healthy = true
    cloud.chatResp = &adapter.ChatResponse{
        ID:      "chatcmpl-overflow",
        Object:  "chat.completion",
        Model:   "test-model",
        Choices: []adapter.ChatChoice{{Index: 0, Message: adapter.ChatMessage{Role: "assistant", Content: "from cloud"}, FinishReason: "stop"}},
        Usage:   adapter.UsageResponse{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
    }
    s.pool.Register("fusion-mlx", local, config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://localhost:0", Enabled: true})
    s.pool.Register("test-cloud", cloud, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://localhost:0", Enabled: true})
    sink := &eventRecorderSink{}
    s.persistSink = sink
    return s, local, cloud, sink
}

func overflowTestRequest() *adapter.ChatRequest {
    return &adapter.ChatRequest{
        Model: "test-model",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "hello"}},
    }
}

// AC1: always-failing local + healthy cloud stub → success via cloud, the
// routed=cloud markers present, and a cloud_overflow audit event carrying the
// trigger reason, source backend, target backend, and cost delta.
func TestOverflow_LocalFailureFallsBackToCloud(t *testing.T) {
    s, _, cloud, sink := newOverflowTestServer(t)
    s.cfg.Config.Routing.Fallback.Overflow.Cooldown = time.Minute

    req := overflowTestRequest()
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "mode_local"}
    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 1000}

    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, s.mustProvider(t, "fusion-mlx"), req, decision, budget, time.Now(), "test")

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 via cloud fallback, got %d: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("X-Route-Decision"); got != "cloud:fallback_from_local" {
        t.Fatalf("X-Route-Decision = %q, want cloud:fallback_from_local", got)
    }
    if got := rec.Header().Get("X-Fusion-Routed"); got != "cloud" {
        t.Fatalf("X-Fusion-Routed = %q, want cloud", got)
    }
    if cloud.calls.Load() != 1 {
        t.Fatalf("cloud provider must be called exactly once, got %d", cloud.calls.Load())
    }

    events := sink.recorded()
    if len(events) != 1 {
        t.Fatalf("expected 1 cloud_overflow event, got %d", len(events))
    }
    ev := events[0]
    if ev.event != "cloud_overflow" || ev.level != "warn" {
        t.Fatalf("event = %s/%s, want cloud_overflow/warn", ev.level, ev.event)
    }
    if ev.detail["reason"] != "local_failure" {
        t.Fatalf("reason = %v, want local_failure", ev.detail["reason"])
    }
    if ev.detail["source_backend"] != "local" || ev.detail["target_backend"] != "test-cloud" {
        t.Fatalf("source/target = %v/%v, want local/test-cloud", ev.detail["source_backend"], ev.detail["target_backend"])
    }
    if _, ok := ev.detail["cost_delta"].(float64); !ok {
        t.Fatalf("cost_delta missing or not numeric: %v", ev.detail["cost_delta"])
    }

    // The overflow armed the cooldown state for subsequent requests.
    if !s.overflow.cooldownActive(time.Now()) {
        t.Fatalf("cooldown must be armed after an overflow")
    }
}

// AC2: once the daily overflow cost reached the ceiling, the next overflow is
// refused with an explicit error — the cloud stub must NOT be called and the
// client must NOT receive a silent local failure without explanation.
func TestOverflow_CostCeilingStopsOverflow(t *testing.T) {
    s, _, cloud, sink := newOverflowTestServer(t)
    s.cfg.Config.Routing.Fallback.Overflow.CostCeiling = 1.0
    // Pre-charge today's counter to the ceiling.
    now := time.Now()
    s.overflow.chargeOverflow(now, 1.0)

    req := overflowTestRequest()
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "mode_local"}
    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 1000}

    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, s.mustProvider(t, "fusion-mlx"), req, decision, budget, time.Now(), "test")

    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("expected 503 when ceiling reached, got %d: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "cost ceiling reached") {
        t.Fatalf("error must name the ceiling, got: %s", rec.Body.String())
    }
    if cloud.calls.Load() != 0 {
        t.Fatalf("cloud provider must NOT be called once ceiling reached, got %d calls", cloud.calls.Load())
    }
    events := sink.recorded()
    if len(events) != 1 || events[0].detail["reason"] != "ceiling_reached" {
        t.Fatalf("expected a ceiling_reached audit event, got %+v", events)
    }
}

// Fail-closed: overflow.enabled=false surfaces the local error; no cloud call.
func TestOverflow_DisabledFailClosed(t *testing.T) {
    s, _, cloud, sink := newOverflowTestServer(t)
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = false

    req := overflowTestRequest()
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "mode_local"}
    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 1000}

    rec := httptest.NewRecorder()
    s.handleNonStreamChat(context.Background(), rec, s.mustProvider(t, "fusion-mlx"), req, decision, budget, time.Now(), "test")

    if rec.Code == http.StatusOK {
        t.Fatalf("fail-closed must not return 200")
    }
    if cloud.calls.Load() != 0 {
        t.Fatalf("cloud provider must NOT be called when overflow disabled, got %d calls", cloud.calls.Load())
    }
    if len(sink.recorded()) != 0 {
        t.Fatalf("no audit event expected when overflow disabled")
    }
}

// Cooldown state machine: arm → active → expires; day rollover resets the
// cost counter so the ceiling is daily, not lifetime.
func TestOverflow_CooldownAndDailyReset(t *testing.T) {
    var o overflowState
    now := time.Now()
    if o.cooldownActive(now) {
        t.Fatalf("cooldown must start inactive")
    }
    o.armCooldown(now, 30*time.Second)
    if !o.cooldownActive(now.Add(time.Second)) {
        t.Fatalf("cooldown must be active inside window")
    }
    if o.cooldownActive(now.Add(31 * time.Second)) {
        t.Fatalf("cooldown must expire after window")
    }

    if o.ceilingBlocked(now, 1.0) {
        t.Fatalf("no cost charged yet — must not block")
    }
    o.chargeOverflow(now, 0.6)
    if o.ceilingBlocked(now, 1.0) {
        t.Fatalf("0.6 < 1.0 ceiling — must not block")
    }
    o.chargeOverflow(now, 0.5)
    if !o.ceilingBlocked(now, 1.0) {
        t.Fatalf("1.1 >= 1.0 ceiling — must block")
    }
    // Next UTC day: counter rolled, ceiling no longer blocks.
    tomorrow := now.Add(24 * time.Hour)
    if o.ceilingBlocked(tomorrow, 1.0) {
        t.Fatalf("day rollover must reset the cost counter")
    }
}

func (s *Server) mustProvider(t *testing.T, name string) adapter.Provider {
    t.Helper()
    p, ok := s.pool.Get(name)
    if !ok {
        t.Fatalf("provider %s not registered", name)
    }
    return p
}
