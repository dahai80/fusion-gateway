package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// #487/#491: shell-session-authenticated modules read the model-policy
// chain behind the existing withShellSession gate — resolved model, the
// effective allowlist, and privacy_mode. local_only hides cloud models
// from the allowlist and the resolution alike.
func TestShellModelPolicy_ResolveAndGate(t *testing.T) {
    s := newShellTestServer(t, false)
    s.modelPolicies = modelpolicy.NewStore()
    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{
        DefaultModel:    "mlx/tenant-default",
        ModuleOverrides: map[string]string{"fusion-code": "mlx/coder"},
        UserAllowlist:   []string{"mlx/coder", "cloud/big"},
        PrivacyMode:     modelpolicy.PrivacyModeAll,
    }); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    _, resp := loginShell(t, s, "alice", "alicepw")
    token, _ := resp["token"].(string)
    if token == "" {
        t.Fatalf("no session token: %v", resp)
    }
    call := func(target string) (int, map[string]any) {
        t.Helper()
        req := httptest.NewRequest(http.MethodGet, target, nil)
        req.Header.Set("Authorization", "Bearer "+token)
        w := httptest.NewRecorder()
        s.withShellSession(s.handleShellModelPolicy)(w, req)
        var out map[string]any
        _ = json.Unmarshal(w.Body.Bytes(), &out)
        return w.Code, out
    }
    // Module override wins for the overridden module.
    code, out := call("/api/v1/shell/model-policy?module=fusion-code")
    if code != http.StatusOK {
        t.Fatalf("code = %d body=%v", code, out)
    }
    if out["resolved_model"] != "mlx/coder" || out["source"] != "module_override" {
        t.Fatalf("override must win, got %v", out)
    }
    if out["tenant"] != "local" || out["user"] != "alice" {
        t.Fatalf("tenant/user must come from the session, got %v", out)
    }
    // Allowlist and privacy_mode are surfaced unfiltered under mode=all.
    al, _ := out["allowlist"].([]any)
    if len(al) != 2 {
        t.Fatalf("allowlist must list both models under mode=all, got %v", out["allowlist"])
    }
    if out["privacy_mode"] != "all" {
        t.Fatalf("privacy_mode = %v", out["privacy_mode"])
    }
    // No override + no pref for the module: tenant default.
    _, out = call("/api/v1/shell/model-policy?module=fusion-doc")
    if out["resolved_model"] != "mlx/tenant-default" || out["source"] != "tenant_default" {
        t.Fatalf("tenant default must apply, got %v", out)
    }
}

// #487 AC: privacy_mode=local_only hides cloud models — the allowlist is
// filtered to local models and the resolution never returns a cloud model.
func TestShellModelPolicy_PrivacyHidesCloud(t *testing.T) {
    s := newShellTestServer(t, false)
    s.modelPolicies = modelpolicy.NewStore()
    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{
        DefaultModel:  "mlx/local",
        UserAllowlist: []string{"mlx/local", "cloud/big"},
        PrivacyMode:   modelpolicy.PrivacyModeLocalOnly,
    }); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    e := router.NewEngine(cfg, hardware.NewCollector(&cfg.Config.Hardware))
    e.SetLocalModels(func() map[string]bool { return map[string]bool{"mlx/local": true} })
    s.router = e
    _, resp := loginShell(t, s, "alice", "alicepw")
    token, _ := resp["token"].(string)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/model-policy?module=fusion-doc", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withShellSession(s.handleShellModelPolicy)(w, req)
    var out map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &out)
    if out["resolved_model"] != "mlx/local" || out["privacy_mode"] != "local_only" {
        t.Fatalf("local resolution under the pin, got %v", out)
    }
    al, _ := out["allowlist"].([]any)
    if len(al) != 1 || al[0] != "mlx/local" {
        t.Fatalf("cloud model must be hidden from the allowlist, got %v", out["allowlist"])
    }
}

// #491: the /api/v1/model-policy alias serves the same payload for
// consumers that poll the non-sh-prefixed path (best-effort, fail-open).
func TestShellModelPolicy_AliasRoute(t *testing.T) {
    s := newShellTestServer(t, false)
    s.modelPolicies = modelpolicy.NewStore()
    _, resp := loginShell(t, s, "alice", "alicepw")
    token, _ := resp["token"].(string)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/model-policy?module=fusion-doc", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withShellSession(s.handleShellModelPolicy)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("alias must serve the same payload, got %d body=%s", w.Code, w.Body.String())
    }
    var out map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &out)
    if _, ok := out["resolved_model"]; !ok {
        t.Fatalf("alias payload missing resolved_model: %v", out)
    }
}
