package server

import (
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
)

// newTestServerE5 builds a Server with a model-hub provider whose ReverseProxy
// targets a recording upstream, plus a small ProxyMaxBodySize so the E5 cap is
// exercisable without generating a 256 MiB body.
func newTestServerE5(upstreamURL string) *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.Server.Port = 0
    cfg.Config.Server.MaxRequestBodySize = 5 << 20
    cfg.Config.Server.ProxyMaxBodySize = 16 // tiny cap so the test body (100B) trips it
    cfg.Config.OIDC.Enabled = false

    pool := adapter.NewPool()
    pool.Register("model-hub",
        adapter.NewFusionModelHubProvider("model-hub", config.BackendConfig{
            Type:    "fusion-model-hub",
            Enabled: true,
            BaseURL: upstreamURL,
        }),
        config.BackendConfig{Type: "fusion-model-hub", Enabled: true, BaseURL: upstreamURL})

    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
    s := &Server{
        cfg:            cfg,
        pool:           pool,
        startTime:      time.Now(),
        store:          memorystore.NewMemoryStoreWithConfig(1000, cfg.Config.Batch),
        cache:          cache.New(cfg.Config.Cache),
        fetchCoalescer: newCoalescer(),
        shutdownCtx:    shutdownCtx,
        shutdownCancel: shutdownCancel,
    }
    s.buildMiddlewareChain()
    return s
}

// TestE5_ProxyBodyCap_RejectsOversizedBody: a body larger than
// server.proxy_max_body_size forwarded through the model-load proxy must NOT
// reach the upstream in full — the MaxBytesReader cap trips mid-read and the
// proxy errors out. Before E5 the proxy forwarded r.Body with no cap, so the
// upstream received the entire 100-byte body. Revert (drop wrapProxyBody in
// handleModelLoadUnload): upstreamReceived == 100 → FAIL.
func TestE5_ProxyBodyCap_RejectsOversizedBody(t *testing.T) {
    var upstreamReceived int64
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        n, _ := io.Copy(io.Discard, r.Body)
        atomic.StoreInt64(&upstreamReceived, n)
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()

    s := newTestServerE5(upstream.URL)
    body := strings.NewReader(strings.Repeat("x", 100)) // 100B > 16B cap

    req := httptest.NewRequest(http.MethodPost, "/v1/models/test-model/load", body)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleModelLoadUnload(rec, req)

    got := atomic.LoadInt64(&upstreamReceived)
    if got >= 100 {
        t.Errorf("E5: oversized proxy body (100B > cap 16B) must be truncated/rejected before reaching upstream, but upstream received %d bytes (full body forwarded — pre-E5 unbounded proxy body bug)", got)
    }
}

// TestE5_ProxyBodyCap_AllowsUnderCapBody: a body within the cap reaches the
// upstream in full. Companion to the rejection guard — proves the cap does not
// reject legitimate small bodies.
func TestE5_ProxyBodyCap_AllowsUnderCapBody(t *testing.T) {
    var upstreamReceived int64
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        n, _ := io.Copy(io.Discard, r.Body)
        atomic.StoreInt64(&upstreamReceived, n)
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()

    s := newTestServerE5(upstream.URL)
    body := strings.NewReader("small") // 5B < 16B cap

    req := httptest.NewRequest(http.MethodPost, "/v1/models/test-model/load", body)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleModelLoadUnload(rec, req)

    if got := atomic.LoadInt64(&upstreamReceived); got != 5 {
        t.Errorf("E5: under-cap body (5B < 16B) must reach upstream in full, got %d bytes", got)
    }
}

// TestE5_ProxyBodyCap_DefaultFallback: ProxyMaxBodySize=0 falls back to the
// maxProxyBodySize const (256 MiB), so a 100B body is allowed. Proves the
// 0-fallback path so an omitted YAML key still caps the body via the const.
func TestE5_ProxyBodyCap_DefaultFallback(t *testing.T) {
    var upstreamReceived int64
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        n, _ := io.Copy(io.Discard, r.Body)
        atomic.StoreInt64(&upstreamReceived, n)
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()

    s := newTestServerE5(upstream.URL)
    s.cfg.Config.Server.ProxyMaxBodySize = 0 // fall back to const default

    body := strings.NewReader(strings.Repeat("x", 100))
    req := httptest.NewRequest(http.MethodPost, "/v1/models/test-model/load", body)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleModelLoadUnload(rec, req)

    if got := atomic.LoadInt64(&upstreamReceived); got != 100 {
        t.Errorf("E5: ProxyMaxBodySize=0 must fall back to const default (256MiB) and allow a 100B body, got %d bytes", got)
    }
}
