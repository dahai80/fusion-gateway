package server

// G5 (plan §9.2/§9.3): X-Fusion-Via + Server self-identification headers on
// every withMiddleware response, and the direct_calls_blocked migration hint
// bit on /v1/status.

import (
    "encoding/json"
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

func g5MinimalServer(t *testing.T) *Server {
    t.Helper()
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
    }
    s.middlewareChain = func(next http.Handler) http.Handler { return next }
    return s
}

// TestG5_ViaHeadersOnMiddlewareResponses: a client must be able to verify its
// request traversed the gateway — X-Fusion-Via: gateway plus a Server header
// carrying the build version (plan §9.2, AC12b).
func TestG5_ViaHeadersOnMiddlewareResponses(t *testing.T) {
    s := g5MinimalServer(t)

    handler := s.withMiddleware(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler(rec, req)

    if got := rec.Header().Get("X-Fusion-Via"); got != "gateway" {
        t.Errorf("X-Fusion-Via: want gateway, got %q", got)
    }
    if got := rec.Header().Get("Server"); got != "fusion-gateway/test-ver" {
        t.Errorf("Server: want fusion-gateway/test-ver, got %q", got)
    }
}

// TestG5_StatusDirectCallsBlocked: /v1/status always carries the hint bit —
// false by default (no context snapshot: SnapshotFromContext falls back to
// the global DefaultConfig).
func TestG5_StatusDirectCallsBlocked(t *testing.T) {
    s := g5MinimalServer(t)

    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    rec := httptest.NewRecorder()
    s.handleStatus(rec, req)

    var body map[string]interface{}
    if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
        t.Fatalf("decode status body: %v", err)
    }
    if body["direct_calls_blocked"] != false {
        t.Errorf("direct_calls_blocked: want false by default, got %v", body["direct_calls_blocked"])
    }
}

// TestG5_StatusDirectCallsBlockedContextSnapshot: the context snapshot (set by
// withMiddleware) wins over the startup snapshot — hot-reload flips the bit
// without a restart.
func TestG5_StatusDirectCallsBlockedContextSnapshot(t *testing.T) {
    s := g5MinimalServer(t)

    hotCfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hotCfg.Config.Server.DirectCallsBlocked = true
    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), hotCfg))
    rec := httptest.NewRecorder()
    s.handleStatus(rec, req)

    var body map[string]interface{}
    if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
        t.Fatalf("decode status body: %v", err)
    }
    if body["direct_calls_blocked"] != true {
        t.Errorf("direct_calls_blocked: want true from context snapshot, got %v", body["direct_calls_blocked"])
    }
}
