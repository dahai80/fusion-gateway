package server

import (
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

func TestActiveRequests_TrackAndRelease(t *testing.T) {
    slog.Info("test ActiveRequests_TrackAndRelease")
    a := newActiveRequests()
    id := a.Begin("POST", "/v1/chat/completions", "trace-1")
    if got := len(a.Snapshot()); got != 1 {
        t.Fatalf("expected 1 tracked request, got %d", got)
    }
    a.End(id)
    if got := len(a.Snapshot()); got != 0 {
        t.Fatalf("expected 0 tracked requests after End, got %d", got)
    }
}

// The drain report is only useful if it lists the OLDEST handler first — that
// is the request that held the window open.
func TestActiveRequests_SnapshotOldestFirst(t *testing.T) {
    slog.Info("test ActiveRequests_SnapshotOldestFirst")
    a := newActiveRequests()
    young := a.Begin("GET", "/v1/models", "trace-young")
    old := a.Begin("POST", "/v1/messages", "trace-old")
    a.mu.Lock()
    e := a.items[old]
    e.started = time.Now().Add(-45 * time.Second)
    a.items[old] = e
    a.mu.Unlock()

    snap := a.Snapshot()
    if len(snap) != 2 {
        t.Fatalf("expected 2 entries, got %d", len(snap))
    }
    if snap[0].Trace != "trace-old" {
        t.Errorf("oldest request must come first, got %q", snap[0].Trace)
    }
    if snap[0].Age < 40*time.Second {
        t.Errorf("expected age > 40s for the backdated entry, got %s", snap[0].Age)
    }
    a.End(young)
    a.End(old)
}

// &Server{} literals (many tests, and any future construction site that skips
// New) must not panic on the request path or on the drain report.
func TestActiveRequests_NilSafe(t *testing.T) {
    slog.Info("test ActiveRequests_NilSafe")
    s := &Server{}
    if id := s.activeReqs.Begin("GET", "/healthz", ""); id != 0 {
        t.Errorf("nil tracker must return slot 0, got %d", id)
    }
    s.activeReqs.End(0)
    if snap := s.activeReqs.Snapshot(); snap != nil {
        t.Errorf("nil tracker must report no requests, got %v", snap)
    }
    s.LogActiveRequests("test")
}

// The tracker has to be wired into the real request funnel: a handler blocked
// mid-flight must be visible to the drain report, and must disappear once it
// returns (otherwise a later drain would blame a finished request).
func TestWithMiddleware_RegistersActiveRequest(t *testing.T) {
    slog.Info("test WithMiddleware_RegistersActiveRequest")
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    s := &Server{
        cfg:         cfg,
        pool:        adapter.NewPool(),
        hwCollector: hwCollector,
        router:      router.NewEngine(cfg, hwCollector),
        tokEngine:   tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        startTime:   time.Now(),
        version:     "test-ver",
        activeReqs:  newActiveRequests(),
    }
    s.middlewareChain = func(next http.Handler) http.Handler { return next }

    var inside []ActiveRequest
    handler := s.withMiddleware(func(w http.ResponseWriter, r *http.Request) {
        inside = s.activeReqs.Snapshot()
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    handler(httptest.NewRecorder(), req)

    if len(inside) != 1 {
        t.Fatalf("handler must see itself tracked, got %d entries", len(inside))
    }
    if inside[0].Path != "/v1/chat/completions" {
        t.Errorf("tracked path: want /v1/chat/completions, got %q", inside[0].Path)
    }
    if inside[0].Trace == "" {
        t.Error("tracked entry should carry the request trace id")
    }
    if got := len(s.activeReqs.Snapshot()); got != 0 {
        t.Errorf("finished request must be released, %d still tracked", got)
    }
}
