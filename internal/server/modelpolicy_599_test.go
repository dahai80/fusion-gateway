package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
)

// Issue #599: the tenant policy gate needs a global-default fallback,
// wildcard allowlist parity with the key plane, and loud tenant-mismatch
// rejection on PUT/GET.

func seed599(t *testing.T, tenant string, p modelpolicy.Policy) *Server {
    t.Helper()
    s := newTestServer()
    s.initModelPolicyStore()
    if tenant != "" || true {
        if err := s.modelPolicies.SetPolicy(tenant, p); err != nil {
            t.Fatalf("seed: %v", err)
        }
    }
    return s
}

func Test599_NewTenantFallsBackToGlobalDefault(t *testing.T) {
    s := newTestServer()
    s.initModelPolicyStore()
    if err := s.modelPolicies.SetPolicy("", modelpolicy.Policy{
        DefaultModel:  "glm-5",
        UserAllowlist: []string{"glm*"},
    }); err != nil {
        t.Fatalf("seed global: %v", err)
    }
    if !s.modelPolicyAllowsModel("brand-new-tenant", "glm-5") {
        t.Fatal("tenant with zero policy must fall back to the global default allowlist")
    }
    if s.modelPolicyAllowsModel("brand-new-tenant", "claude-fable-5") {
        t.Fatal("fallback allowlist must still restrict non-matching models")
    }
}

func Test599_NoPolicyAnywhereStillDenies(t *testing.T) {
    s := newTestServer()
    s.initModelPolicyStore()
    if s.modelPolicyAllowsModel("empty-tenant", "glm-5") {
        t.Fatal("no tenant policy and no global default must deny (fail-closed)")
    }
}

func Test599_WildcardAllowlistAllowsAll(t *testing.T) {
    s := seed599(t, "wild", modelpolicy.Policy{UserAllowlist: []string{"*"}})
    for _, m := range []string{"glm-5", "claude-fable-5", "BAAI--bge-m3"} {
        if !s.modelPolicyAllowsModel("wild", m) {
            t.Fatalf("user_allowlist [\"*\"] must allow %s", m)
        }
    }
}

func Test599_PrefixPatternParityWithKeyPlane(t *testing.T) {
    s := seed599(t, "pref", modelpolicy.Policy{UserAllowlist: []string{"glm*"}})
    if !s.modelPolicyAllowsModel("pref", "GLM-5") {
        t.Fatal("case-insensitive prefix match required (key-plane parity)")
    }
    if s.modelPolicyAllowsModel("pref", "gpt-4o") {
        t.Fatal("non-matching model must deny")
    }
}

func Test599_PutTenantMismatch400(t *testing.T) {
    s := newTestServer()
    s.initModelPolicyStore()
    req := httptest.NewRequest(http.MethodPut, "/v1/model-policy?tenant=other", strings.NewReader(`{"user_allowlist":["glm-5"]}`))
    ctx, p := middleware.EnsurePrincipal(req.Context())
    p.AuthMethod = "admin-jwt"
    p.Role = middleware.RoleAdmin
    ctx = adapter.WithTenant(ctx, "local")
    req = req.WithContext(ctx)
    w := httptest.NewRecorder()
    s.handleModelPolicy(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("tenant mismatch must 400, got %d: %s", w.Code, w.Body.String())
    }
    if !strings.Contains(w.Body.String(), "tenant mismatch") {
        t.Fatalf("body must name the mismatch: %s", w.Body.String())
    }
    // No silent mis-write happened.
    if s.modelPolicies.HasPolicy("other") {
        t.Fatal("mismatched PUT must not write the requested tenant")
    }
    if s.modelPolicies.HasPolicy("local") {
        t.Fatal("mismatched PUT must not write the resolved tenant either")
    }
}

func Test599_GetTenantMismatch400(t *testing.T) {
    s := newTestServer()
    s.initModelPolicyStore()
    req := httptest.NewRequest(http.MethodGet, "/v1/model-policy?tenant=other", nil)
    ctx, p := middleware.EnsurePrincipal(req.Context())
    p.AuthMethod = "admin-jwt"
    p.Role = middleware.RoleAdmin
    ctx = adapter.WithTenant(ctx, "local")
    req = req.WithContext(ctx)
    w := httptest.NewRecorder()
    s.handleModelPolicy(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("GET tenant mismatch must 400, got %d", w.Code)
    }
}
