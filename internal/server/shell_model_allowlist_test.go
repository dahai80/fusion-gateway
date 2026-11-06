package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// Issue #11: shell-session chat must enforce the tenant model-policy
// user_allowlist (and privacy_mode local_only). CheckModelAllowlist never
// applies to sessions because they carry no API key, so without this gate a
// shell user could call chat with an arbitrary model no matter what the
// model picker shows.
func TestShellChatCompletions_TenantModelPolicyEnforced(t *testing.T) {
    s := newTestServer()
    s.modelPolicies = modelpolicy.NewStore()
    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{
        DefaultModel:  "local-small",
        UserAllowlist: []string{"local-small", "mlx-community/Qwen3.5-4B-MLX-4bit"},
    }); err != nil {
        t.Fatalf("SetPolicy: %v", err)
    }
    ctx := config.WithSnapshot(context.Background(), s.cfg)
    ctx = withShellClaims(ctx, shell.SessionClaims{Sub: "user-1", TenantID: "local"})

    post := func(model string) int {
        t.Helper()
        body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":false}`
        req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/chat/completions", strings.NewReader(body))
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        s.handleChatCompletions(rec, req)
        return rec.Code
    }

    if code := post("cloud/glm-5"); code != http.StatusForbidden {
        t.Fatalf("expected 403 for a model outside the tenant allowlist, got %d", code)
    }
    if code := post("local-small"); code == http.StatusForbidden {
        t.Fatalf("allowlisted model must pass the policy gate, got 403")
    }
}

// A tenant without a user_allowlist stays unrestricted (issue #11: the
// allowlist is opt-in), and local_only privacy blocks cloud models even
// when the allowlist is empty.
func TestShellChatCompletions_TenantModelPolicyDefaults(t *testing.T) {
    s := newTestServer()
    s.modelPolicies = modelpolicy.NewStore()
    ctx := config.WithSnapshot(context.Background(), s.cfg)
    ctx = withShellClaims(ctx, shell.SessionClaims{Sub: "user-1", TenantID: "local"})

    post := func(model string) int {
        t.Helper()
        body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"stream":false}`
        req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/chat/completions", strings.NewReader(body))
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        s.handleChatCompletions(rec, req)
        return rec.Code
    }

    if code := post("cloud/glm-5"); code == http.StatusForbidden {
        t.Fatalf("no allowlist configured: model must not be rejected by the policy gate")
    }

    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{PrivacyMode: modelpolicy.PrivacyModeLocalOnly}); err != nil {
        t.Fatalf("SetPolicy: %v", err)
    }
    if code := post("cloud/glm-5"); code != http.StatusForbidden {
        t.Fatalf("expected 403 for a cloud model under local_only privacy, got %d", code)
    }
}

// A policy PUT through the shell-session admin bridge must land under the
// session's tenant, not "": handleShellModelPolicy and the chat policy gate
// resolve claims.TenantID, so a policy written under the empty tenant was
// invisible to the entire shell plane.
func TestModelPolicyPut_ShellSessionBridgeWritesSessionTenant(t *testing.T) {
    s := newTestServer()
    s.modelPolicies = modelpolicy.NewStore()
    s.shellSessions = shell.NewManager([]byte("test-secret"), time.Hour)
    s.shellUsers = shell.NewUserStore("")
    if err := s.shellUsers.SeedFromConfig([]shell.UserAccount{{Username: "admin", Roles: []string{"admin"}}}); err != nil {
        t.Fatalf("SeedFromConfig: %v", err)
    }
    token, err := s.shellSessions.Issue(shell.SessionClaims{Sub: "admin", TenantID: "local", Roles: []string{"admin"}})
    if err != nil {
        t.Fatalf("Issue: %v", err)
    }

    body := `{"default_model":"local-small","user_allowlist":["local-small"]}`
    req := httptest.NewRequest(http.MethodPut, "/v1/model-policy", strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    rec := httptest.NewRecorder()
    s.handleModelPolicy(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("PUT /v1/model-policy: got %d: %s", rec.Code, rec.Body.String())
    }
    p := s.modelPolicies.Policy("local")
    if len(p.UserAllowlist) != 1 || p.UserAllowlist[0] != "local-small" {
        t.Fatalf("policy stored under wrong tenant: %+v", p)
    }
}

// Issue #14 companion: an empty model on a shell-session chat must backfill
// from the tenant policy chain, not the global routing default — the global
// default is invisible to the tenant allowlist and would 403 on the policy
// gate even though the tenant has a valid default model.
func TestShellChatCompletions_EmptyModelBackfillsTenantDefault(t *testing.T) {
    s := newTestServer()
    s.modelPolicies = modelpolicy.NewStore()
    s.cfg.Config.Routing.DefaultModel = "global-default"
    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{
        DefaultModel:  "tenant-default",
        UserAllowlist: []string{"tenant-default"},
    }); err != nil {
        t.Fatalf("SetPolicy: %v", err)
    }
    ctx := config.WithSnapshot(context.Background(), s.cfg)
    ctx = withShellClaims(ctx, shell.SessionClaims{Sub: "user-1", TenantID: "local"})

    body := `{"messages":[{"role":"user","content":"hi"}],"stream":false}`
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/chat/completions", strings.NewReader(body))
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req.WithContext(ctx))
    if rec.Code == http.StatusForbidden {
        t.Fatalf("empty model must backfill from the tenant policy default, got 403: %s", rec.Body.String())
    }
}
