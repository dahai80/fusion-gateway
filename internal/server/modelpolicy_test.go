package server

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/admin"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// modelPolicyTestServer builds the minimal Server the #486 handlers need:
// a policy store and, when wired, a router engine with a fixed local-model
// set for the privacy filter.
func modelPolicyTestServer(t *testing.T, localModels ...string) *Server {
    t.Helper()
    s := &Server{}
    s.modelPolicies = modelpolicy.NewStore()
    if localModels != nil {
        cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
        e := router.NewEngine(cfg, hardware.NewCollector(&cfg.Config.Hardware))
        set := map[string]bool{}
        for _, m := range localModels {
            set[m] = true
        }
        e.SetLocalModels(func() map[string]bool { return set })
        s.router = e
    }
    return s
}

func modelPolicyRequest(s *Server, method, target, tenant string, principal *middleware.Principal, body any) *httptest.ResponseRecorder {
    var rdr *bytes.Reader
    if body == nil {
        rdr = bytes.NewReader(nil)
    } else {
        raw, _ := json.Marshal(body)
        rdr = bytes.NewReader(raw)
    }
    r := httptest.NewRequest(method, target, rdr)
    ctx := r.Context()
    if tenant != "" {
        ctx = adapter.WithTenant(ctx, tenant)
    }
    if principal != nil {
        ctx = middleware.ContextWithPrincipal(ctx, principal)
    }
    r = r.WithContext(ctx)
    w := httptest.NewRecorder()
    if bytes.Contains([]byte(target), []byte("model-policy")) {
        s.handleModelPolicy(w, r)
    } else {
        s.handleModelPreference(w, r)
    }
    return w
}

func adminPrincipal() *middleware.Principal {
    return &middleware.Principal{Role: middleware.RoleAdmin}
}

// #486: the admin JWT bridge must reach the /v1 model-policy plane — the
// bridge otherwise only runs inside withAdminOnly (admin_handlers.go), so a
// deployment without OIDC would have NO principal able to administer the
// policy. A valid admin-login Bearer is bridged to RoleAdmin exactly like
// the admin surface does; an invalid one fails closed at 403.
func TestModelPolicy_AdminJWTBridge(t *testing.T) {
    aa, err := admin.NewAdminAuth("test-admin-jwt-secret-32-characters!", map[string]string{"root": "pw"})
    if err != nil {
        t.Fatalf("admin auth: %v", err)
    }
    s := modelPolicyTestServer(t)
    s.adminAuth = aa
    tok, err := aa.GenerateToken("root", "admin")
    if err != nil {
        t.Fatalf("generate token: %v", err)
    }
    r := httptest.NewRequest("GET", "/v1/model-policy", nil)
    r.Header.Set("Authorization", "Bearer "+tok)
    r = r.WithContext(adapter.WithTenant(r.Context(), "tenant-a"))
    w := httptest.NewRecorder()
    s.handleModelPolicy(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("admin JWT GET must be 200, got %d body=%s", w.Code, w.Body.String())
    }
    // Tampered token fails closed.
    r2 := httptest.NewRequest("GET", "/v1/model-policy", nil)
    r2.Header.Set("Authorization", "Bearer "+tok+"x")
    r2 = r2.WithContext(adapter.WithTenant(r2.Context(), "tenant-a"))
    w2 := httptest.NewRecorder()
    s.handleModelPolicy(w2, r2)
    if w2.Code != http.StatusForbidden {
        t.Fatalf("tampered admin JWT must be 403, got %d", w2.Code)
    }
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
    t.Helper()
    var out map[string]any
    if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
        t.Fatalf("decode body %q: %v", w.Body.String(), err)
    }
    return out
}

