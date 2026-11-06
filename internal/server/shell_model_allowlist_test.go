package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

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
