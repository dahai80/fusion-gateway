package server

import (
    "context"
    "io"
    "mime/multipart"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// #698: the four fusion-mlx media surfaces must passthrough the gateway with
// router admission (breaker + backend access) and local-only routing, so the
// drama pipeline can point FUSION_MLX_BASE_URL at :11432 and retire the
// hardcoded :11434 dual path.

func newMedia698Server(t *testing.T, upstreamPath *string, upstreamBody *string) (*Server, *httptest.Server) {
    t.Helper()
    up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        if upstreamPath != nil {
            *upstreamPath = r.URL.Path
        }
        if upstreamBody != nil {
            *upstreamBody = string(b)
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"ok":true}`))
    }))
    t.Cleanup(up.Close)

    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.OIDC.Enabled = false
    cfg.Config.Routing.Mode = "local"
    pool := adapter.NewPool()
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type: "openai", Enabled: true, BaseURL: up.URL,
    }, cfg.Config.Routing)
    pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "openai", Enabled: true})

    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
    s := &Server{
        cfg:            cfg,
        pool:           pool,
        hwCollector:    hwCollector,
        router:         router.NewEngine(cfg, hwCollector),
        startTime:      time.Now(),
        store:          memorystore.NewMemoryStoreWithConfig(100, cfg.Config.Batch),
        shutdownCtx:    shutdownCtx,
        shutdownCancel: shutdownCancel,
    }
    mlx.RefreshModelSet(context.Background())
    return s, up
}

func Test698_MediaJSONRoutesForwardVerbatim(t *testing.T) {
    for _, tc := range []struct {
        path, body, surface string
    }{
        {"/v1/images/generate", `{"model":"mflux-1-schnell","prompt":"a cat"}`, "images-generate"},
        {"/v1/videos/generate", `{"model":"wan2.2","prompt":"sunrise"}`, "videos-generate"},
    } {
        var gotPath, gotBody string
        s, _ := newMedia698Server(t, &gotPath, &gotBody)
        rec := httptest.NewRecorder()
        req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
        req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
        s.mediaJSONHandler(tc.surface)(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("%s: status %d body %s", tc.path, rec.Code, rec.Body.String())
        }
        if gotPath != tc.path {
            t.Fatalf("%s: upstream path %s", tc.path, gotPath)
        }
        if gotBody != tc.body {
            t.Fatalf("%s: body not forwarded verbatim: %s", tc.path, gotBody)
        }
    }
}

func Test698_MediaMultipartForwardsModel(t *testing.T) {
    var gotPath, gotBody string
    s, _ := newMedia698Server(t, &gotPath, &gotBody)

    var buf strings.Builder
    mw := multipart.NewWriter(&buf)
    _ = mw.WriteField("model", "mflux-1-dev")
    _ = mw.WriteField("prompt", "redo the sky")
    fw, _ := mw.CreateFormFile("image", "in.png")
    _, _ = fw.Write([]byte("PNGDATA"))
    _ = mw.Close()

    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(buf.String()))
    req.Header.Set("Content-Type", mw.FormDataContentType())
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    s.mediaMultipartHandler("images-edits")(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("edits: status %d body %s", rec.Code, rec.Body.String())
    }
    if gotPath != "/v1/images/edits" {
        t.Fatalf("edits: upstream path %s", gotPath)
    }
    if !strings.Contains(gotBody, "PNGDATA") || !strings.Contains(gotBody, "mflux-1-dev") {
        t.Fatalf("edits: multipart not forwarded intact: %.120s", gotBody)
    }
}

func Test698_MediaRejectsWrongMethodAndBadJSON(t *testing.T) {
    s, _ := newMedia698Server(t, nil, nil)
    rec := httptest.NewRecorder()
    s.mediaJSONHandler("videos-generate")(rec, httptest.NewRequest(http.MethodGet, "/v1/videos/generate", nil))
    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("GET on media route: status %d", rec.Code)
    }
    rec2 := httptest.NewRecorder()
    s.mediaJSONHandler("videos-generate")(rec2, httptest.NewRequest(http.MethodPost, "/v1/videos/generate", strings.NewReader("not-json")))
    if rec2.Code != http.StatusBadRequest {
        t.Fatalf("bad json: status %d", rec2.Code)
    }
}

func Test698_MediaMultipartSuperResolutionForwards(t *testing.T) {
    var gotPath, gotBody string
    s, _ := newMedia698Server(t, &gotPath, &gotBody)

    var buf strings.Builder
    mw := multipart.NewWriter(&buf)
    _ = mw.WriteField("model", "seedvr2")
    _ = mw.WriteField("scale", "2")
    fw, _ := mw.CreateFormFile("image", "frame.png")
    _, _ = fw.Write([]byte("SR-INPUT"))
    _ = mw.Close()

    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/v1/images/super-resolution", strings.NewReader(buf.String()))
    req.Header.Set("Content-Type", mw.FormDataContentType())
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    s.mediaMultipartHandler("images-super-resolution")(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("super-resolution: status %d body %s", rec.Code, rec.Body.String())
    }
    if gotPath != "/v1/images/super-resolution" || !strings.Contains(gotBody, "SR-INPUT") {
        t.Fatalf("super-resolution: path %s body %.120s", gotPath, gotBody)
    }
}