// #486 AC: model-policy administration is admin-role only; the inference
// master key (RoleInference per RR3) must not administer policy.
func TestModelPolicy_AdminGate(t *testing.T) {
    s := modelPolicyTestServer(t)
    if w := modelPolicyRequest(s, "GET", "/v1/model-policy", "tenant-a", nil, nil); w.Code != http.StatusForbidden {
        t.Fatalf("unauthenticated GET must be 403, got %d", w.Code)
    }
    viewer := &middleware.Principal{Role: middleware.RoleViewer}
    if w := modelPolicyRequest(s, "GET", "/v1/model-policy", "tenant-a", viewer, nil); w.Code != http.StatusForbidden {
        t.Fatalf("viewer GET must be 403, got %d", w.Code)
    }
    master := &middleware.Principal{IsMaster: true}
    if w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", master, modelpolicy.Policy{DefaultModel: "mlx/x"}); w.Code != http.StatusForbidden {
        t.Fatalf("inference master key PUT must be 403, got %d", w.Code)
    }
    w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{
        DefaultModel:    "mlx/default",
        ModuleOverrides: map[string]string{"fusion-doc": "mlx/doc"},
        UserAllowlist:   []string{"mlx/doc", "mlx/chat"},
        PrivacyMode:     modelpolicy.PrivacyModeLocalOnly,
    })
    if w.Code != http.StatusOK {
        t.Fatalf("admin PUT must be 200, got %d body=%s", w.Code, w.Body.String())
    }
    w = modelPolicyRequest(s, "GET", "/v1/model-policy", "tenant-a", adminPrincipal(), nil)
    out := decodeBody(t, w)
    pol, _ := out["policy"].(map[string]any)
    if pol == nil || pol["default_model"] != "mlx/default" || pol["privacy_mode"] != "local_only" {
        t.Fatalf("GET must reflect the stored policy, got %v", out)
    }
    // Invalid enum is refused 400.
    w = modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{PrivacyMode: "paranoid"})
    if w.Code != http.StatusBadRequest {
        t.Fatalf("invalid privacy_mode must be 400, got %d", w.Code)
    }
}

// #486 AC: tenant A policy must not leak to tenant B over the wire.
func TestModelPolicy_TenantIsolationHTTP(t *testing.T) {
    s := modelPolicyTestServer(t)
    if w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{DefaultModel: "mlx/a-only"}); w.Code != http.StatusOK {
        t.Fatalf("put tenant-a: %d", w.Code)
    }
    out := decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-policy", "tenant-b", adminPrincipal(), nil))
    pol, _ := out["policy"].(map[string]any)
    if pol == nil || pol["default_model"] != nil {
        t.Fatalf("tenant B must see no tenant A default, got %v", out)
    }
}

// #486 AC: resolution order over HTTP — module override > user preference
// > tenant default; the fallback comes from routing.default_model.
func TestModelPreference_ResolveChainHTTP(t *testing.T) {
    s := modelPolicyTestServer(t)
    s.cfg = &config.ConfigSnapshot{Config: config.DefaultConfig()}
    s.cfg.Config.Routing.DefaultModel = "legacy/build-time"
    if w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{
        DefaultModel:    "mlx/tenant-default",
        ModuleOverrides: map[string]string{"fusion-code": "mlx/coder"},
        UserAllowlist:   []string{"mlx/chat"},
    }); w.Code != http.StatusOK {
        t.Fatalf("put policy: %d body=%s", w.Code, w.Body.String())
    }
    user := &middleware.Principal{Role: middleware.RoleViewer, KeyConfig: &config.AuthKeyConfig{Name: "user-1"}}
    // Module override wins.
    out := decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-preference?module=fusion-code", "tenant-a", user, nil))
    if out["model"] != "mlx/coder" || out["source"] != "module_override" {
        t.Fatalf("override must win, got %v", out)
    }
    // User preference next.
    if w := modelPolicyRequest(s, "PUT", "/v1/model-preference", "tenant-a", user, modelPreferenceRequest{Module: "fusion-doc", Model: "mlx/chat"}); w.Code != http.StatusOK {
        t.Fatalf("put pref: %d body=%s", w.Code, w.Body.String())
    }
    out = decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-preference?module=fusion-doc", "tenant-a", user, nil))
    if out["model"] != "mlx/chat" || out["source"] != "user_preference" {
        t.Fatalf("preference must apply, got %v", out)
    }
    // A different user falls to the tenant default.
    user2 := &middleware.Principal{Role: middleware.RoleViewer, KeyConfig: &config.AuthKeyConfig{Name: "user-2"}}
    out = decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-preference?module=fusion-doc", "tenant-a", user2, nil))
    if out["model"] != "mlx/tenant-default" || out["source"] != "tenant_default" {
        t.Fatalf("tenant default must apply, got %v", out)
    }
    // An unknown tenant falls to the build-time fallback.
    out = decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-preference?module=fusion-doc", "tenant-b", user2, nil))
    if out["model"] != "legacy/build-time" || out["source"] != "fallback" {
        t.Fatalf("fallback must apply, got %v", out)
    }
}

