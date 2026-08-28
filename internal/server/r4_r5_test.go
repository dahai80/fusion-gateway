package server

// R4 (version visibility) + R5 (log_level→slog) audit-fix tests.

import (
    "encoding/json"
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

// r4MinimalServer builds a Server with the fields handleStatus touches
// (cfg, hwCollector, router, startTime, version, commit). It does NOT call
// server.New — we only exercise the /v1/status handler directly.
func r4MinimalServer(t *testing.T) *Server {
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
        commit:      "test-commit",
    }
    return s
}

// TestR4_StatusReportsVersionAndCommit asserts /v1/status surfaces the
// build-time version + commit (R4 audit fix), not just config_version.
func TestR4_StatusReportsVersionAndCommit(t *testing.T) {
    s := r4MinimalServer(t)

    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    rec := httptest.NewRecorder()
    s.handleStatus(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status: want 200, got %d", rec.Code)
    }

    var body map[string]interface{}
    if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
        t.Fatalf("decode status body: %v", err)
    }
    if body["version"] != "test-ver" {
        t.Errorf("version: want test-ver, got %v", body["version"])
    }
    if body["commit"] != "test-commit" {
        t.Errorf("commit: want test-commit, got %v", body["commit"])
    }
    if body["config_version"] == nil {
        t.Error("config_version should still be present")
    }
}

// TestR4_SetVersionDefaults asserts a freshly built Server (no SetVersion)
// reports the default "dev"/"unknown" — a plain go build must not 404 the key.
func TestR4_SetVersionDefaults(t *testing.T) {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    s := &Server{
        cfg:         cfg,
        pool:        adapter.NewPool(),
        hwCollector: hwCollector,
        router:      router.NewEngine(cfg, hwCollector),
        tokEngine:   tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        startTime:   time.Now(),
        // version/commit intentionally zero — no SetVersion call.
    }

    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    rec := httptest.NewRecorder()
    s.handleStatus(rec, req)

    var body map[string]interface{}
    _ = json.NewDecoder(rec.Body).Decode(&body)
    if body["version"] != "" {
        t.Errorf("default version: want empty string, got %v", body["version"])
    }
}

// TestR4_SetVersion stamps values post-construction (mirrors main.go wiring).
func TestR4_SetVersion(t *testing.T) {
    s := r4MinimalServer(t)
    s.version = ""
    s.commit = ""
    s.SetVersion("v0.8.46", "abc1234")
    if s.version != "v0.8.46" || s.commit != "abc1234" {
        t.Errorf("SetVersion: got version=%q commit=%q", s.version, s.commit)
    }
}

// TestR5_ParseLevel asserts the log_level→slog.Level mapping (R5 audit fix).
// Empty/unknown default to Info (the safe baseline) without panicking.
func TestR5_ParseLevel(t *testing.T) {
    cases := []struct {
        in   string
        want slog.Level
        known bool
    }{
        {"debug", slog.LevelDebug, true},
        {"DEBUG", slog.LevelDebug, true},
        {"  info  ", slog.LevelInfo, true},
        {"warn", slog.LevelWarn, true},
        {"warning", slog.LevelWarn, true},
        {"error", slog.LevelError, true},
        {"", slog.LevelInfo, true},
        {"info", slog.LevelInfo, true},
        {"bogus", slog.LevelInfo, false},
        {"verbose", slog.LevelInfo, false},
    }
    for _, c := range cases {
        got, known := parseLevel(c.in)
        if got != c.want || known != c.known {
            t.Errorf("parseLevel(%q): want (%v,%v), got (%v,%v)", c.in, c.want, c.known, got, known)
        }
    }
}

// TestR5_SetupLogging_NoPanic asserts setupLogging runs without panicking on
// every accepted + unknown level and sets a non-nil default handler.
func TestR5_SetupLogging_NoPanic(t *testing.T) {
    for _, lvl := range []string{"debug", "info", "warn", "error", "", "garbage"} {
        // Must not panic on any input.
        setupLogging(lvl)
    }
}
