package server

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/admin"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
)

// #499 regression: the bridge must run BEFORE the auth middleware. These
// tests drive the full composite the mux registers (bridge wrapper +
// withMiddleware + handler), not the bare handler — the handler-only tests
// above masked the ordering bug.

func bridgeTestServer(t *testing.T) (*Server, string) {
    t.Helper()
    aa, err := admin.NewAdminAuth("test-admin-jwt-secret-32-characters!", map[string]string{"root": "pw"})
    if err != nil {
        t.Fatalf("admin auth: %v", err)
    }
    s := newTestServer()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Auth.Passthrough = false
    s.cfg.Config.Auth.MasterKey = "bridge-test-master-key"
    s.modelPolicies = modelpolicy.NewStore()
    s.adminAuth = aa
    s.buildMiddlewareChain()
    tok, err := aa.GenerateToken("root", "admin")
    if err != nil {
        t.Fatalf("generate token: %v", err)
    }
    return s, tok
}

// #499 AC: GET /v1/model-policy with an admin-login JWT returns 200 through
// the full middleware chain.
func TestModelPolicy_AdminJWT_FullChain_GET(t *testing.T) {
    s, tok := bridgeTestServer(t)
    r := httptest.NewRequest(http.MethodGet, "/v1/model-policy", nil)
    r.Header.Set("Authorization", "Bearer "+tok)
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleModelPolicy)(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("admin JWT GET must be 200 through the chain, got %d body=%s", w.Code, w.Body.String())
    }
    var out map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &out)
    if _, ok := out["policy"]; !ok {
        t.Fatalf("policy JSON missing: %s", w.Body.String())
    }
}

// #499 AC: PUT with an admin-login JWT mutates policy through the full chain.
func TestModelPolicy_AdminJWT_FullChain_PUT(t *testing.T) {
    s, tok := bridgeTestServer(t)
    body, _ := json.Marshal(modelpolicy.Policy{
        DefaultModel: "mlx/bridge",
        PrivacyMode:  modelpolicy.PrivacyModeAll,
    })
    r := httptest.NewRequest(http.MethodPut, "/v1/model-policy", bytes.NewReader(body))
    r.Header.Set("Authorization", "Bearer "+tok)
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleModelPolicy)(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("admin JWT PUT must be 200 through the chain, got %d body=%s", w.Code, w.Body.String())
    }
    got := s.modelPolicies.Policy("").DefaultModel
    if got != "mlx/bridge" {
        t.Fatalf("PUT must mutate the policy, got default_model %q", got)
    }
}

// #499 AC: non-admin principals still rejected 403 through the full chain.
func TestModelPolicy_NonAdmin_FullChain(t *testing.T) {
    s, _ := bridgeTestServer(t)
    s.cfg.Config.Auth.APIKeys = []config.AuthKeyConfig{{Key: "plain-key", Name: "plain"}}
    s.buildMiddlewareChain()
    r := httptest.NewRequest(http.MethodGet, "/v1/model-policy", nil)
    r.Header.Set("Authorization", "Bearer plain-key")
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleModelPolicy)(w, r)
    if w.Code != http.StatusForbidden {
        t.Fatalf("plain API key must be 403, got %d body=%s", w.Code, w.Body.String())
    }
    // Master key: RR3 — inference plane, never admin.
    r2 := httptest.NewRequest(http.MethodGet, "/v1/model-policy", nil)
    r2.Header.Set("Authorization", "Bearer bridge-test-master-key")
    w2 := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleModelPolicy)(w2, r2)
    if w2.Code != http.StatusForbidden {
        t.Fatalf("master key must stay 403 (RR3), got %d", w2.Code)
    }
}

// #499: /v1/model-preference resolves with an admin JWT through the full
// chain (the dead shell-session admin bridge ordering).
func TestModelPreference_AdminJWT_FullChain(t *testing.T) {
    s, tok := bridgeTestServer(t)
    r := httptest.NewRequest(http.MethodGet, "/v1/model-preference?module=fusion-code", nil)
    r.Header.Set("Authorization", "Bearer "+tok)
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleModelPreference)(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("admin JWT preference GET must be 200 through the chain, got %d body=%s", w.Code, w.Body.String())
    }
}
