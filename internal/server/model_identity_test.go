package server

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// newTestServerIdentity builds a Server with two fake providers (local
// fusion-mlx + cloud) so qualified-lock and /v1/models attribution can be
// exercised without real backends.
func newTestServerIdentity() *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.Server.Port = 0
    cfg.Config.OIDC.Enabled = false
    cfg.Config.Backends = map[string]config.BackendConfig{
        "fusion-mlx": {Type: "openai", BaseURL: "http://127.0.0.1:11434", Enabled: true},
        "glm-cloud":  {Type: "anthropic", BaseURL: "http://glm.example", Enabled: true},
    }
    cfg.Config.Models.Aliases = map[string]string{
        "cc-main": "glm-cloud/glm-5",
    }

    pool := adapter.NewPool()
    local := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type: "openai", Enabled: true, BaseURL: "http://127.0.0.1:1",
    }, cfg.Config.Routing)
    cloud := adapter.NewOpenAICompatibleProvider("glm-cloud", config.BackendConfig{
        Type: "anthropic", Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "sk-x",
    })
    pool.Register("fusion-mlx", local, cfg.Config.Backends["fusion-mlx"])
    pool.Register("glm-cloud", cloud, cfg.Config.Backends["glm-cloud"])

    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
    return &Server{
        cfg:            cfg,
        pool:           pool,
        startTime:      time.Now(),
        store:          memorystore.NewMemoryStoreWithConfig(100, cfg.Config.Batch),
        shutdownCtx:    shutdownCtx,
        shutdownCancel: shutdownCancel,
    }
}

// Qualified lock: a "backend/model" pin overrides the engine decision to
// the named cloud backend (deterministic, AC13).
func TestApplyQualifiedModelLock_CloudPin(t *testing.T) {
    s := newTestServerIdentity()
    res := config.ResolveModel("glm-cloud/glm-5", s.cfg.Config.Backends, s.cfg.Config.Models)
    engineDecision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "token_budget"}
    locked := s.applyQualifiedModelLock(res, engineDecision)
    if locked.Backend != router.CloudBackend || locked.CloudTarget != "glm-cloud" {
        t.Fatalf("expected cloud/glm-cloud pin, got %s/%q", locked.Backend, locked.CloudTarget)
    }
    if locked.Reason != "qualified_lock:token_budget" {
        t.Errorf("engine reason not preserved: %s", locked.Reason)
    }
}

// Qualified lock to an unknown backend must reject loudly, not divert.
func TestApplyQualifiedModelLock_UnknownBackend(t *testing.T) {
    s := newTestServerIdentity()
    s.cfg.Config.Backends["ghost"] = config.BackendConfig{Type: "openai", Enabled: true}
    res := config.ResolveModel("ghost/m", s.cfg.Config.Backends, s.cfg.Config.Models)
    // "ghost" is in Backends but NOT registered in the pool — the runtime
    // membership check must reject.
    locked := s.applyQualifiedModelLock(res, &router.RouteDecision{Backend: router.CloudBackend, Reason: "x"})
    if !locked.Rejected {
        t.Fatalf("expected rejection for pool-missing backend, got %+v", locked)
    }
}

// AC17: cloud_models guard outranks a cloud pin — the engine's local
// decision survives a qualified cloud request for a pinned-local model.
func TestApplyQualifiedModelLock_GuardWins(t *testing.T) {
    s := newTestServerIdentity()
    s.cfg.Config.Routing.CloudModels = []string{"qwen*"}
    res := config.ResolveModel("glm-cloud/glm-5", s.cfg.Config.Backends, s.cfg.Config.Models)
    localDecision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "cloud_models_allowlist_miss"}
    locked := s.applyQualifiedModelLock(res, localDecision)
    if locked.Backend != router.LocalBackend {
        t.Fatalf("guard must outrank pin, got %s", locked.Backend)
    }
}

// No pin (bare model): decision passes through untouched.
func TestApplyQualifiedModelLock_NoPinPassthrough(t *testing.T) {
    s := newTestServerIdentity()
    res := config.ResolveModel("glm-5", s.cfg.Config.Backends, s.cfg.Config.Models)
    d := &router.RouteDecision{Backend: router.LocalBackend, Reason: "r"}
    locked := s.applyQualifiedModelLock(res, d)
    if locked != d || locked.Reason != "r" {
        t.Fatalf("bare model must not mutate decision: %+v", locked)
    }
}

// /v1/models attribution (AC14): every entry carries fusion_backend +
// fusion_qualified_id; alias entries surface with owned_by=alias (AC16).
func TestHandleModels_BackendAttributionAndAliases(t *testing.T) {
    s := newTestServerIdentity()
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    // withMiddleware injects the snapshot in production; mirror it here.
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    s.handleModels(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("status: %d", rec.Code)
    }
    var body struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if len(body.Data) == 0 {
        t.Fatal("expected model entries (httptest providers may return none; ensure at least alias entries)")
    }
    sawAlias := false
    for _, m := range body.Data {
        if m.ID == "cc-main" {
            sawAlias = true
            if m.OwnedBy != "alias" || m.QualifiedID != "glm-cloud/glm-5" {
                t.Errorf("alias entry wrong: %+v", m)
            }
            continue
        }
        if m.Backend == "" || m.QualifiedID == "" {
            t.Errorf("provider entry missing attribution: %+v", m)
        }
        if m.QualifiedID != m.Backend+"/"+m.ID {
            t.Errorf("qualified id malformed: %s", m.QualifiedID)
        }
    }
    if !sawAlias {
        t.Error("alias entry cc-main not listed in /v1/models")
    }
}
