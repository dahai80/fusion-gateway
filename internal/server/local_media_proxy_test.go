package server

import (
    "context"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// localMediaMLXServer spins a fusion-mlx stand-in serving /v1/models plus the
// two #366 media surfaces, recording the upstream Authorization headers so
// the tests can assert credential injection by the provider ReverseProxy.
func localMediaMLXServer(t *testing.T) (*httptest.Server, *[]string) {
    t.Helper()
    authSeen := &[]string{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"object":"list","data":[{"id":"kokoro-82m","object":"model","owned_by":"mlx"},{"id":"flux-schnell","object":"model","owned_by":"mlx"}]}`))
        case "/v1/audio/speech":
            *authSeen = append(*authSeen, r.Header.Get("Authorization"))
            w.Header().Set("Content-Type", "audio/wav")
            _, _ = w.Write([]byte("RIFF....WAVEfmt"))
        case "/v1/images/generations":
            *authSeen = append(*authSeen, r.Header.Get("Authorization"))
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"created":1,"data":[{"url":"http://img/1.png"}]}`))
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    return srv, authSeen
}

// TestHandleSpeech_LocalBackendProxiesToMLX (#366): a speech request whose
// model resolves to the local backend must forward verbatim to fusion-mlx
// /v1/audio/speech and relay the audio bytes + content type, instead of
// falling through resolveCloudProvider and 400ing "Provider does not support
// speech".
func TestHandleSpeech_LocalBackendProxiesToMLX(t *testing.T) {
    srv, authSeen := localMediaMLXServer(t)
    defer srv.Close()

    s := newTestServer()
    registerLocalMLX(t, s, srv.URL)

    body := `{"model":"kokoro-82m","input":"Hello world","voice":"af_heart","response_format":"wav"}`
    req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
    rec := httptest.NewRecorder()
    s.handleSpeech(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("Content-Type"); got != "audio/wav" {
        t.Fatalf("content-type = %q, want audio/wav", got)
    }
    if !strings.HasPrefix(rec.Body.String(), "RIFF") {
        t.Fatalf("expected wav bytes relayed, got: %q", rec.Body.String())
    }
    if len(*authSeen) == 0 {
        t.Fatal("upstream speech request missing Authorization injection")
    }
    slog.Info("TestHandleSpeech_LocalBackendProxiesToMLX passed")
}

// TestHandleImages_LocalBackendProxiesToMLX (#366): an image request whose
// model resolves to the local backend must forward verbatim to fusion-mlx
// /v1/images/generations instead of always resolving CloudDefault and 400ing
// "Selected backend does not support image generation".
func TestHandleImages_LocalBackendProxiesToMLX(t *testing.T) {
    srv, authSeen := localMediaMLXServer(t)
    defer srv.Close()

    s := newTestServer()
    registerLocalMLX(t, s, srv.URL)

    body := `{"model":"flux-schnell","prompt":"a cat","n":1}`
    req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
    rec := httptest.NewRecorder()
    s.handleImages(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), `"url":"http://img/1.png"`) {
        t.Fatalf("expected upstream image response relayed, got: %s", rec.Body.String())
    }
    if len(*authSeen) == 0 {
        t.Fatal("upstream image request missing Authorization injection")
    }
    slog.Info("TestHandleImages_LocalBackendProxiesToMLX passed")
}

// registerLocalMLX registers a real FusionMLXProvider against the mock and
// refreshes its model set so the router resolves local models.
func registerLocalMLX(t *testing.T, s *Server, baseURL string) {
    t.Helper()
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: baseURL,
        Enabled: true,
    }, config.RoutingConfig{})
    s.pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "fusion-mlx", BaseURL: baseURL, Enabled: true})
    mlx.RefreshModelSet(context.Background())
    // mirror main.go wiring: the engine reads the live local model set via
    // this callback; without it Decide falls through to the cloud chain.
    s.router.SetLocalModels(mlx.ModelSet)
    s.router.SetLocalReady(true)
}