// #486 AC: user preference writes are allowlist-constrained; outside the
// allowlist = 403, inside = 200.
func TestModelPreference_AllowlistGate(t *testing.T) {
    s := modelPolicyTestServer(t)
    if w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{UserAllowlist: []string{"mlx/allowed"}}); w.Code != http.StatusOK {
        t.Fatalf("put policy: %d", w.Code)
    }
    user := &middleware.Principal{Role: middleware.RoleViewer, KeyConfig: &config.AuthKeyConfig{Name: "user-1"}}
    w := modelPolicyRequest(s, "PUT", "/v1/model-preference", "tenant-a", user, modelPreferenceRequest{Module: "fusion-doc", Model: "cloud/not-allowed"})
    if w.Code != http.StatusForbidden {
        t.Fatalf("outside allowlist must be 403, got %d", w.Code)
    }
    w = modelPolicyRequest(s, "PUT", "/v1/model-preference", "tenant-a", user, modelPreferenceRequest{Module: "fusion-doc", Model: "mlx/allowed"})
    if w.Code != http.StatusOK {
        t.Fatalf("inside allowlist must be 200, got %d body=%s", w.Code, w.Body.String())
    }
}

// #486 AC: privacy_mode=local_only filters resolution to local models.
func TestModelPreference_PrivacyFilterHTTP(t *testing.T) {
    s := modelPolicyTestServer(t, "mlx/local", "mlx/local2")
    if w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{
        DefaultModel: "cloud/big",
        PrivacyMode:  modelpolicy.PrivacyModeLocalOnly,
    }); w.Code != http.StatusOK {
        t.Fatalf("put policy: %d", w.Code)
    }
    user := &middleware.Principal{Role: middleware.RoleViewer, KeyConfig: &config.AuthKeyConfig{Name: "user-1"}}
    // Nothing local in the chain: resolution must come back empty and
    // flagged, never a cloud model.
    out := decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-preference?module=fusion-doc", "tenant-a", user, nil))
    if out["model"] != "" || out["privacy_filtered"] != true {
        t.Fatalf("local_only with no local candidates must resolve empty+filtered, got %v", out)
    }
    // A local user preference is honored under the pin.
    if w := modelPolicyRequest(s, "PUT", "/v1/model-policy", "tenant-a", adminPrincipal(), modelpolicy.Policy{
        DefaultModel: "cloud/big",
        UserAllowlist: []string{"mlx/local"},
        PrivacyMode:  modelpolicy.PrivacyModeLocalOnly,
    }); w.Code != http.StatusOK {
        t.Fatalf("put policy: %d", w.Code)
    }
    if w := modelPolicyRequest(s, "PUT", "/v1/model-preference", "tenant-a", user, modelPreferenceRequest{Module: "fusion-doc", Model: "mlx/local"}); w.Code != http.StatusOK {
        t.Fatalf("put pref: %d body=%s", w.Code, w.Body.String())
    }
    out = decodeBody(t, modelPolicyRequest(s, "GET", "/v1/model-preference?module=fusion-doc", "tenant-a", user, nil))
    if out["model"] != "mlx/local" || out["privacy_filtered"] != true {
        t.Fatalf("local preference must be honored under the pin, got %v", out)
    }
}
