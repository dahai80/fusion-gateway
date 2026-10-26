package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #327: /v1/laya/* must proxy to fusion-mlx verbatim so gateway-mode clients
// (fusion-bench) reach the laya surface routes without a direct :11434
// exception.
func TestHandleLayaProxy_ProxiesVerbatim(t *testing.T) {
    var gotPath, gotMethod string
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        gotMethod = r.Method
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"loaded":true}`))
    }))
    defer backend.Close()

    s := newTestServer()
    bc := config.BackendConfig{Type: "fusion-mlx", BaseURL: backend.URL, Enabled: true}
    s.pool.Register("fusion-mlx", adapter.NewFusionMLXProvider(bc, config.RoutingConfig{}), bc)

    req := httptest.NewRequest(http.MethodGet, "/v1/laya/status", nil)
    rec := httptest.NewRecorder()
    s.handleLayaProxy(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    if gotPath != "/v1/laya/status" {
        t.Errorf("backend path = %q, want /v1/laya/status", gotPath)
    }
    if gotMethod != http.MethodGet {
        t.Errorf("backend method = %q, want GET", gotMethod)
    }
}

func TestHandleLayaProxy_PostDecide(t *testing.T) {
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"decision":"code"}`))
    }))
    defer backend.Close()

    s := newTestServer()
    bc := config.BackendConfig{Type: "fusion-mlx", BaseURL: backend.URL, Enabled: true}
    s.pool.Register("fusion-mlx", adapter.NewFusionMLXProvider(bc, config.RoutingConfig{}), bc)

    req := httptest.NewRequest(http.MethodPost, "/v1/laya/decide", strings.NewReader(`{"prompt":"read a file"}`))
    rec := httptest.NewRecorder()
    s.handleLayaProxy(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
}

func TestHandleLayaProxy_NoBackend(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest(http.MethodGet, "/v1/laya/status", nil)
    rec := httptest.NewRecorder()
    s.handleLayaProxy(rec, req)
    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("expected 503, got %d", rec.Code)
    }
}

// #327: with auth.accept_backend_keys on, a credential equal to a local
// backend's api_key authenticates as "backend:<name>"; off (default) it must
// stay rejected by the matcher.
func TestBackendKeyMatcher(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Backends = map[string]config.BackendConfig{
        "fusion-mlx": {Type: "fusion-mlx", BaseURL: "http://127.0.0.1:11434", APIKey: "fg-admin-key", Enabled: true},
    }

    // Default off: no matcher.
    if m := s.backendKeyMatcher(); m != nil {
        t.Fatal("accept_backend_keys off must return nil matcher")
    }

    // On: local backend key matches, foreign key does not.
    s.cfg.Config.Auth.AcceptBackendKeys = true
    m := s.backendKeyMatcher()
    if m == nil {
        t.Fatal("accept_backend_keys on with a keyed local backend must return a matcher")
    }
    kc := m("fg-admin-key")
    if kc == nil || kc.Name != "backend:fusion-mlx" {
        t.Fatalf("expected backend:fusion-mlx match, got %+v", kc)
    }
    if m("wrong-key") != nil {
        t.Fatal("foreign key must not match")
    }
}

// Cloud provider keys must never double as gateway credentials even when the
// feature is on.
func TestBackendKeyMatcher_CloudBackendsExcluded(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.AcceptBackendKeys = true
    s.cfg.Config.Backends = map[string]config.BackendConfig{
        "deepseek": {Type: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "sk-cloud", Enabled: true},
    }
    if m := s.backendKeyMatcher(); m != nil {
        t.Fatal("cloud-only backends must not produce a matcher")
    }
}
