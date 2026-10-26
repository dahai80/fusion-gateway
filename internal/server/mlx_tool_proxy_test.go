package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func newToolProxyServer(t *testing.T, backendURL string) *Server {
    t.Helper()
    s := newTestServer()
    bc := config.BackendConfig{Type: "fusion-mlx", BaseURL: backendURL, Enabled: true}
    s.pool.Register("fusion-mlx", adapter.NewFusionMLXProvider(bc, config.RoutingConfig{}), bc)
    return s
}

// #340 Gap 1: the fusion-mlx tool surface must proxy verbatim (method + path
// + body) through the gateway so fusion-model-hub drops its direct :11434
// tool side channel.
func TestHandleMLXToolProxy_Verbatim(t *testing.T) {
    var gotPath, gotMethod string
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        gotMethod = r.Method
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true}`))
    }))
    defer backend.Close()

    s := newToolProxyServer(t, backend.URL)
    req := httptest.NewRequest(http.MethodPost, "/v1/quantize", strings.NewReader(`{"model":"m1"}`))
    rec := httptest.NewRecorder()
    s.handleMLXToolProxy(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    if gotPath != "/v1/quantize" || gotMethod != http.MethodPost {
        t.Errorf("backend got %s %s, want POST /v1/quantize", gotMethod, gotPath)
    }
}

func TestHandleMLXToolProxy_NoBackend(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest(http.MethodGet, "/v1/hardware", nil)
    rec := httptest.NewRecorder()
    s.handleMLXToolProxy(rec, req)
    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("expected 503, got %d", rec.Code)
    }
}

// #340 Gap 2: /v1/admin/models/{name}/load proxies straight to fusion-mlx —
// the hub drives MLX-level load through the gateway without the
// /v1/models/{id}/load hub-registry rewrite.
func TestHandleMLXModelManage_LoadVerbatim(t *testing.T) {
    var gotPath, gotMethod string
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.EscapedPath()
        gotMethod = r.Method
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"status":"loading"}`))
    }))
    defer backend.Close()

    s := newToolProxyServer(t, backend.URL)
    req := httptest.NewRequest(http.MethodPost, "/v1/admin/models/mlx-community%2FSome-Model-4bit/load", nil)
    rec := httptest.NewRecorder()
    s.handleMLXModelManage(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    if gotPath != "/v1/admin/models/mlx-community%2FSome-Model-4bit/load" {
        t.Errorf("backend path = %q, want verbatim /v1/admin/models/.../load", gotPath)
    }
    if gotMethod != http.MethodPost {
        t.Errorf("backend method = %q, want POST", gotMethod)
    }
}

// #340: unknown /v1/models/{name}/<action> (merge-adapter) is part of the
// MLX tool surface — forward verbatim to fusion-mlx instead of 404.
func TestHandleModelLoadUnload_FallthroughToMLX(t *testing.T) {
    var gotPath string
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"job":"merge-1"}`))
    }))
    defer backend.Close()

    s := newToolProxyServer(t, backend.URL)
    req := httptest.NewRequest(http.MethodPost, "/v1/models/m1/merge-adapter", nil)
    rec := httptest.NewRecorder()
    s.handleModelLoadUnload(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    if gotPath != "/v1/models/m1/merge-adapter" {
        t.Errorf("backend path = %q, want verbatim /v1/models/m1/merge-adapter", gotPath)
    }
}

// Tool-surface routes must be registered on the shared mux behind
// withMiddleware — same auth chain as the v1 surface.
func TestMLXToolProxyRoutesRegistered(t *testing.T) {
    s := newTestServer()
    mux := http.NewServeMux()
    s.registerMLXToolRoutes(mux)
    for _, path := range []string{
        "/v1/hardware", "/v1/recommend", "/v1/analyze", "/v1/analyze-batch",
        "/v1/migration-level", "/v1/convert", "/v1/quantize", "/v1/quantize/jobs/x",
        "/v1/benchmarks", "/v1/admin/models/m1/load",
    } {
        req := httptest.NewRequest(http.MethodGet, path, nil)
        if _, pattern := mux.Handler(req); pattern == "" {
            t.Errorf("route %s not registered", path)
        }
    }
}
