package server

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
)

// Issue #598: tenant model policies must survive a gateway restart even
// when web_shell is disabled — the store was previously constructed only
// inside setupShellAdminSurfaces (shell-gated), so a restart re-broke every
// tenant's chat until an admin re-PUT the policy.

func put598Policy(t *testing.T, s *Server, tenant, body string) int {
    t.Helper()
    req := httptest.NewRequest(http.MethodPut, "/v1/model-policy", strings.NewReader(body))
    // Admin principal: the handler gates on IsAdmin before it touches the
    // store; the persistence behavior under test is behind that gate. The
    // tenant comes from the adapter context (gateway-derived, #150), not a
    // client header.
    ctx, p := middleware.EnsurePrincipal(req.Context())
    p.AuthMethod = "admin-jwt"
    p.Role = middleware.RoleAdmin
    ctx = adapter.WithTenant(ctx, tenant)
    req = req.WithContext(ctx)
    w := httptest.NewRecorder()
    s.handleModelPolicy(w, req)
    return w.Code
}

func Test598_PolicySurvivesRestartWithoutShell(t *testing.T) {
    dir := t.TempDir()
    mk := func() *Server {
        s := newTestServer()
        s.cfg.Config.Store.DataDir = dir
        s.initModelPolicyStore()
        return s
    }
    s1 := mk()
    if s1.modelPolicies == nil {
        t.Fatal("model policy store must exist without web_shell enabled")
    }
    if code := put598Policy(t, s1, "local", `{"default_model":"glm-5","user_allowlist":["glm-5"]}`); code != http.StatusOK {
        t.Fatalf("PUT policy: %d", code)
    }
    // Simulate a restart: brand-new server over the same data dir.
    s2 := mk()
    p := s2.modelPolicies.Policy("local")
    if p.DefaultModel != "glm-5" || len(p.UserAllowlist) != 1 {
        t.Fatalf("policy lost across restart: %+v", p)
    }
}

func Test598_BootWithoutFileStartsEmpty(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Store.DataDir = t.TempDir()
    s.initModelPolicyStore()
    if p := s.modelPolicies.Policy("local"); p.DefaultModel != "" || len(p.UserAllowlist) != 0 {
        t.Fatalf("fresh boot must start with empty policy: %+v", p)
    }
}

func Test598_CorruptFileFailsSafeWithEmptyStore(t *testing.T) {
    dir := t.TempDir()
    s := newTestServer()
    s.cfg.Config.Store.DataDir = dir
    s.initModelPolicyStore()
    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{DefaultModel: "glm-5", UserAllowlist: []string{"glm-5"}}); err != nil {
        t.Fatalf("seed policy: %v", err)
    }
    // Corrupt the file behind the store's back.
    if err := os.WriteFile(filepath.Join(dir, "model_policy.json"), []byte("{not json"), 0600); err != nil {
        t.Fatal(err)
    }
    s2 := newTestServer()
    s2.cfg.Config.Store.DataDir = dir
    s2.initModelPolicyStore()
    if p := s2.modelPolicies.Policy("local"); p.DefaultModel != "" {
        t.Fatalf("corrupt file must degrade to empty store, got %+v", p)
    }
}
