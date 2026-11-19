package ops

import (
    "os"
    "strings"
    "testing"
    "time"
)

func Test689_RouteTemplateCollapsesIds(t *testing.T) {
    cases := []struct {
        in, want string
    }{
        {"/v1/chat/completions", "/v1/chat/completions"},
        {"/v1/chat/completions/", "/v1/chat/completions"},
        {"/v1/stream/6CQaDhlua3cXEzqve1jRKA/events", "/v1/stream/{id}/events"},
        {"/v1/agent/tasks/3f2b8a90-1c2d-3e4f-5a6b-7c8d9e0f1a2b/cancel", "/v1/agent/tasks/{id}/cancel"},
        {"/gateway/v1/browser/sessions/12345", "/gateway/v1/browser/sessions/{id}"},
        {"/v1/models/abc123def456", "/v1/models/{id}"},
        {"/admin/api/config/routing", "/admin/api/config/routing"},
        {"/shell/fusion-cowork/", "/shell/fusion-cowork"},
        {"/v1/agent/tasks/999/cancel", "/v1/agent/tasks/{id}/cancel"},
        {"/", "/"},
        {"", "/"},
    }
    for _, c := range cases {
        if got := RouteTemplate(c.in); got != c.want {
            t.Errorf("RouteTemplate(%q) = %q, want %q", c.in, got, c.want)
        }
    }
}

func Test689_TenantHash(t *testing.T) {
    if TenantHash("") != "" {
        t.Fatal("empty tenant must hash to empty")
    }
    a := TenantHash("acme")
    b := TenantHash("acme")
    if a != b || len(a) != 8 {
        t.Fatalf("tenant hash must be deterministic 8 hex chars, got %q/%q", a, b)
    }
    if a == TenantHash("other") {
        t.Fatal("different tenants must hash differently")
    }
}

func Test689_ErrorSignature(t *testing.T) {
    sig := ErrorSignature(502, "/v1/chat/{id}")
    if sig != "502 /v1/chat/{id}" {
        t.Fatalf("unexpected signature %q", sig)
    }
}

func Test689_EmitDropOnFullQueue(t *testing.T) {
    e := New(1, 10, 1000, "invalid-dsn-for-test")
    if !e.Emit(Event{EventType: "http_request", Status: "200"}) {
        t.Fatal("first emit into empty queue must succeed")
    }
    if e.Emit(Event{EventType: "http_request", Status: "200"}) {
        t.Fatal("emit into full queue must drop (backpressure contract)")
    }
    if e.Dropped.Load() != 1 || e.Emitted.Load() != 1 {
        t.Fatalf("counters wrong: emitted=%d dropped=%d", e.Emitted.Load(), e.Dropped.Load())
    }
    // Emit must never block: fill with a stopped emitter and confirm the
    // call returns promptly even after the buffer is exhausted.
    done := make(chan struct{})
    go func() {
        for i := 0; i < 1000; i++ {
            e.Emit(Event{EventType: "x"})
        }
        close(done)
    }()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("Emit blocked on full queue")
    }
}

// Test689_FlushEndToEnd verifies schema-ensure + batched COPY against a real
// PostgreSQL when FUSION_OPS_TEST_DSN is set (local/prod verification); it
// skips silently in CI where no ops database exists.
func Test689_FlushEndToEnd(t *testing.T) {
    dsn := strings.TrimSpace(os.Getenv("FUSION_OPS_TEST_DSN"))
    if dsn == "" {
        t.Skip("FUSION_OPS_TEST_DSN not set — skipping live-PG verification")
    }
    e := New(100, 10, 100, dsn)
    e.Start(t.Context())
    for i := 0; i < 25; i++ {
        if !e.Emit(Event{
            EventType: "http_request",
            LatencyMS: i,
            Status:    "200",
            Payload:   map[string]any{"route": "/v1/models", "method": "GET"},
        }) {
            t.Fatalf("emit %d dropped", i)
        }
    }
    e.Emit(Event{EventType: "http_error", Status: "502",
        Payload: map[string]any{"route": "/v1/chat/{id}", "signature": "502 /v1/chat/{id}"}})
    deadline := time.Now().Add(10 * time.Second)
    for time.Now().Before(deadline) && e.Flushed.Load() < 26 {
        time.Sleep(100 * time.Millisecond)
    }
    e.Stop(5 * time.Second)
    if e.Flushed.Load() != 26 {
        t.Fatalf("expected 26 flushed, got %d (dropped=%d reconnects=%d)",
            e.Flushed.Load(), e.Dropped.Load(), e.Reconnects.Load())
    }
}
