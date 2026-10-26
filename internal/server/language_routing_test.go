package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
)

// newLanguageTestServer builds a Server whose fusion-mlx provider points at
// a fake upstream serving canned /v1/models + /health payloads, then runs
// one RefreshModelSet so ModelInfos carries the metadata the rewriter reads.
func newLanguageTestServer(t *testing.T, modelsJSON, healthJSON string) *Server {
    t.Helper()
    up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(modelsJSON))
        case "/health":
            _, _ = w.Write([]byte(healthJSON))
        default:
            http.NotFound(w, r)
        }
    }))
    t.Cleanup(up.Close)

    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.OIDC.Enabled = false

    pool := adapter.NewPool()
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type: "openai", Enabled: true, BaseURL: up.URL,
    }, cfg.Config.Routing)
    pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "openai", Enabled: true})

    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
    s := &Server{
        cfg:            cfg,
        pool:           pool,
        startTime:      time.Now(),
        store:          memorystore.NewMemoryStoreWithConfig(100, cfg.Config.Batch),
        shutdownCtx:    shutdownCtx,
        shutdownCancel: shutdownCancel,
    }
    mlx.RefreshModelSet(context.Background())
    return s
}

const langModelsJSON = `{"object":"list","data":[
    {"id":"qwen3","object":"model","owned_by":"fusion-mlx","languages":["en","zh"]},
    {"id":"jais3","object":"model","owned_by":"fusion-mlx","languages":["ar","en"]}
]}`

const langHealthJSON = `{"status":"healthy","ready":true,"model_loaded":true,
    "loaded_models":["qwen3","jais3"]}`

func langRequest(lang string) *http.Request {
    r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    if lang != "" {
        r.Header.Set(LanguageHintHeader, lang)
    }
    return r
}

// AC1: an Arabic-hinted request whose model does not declare Arabic is
// rewritten to the loaded Arabic-capable model.
func TestRewriteForLanguage_RewritesToArabicModel(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON, langHealthJSON)
    got := s.rewriteForLanguage(langRequest("ar"), "qwen3")
    if got != "jais3" {
        t.Fatalf("expected jais3, got %q", got)
    }
}

// AC2: no candidate declares the hinted language — keep the requested model.
func TestRewriteForLanguage_FallbackNoCandidate(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON, langHealthJSON)
    if got := s.rewriteForLanguage(langRequest("fr"), "qwen3"); got != "qwen3" {
        t.Fatalf("expected fallback to qwen3, got %q", got)
    }
}

// Requested model already declares the language — no rewrite.
func TestRewriteForLanguage_RequestedModelSupports(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON, langHealthJSON)
    if got := s.rewriteForLanguage(langRequest("AR"), "jais3"); got != "jais3" {
        t.Fatalf("expected jais3 kept (case-insensitive hint), got %q", got)
    }
}

// AC4: no header (or empty) — no rewrite, no metadata access.
func TestRewriteForLanguage_NoHeader(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON, langHealthJSON)
    if got := s.rewriteForLanguage(langRequest(""), "qwen3"); got != "qwen3" {
        t.Fatalf("expected qwen3, got %q", got)
    }
}

// An unloaded language candidate is not servable — keep the requested model.
func TestRewriteForLanguage_SkipsUnloadedCandidate(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON,
        `{"status":"healthy","ready":true,"model_loaded":true,"loaded_models":["qwen3"]}`)
    if got := s.rewriteForLanguage(langRequest("ar"), "qwen3"); got != "qwen3" {
        t.Fatalf("expected qwen3 (jais3 not loaded), got %q", got)
    }
}

// A key restricted to the requested model keeps it even when a language
// candidate exists — the allowlist outranks the hint, never hard-fails.
func TestRewriteForLanguage_AllowlistRejectsCandidate(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON, langHealthJSON)
    r := langRequest("ar")
    ctx := middleware.ContextWithPrincipal(r.Context(), &middleware.Principal{
        KeyConfig: &config.AuthKeyConfig{Name: "k", AllowedModels: []string{"qwen3"}},
    })
    r = r.WithContext(ctx)
    if got := s.rewriteForLanguage(r, "qwen3"); got != "qwen3" {
        t.Fatalf("expected qwen3 (candidate blocked by allowlist), got %q", got)
    }
}

// RefreshModelSet must stamp the /health loaded overlay and pass the
// languages array through into ModelInfos.
func TestRefreshModelSet_LanguagesAndLoadedPassthrough(t *testing.T) {
    s := newLanguageTestServer(t, langModelsJSON, langHealthJSON)
    mlx := s.pool.GetFusionMLX()
    infos := mlx.ModelInfos()
    if len(infos) != 2 {
        t.Fatalf("expected 2 models, got %d", len(infos))
    }
    jais := infos["jais3"]
    if !jais.Loaded {
        t.Fatalf("jais3 should be stamped loaded from /health")
    }
    if len(jais.Languages) != 2 || jais.Languages[0] != "ar" {
        t.Fatalf("jais3 languages passthrough broken: %v", jais.Languages)
    }
}
