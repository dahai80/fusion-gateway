package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// Issue #606: no credential could administer model policy for identity
// tenants — identity SSO sessions with the tenant_admin role were 403 (the
// bridge only elevates shell-store admins), so community users hit the
// hard 403 with no admin path. Fix: an identity-derived shell session
// (admin-class role, non-local tenant) bridges to RoleTenantAdmin and may
// PUT/GET /v1/model-policy scoped to its own tenant ONLY. The local and ""
// namespaces stay admin-only; member-role sessions stay 403.

func sso602Session(t *testing.T, role, tenant string) (*Server, string) {
    t.Helper()
    s := newShellTestServer(t, false)
    s.initModelPolicyStore()
    token, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub:      "u-" + role,
        Roles:    []string{role},
        TenantID: tenant,
    })
    if err != nil {
        t.Fatalf("issue session: %v", err)
    }
    return s, token
}

func put606(s *Server, token, query, body string) *httptest.ResponseRecorder {
    req := httptest.NewRequest(http.MethodPut, "/v1/model-policy"+query, strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    ctx := adapter.WithTenant(req.Context(), "local")
    req = req.WithContext(ctx)
    w := httptest.NewRecorder()
    s.handleModelPolicy(w, req)
    return w
}

func Test606_TenantAdminCanPutOwnTenant(t *testing.T) {
    s, token := sso602Session(t, "admin", "community")
    w := put606(s, token, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("tenant_admin PUT own tenant must 200, got %d: %s", w.Code, w.Body.String())
    }
    if !s.modelPolicies.HasPolicy("community") {
        t.Fatal("policy for community must be written")
    }
}

func Test606_TenantAdminCannotWriteOtherTenant(t *testing.T) {
    s, token := sso602Session(t, "admin", "community")
    w := put606(s, token, "?tenant=finance", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusForbidden {
        t.Fatalf("cross-tenant PUT must 403, got %d: %s", w.Code, w.Body.String())
    }
    if s.modelPolicies.HasPolicy("finance") {
        t.Fatal("cross-tenant write must not land")
    }
}

func Test606_TenantAdminCannotWriteLocalOrRoot(t *testing.T) {
    s, token := sso602Session(t, "admin", "community")
    // Naming another namespace is a scope violation for a tenant-scoped admin.
    w := put606(s, token, "?tenant=local", `{"user_allowlist":["glm-5"]}`)
    if w.Code == http.StatusOK {
        t.Fatal("tenant_admin PUT ?tenant=local must not succeed")
    }
    if s.modelPolicies.HasPolicy("local") || s.modelPolicies.HasPolicy("") {
        t.Fatal("tenant_admin must never write local or root namespaces")
    }
    // An empty ?tenant= resolves to the session's own tenant — allowed.
    w = put606(s, token, "?tenant=", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("empty ?tenant= must resolve to own tenant, got %d: %s", w.Code, w.Body.String())
    }
    if !s.modelPolicies.HasPolicy("community") {
        t.Fatal("empty ?tenant= must write the community namespace")
    }
}

func Test606_MemberRoleStays403(t *testing.T) {
    s, token := sso602Session(t, "viewer", "community")
    w := put606(s, token, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusForbidden {
        t.Fatalf("member-role PUT must 403, got %d: %s", w.Code, w.Body.String())
    }
    if s.modelPolicies.HasPolicy("community") {
        t.Fatal("member role must not write policy")
    }
}

func Test606_TenantAdminCannotAdminLocalSession(t *testing.T) {
    // A shell-store admin session is pinned to tenant "local" by the
    // session config — the tenant-admin bridge must NOT elevate it (local
    // stays full-admin-only), but a full shell-store admin still bridges.
    s := newShellTestServer(t, false)
    s.initModelPolicyStore()
    token, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub:      "localadmin",
        Roles:    []string{"admin"},
        TenantID: "local",
    })
    if err != nil {
        t.Fatalf("issue: %v", err)
    }
    req := httptest.NewRequest(http.MethodPut, "/v1/model-policy", strings.NewReader(`{"user_allowlist":["glm-5"]}`))
    req.Header.Set("Authorization", "Bearer "+token)
    ctx := adapter.WithTenant(req.Context(), "local")
    req = req.WithContext(ctx)
    w := httptest.NewRecorder()
    s.handleModelPolicy(w, req)
    // Local shell user is NOT in the shell user store of this fixture
    // (newShellTestServer without bootstrap) — so no bridge applies at all.
    if w.Code != http.StatusForbidden {
        t.Fatalf("unstored local session must 403, got %d", w.Code)
    }
    var _ = middleware.RoleTenantAdmin
}
