package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// #606 reopen: the handler-level bridge (bridgeIdentityTenantAdmin) is
// unreachable on the real HTTP path — withPrincipalBridge fails to stamp a
// principal for an identity SSO session (its sub is not in the local
// shellUsers store), so the API-key middleware 401s the request before
// handleModelPolicy ever runs. These tests drive the full composite the
// mux registers (withPrincipalBridge + withMiddleware + handler), which the
// handler-only tests in modelpolicy_606_test.go bypass.

func ssoChainTestServer(t *testing.T) *Server {
    t.Helper()
    s := newShellTestServer(t, false)
    s.initModelPolicyStore()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Auth.Passthrough = false
    s.cfg.Config.Auth.MasterKey = "chain-test-master-key"
    // newShellTestServer builds a bare Server without the OIDC authenticator
    // the real composition root always constructs; a nil receiver panics
    // inside the middleware chain. A zero value = disabled = passthrough.
    s.oidcAuth = &middleware.OIDCAuthenticator{}
    s.buildMiddlewareChain()
    return s
}

func ssoChainSession(t *testing.T, s *Server, sub, origin, role, tenant string) string {
    t.Helper()
    token, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub:      sub,
        Roles:    []string{role},
        Origin:   origin,
        TenantID: tenant,
    })
    if err != nil {
        t.Fatalf("issue session: %v", err)
    }
    return token
}

func putThroughChain(s *Server, token, query, body string) *httptest.ResponseRecorder {
    req := httptest.NewRequest(http.MethodPut, "/v1/model-policy"+query, strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleModelPolicy)(w, req)
    return w
}

func Test606Chain_IdentityTenantAdmin_PUT_OwnTenant(t *testing.T) {
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "u-9f2", shell.OriginIdentity, "admin", "community")
    w := putThroughChain(s, tok, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("identity tenant_admin PUT own tenant must 200 through the chain, got %d body=%s", w.Code, w.Body.String())
    }
    if !s.modelPolicies.HasPolicy("community") {
        t.Fatal("policy must be written to the community namespace")
    }
}

func Test606Chain_IdentityTenantAdmin_CrossTenant_403(t *testing.T) {
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "u-9f2", shell.OriginIdentity, "admin", "community")
    w := putThroughChain(s, tok, "?tenant=finance", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusForbidden {
        t.Fatalf("cross-tenant PUT through the chain must 403, got %d body=%s", w.Code, w.Body.String())
    }
    if s.modelPolicies.HasPolicy("finance") {
        t.Fatal("cross-tenant write must not land")
    }
}

func Test606Chain_IdentityMember_403(t *testing.T) {
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "u-member", shell.OriginIdentity, "viewer", "community")
    w := putThroughChain(s, tok, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code == http.StatusOK {
        t.Fatal("member-role session must never write policy")
    }
    if s.modelPolicies.HasPolicy("community") {
        t.Fatal("member role must not write policy")
    }
}

func Test606Chain_LocalUserSession_NoElevation(t *testing.T) {
    // A local password session for a non-admin store account ("alice") must
    // stay unbridged — the identity rules apply only to identity-origin or
    // unknown-sub sessions.
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "alice", shell.OriginLocal, "viewer", "local")
    w := putThroughChain(s, tok, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code == http.StatusOK {
        t.Fatal("local non-admin session must not write root policy")
    }
    if s.modelPolicies.HasPolicy("local") || s.modelPolicies.HasPolicy("") {
        t.Fatal("local non-admin session must not write any policy")
    }
}

func Test606Chain_IdentitySubCollision_NoFullAdmin(t *testing.T) {
    // An identity tenant_admin whose sub collides with a LOCAL admin
    // account must NOT inherit the local account's full-admin elevation.
    // It stays tenant-scoped: own tenant OK, local/foreign tenants denied.
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "admin", shell.OriginIdentity, "admin", "community")
    w := putThroughChain(s, tok, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("identity session with colliding sub must still write own tenant, got %d body=%s", w.Code, w.Body.String())
    }
    if s.modelPolicies.HasPolicy("") {
        t.Fatal("colliding identity session must write its own tenant, not the root namespace")
    }
    w = putThroughChain(s, tok, "?tenant=local", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusForbidden {
        t.Fatalf("colliding identity session must not administer local, got %d body=%s", w.Code, w.Body.String())
    }
}

func Test606Chain_LegacyToken_UnknownSub_TenantAdmin(t *testing.T) {
    // Sessions issued before the origin field existed carry Origin "": they
    // are classified by store lookup — an unknown sub with an admin-class
    // role and a non-local tenant still gets the tenant-scoped bridge.
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "u-legacy", "", "admin", "community")
    w := putThroughChain(s, tok, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("legacy unknown-sub tenant admin must 200, got %d body=%s", w.Code, w.Body.String())
    }
    if !s.modelPolicies.HasPolicy("community") {
        t.Fatal("legacy session policy must land in its own tenant")
    }
}

func Test606Chain_LegacyToken_LocalTenant_FailClosed(t *testing.T) {
    // A legacy token pinned to the local tenant with an admin role must NOT
    // elevate — the local namespace stays full-admin-only.
    s := ssoChainTestServer(t)
    tok := ssoChainSession(t, s, "u-ghost", "", "admin", "local")
    w := putThroughChain(s, tok, "", `{"user_allowlist":["glm-5"]}`)
    if w.Code == http.StatusOK {
        t.Fatal("legacy local-tenant admin-class session must not write the root namespace")
    }
    if s.modelPolicies.HasPolicy("") || s.modelPolicies.HasPolicy("local") {
        t.Fatal("legacy local-tenant session must not write policy")
    }
}
