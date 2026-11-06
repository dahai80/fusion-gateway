package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
)

// #530: a module presenting its own gateway-issued fm- service key on
// /shell/<module>/* must authenticate — the backend accepts the key direct,
// so the gateway hop must not be stricter than the module behind it. The
// session plane (user identity) is unchanged.

func proxyAuthRequest(s *Server, path, token string) *httptest.ResponseRecorder {
    req := httptest.NewRequest(http.MethodGet, path, nil)
    if token != "" {
        req.Header.Set("Authorization", "Bearer "+token)
    }
    w := httptest.NewRecorder()
    s.withShellProxyAuth(s.shellProxy.ServeHTTP)(w, req)
    return w
}

// Full restart simulation: issue a key, persist, reboot the shell subsystem
// on the same data_dir, then present the persisted key on the proxy route.
func TestShellProxy_ModuleServiceKeyAuthenticatesAfterRestart(t *testing.T) {
    dir := t.TempDir()

    s1 := newShellTestServer(t, false)
    s1.cfg.Config.Store.DataDir = dir
    s1.setupShell()
    raw := selfHealRegister(t, s1, "fusion-demo")

    s2 := newShellTestServer(t, false)
    s2.cfg.Config.Store.DataDir = dir
    s2.setupShell()

    w := proxyAuthRequest(s2, "/shell/fusion-demo/api/runs", raw)
    if w.Code == http.StatusUnauthorized {
        t.Fatalf("persisted fm- key rejected on proxy route after restart: %s", w.Body.String())
    }
    if w.Code == http.StatusNotFound {
        t.Fatalf("request never reached the proxy (auth wrap misroute): %s", w.Body.String())
    }
}

func TestShellProxy_ModuleServiceKeyAuthenticatesWithoutRestart(t *testing.T) {
    s := newShellTestServer(t, false)
    raw := selfHealRegister(t, s, "fusion-demo")
    w := proxyAuthRequest(s, "/shell/fusion-demo/api/runs", raw)
    if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
        t.Fatalf("valid fm- key rejected on its own module route: %d %s", w.Code, w.Body.String())
    }
}

// A module's key grants only that module's routes — never a sibling's.
func TestShellProxy_ForeignModuleKeyRejected(t *testing.T) {
    s := newShellTestServer(t, false)
    raw := selfHealRegister(t, s, "fusion-demo")
    w := proxyAuthRequest(s, "/shell/fusion-other/api/runs", raw)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("foreign module key code = %d, want 401", w.Code)
    }
}

// A mismatched fm- plaintext (never issued) is a credential failure, not a
// session failure.
func TestShellProxy_BadServiceKeyRejected(t *testing.T) {
    s := newShellTestServer(t, false)
    selfHealRegister(t, s, "fusion-demo")
    w := proxyAuthRequest(s, "/shell/fusion-demo/api/runs", "fm-fusion-demo-00000000000000000000000000000000")
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("bad fm- key code = %d, want 401", w.Code)
    }
}

// Revoked keys never authenticate the proxy route.
func TestShellProxy_RevokedServiceKeyRejected(t *testing.T) {
    s := newShellTestServer(t, false)
    raw := selfHealRegister(t, s, "fusion-demo")
    if err := s.shellRegistry.RevokeServiceKey("fusion-demo"); err != nil {
        t.Fatal(err)
    }
    w := proxyAuthRequest(s, "/shell/fusion-demo/api/runs", raw)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("revoked key code = %d, want 401", w.Code)
    }
}

// Regression guard: the session plane keeps working through the new wrap,
// including the module-permission check.
func TestShellProxy_SessionPlaneUnchanged(t *testing.T) {
    s := newShellTestServer(t, false)
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)

    w := proxyAuthRequest(s, "/shell/fusion-mlx/api/models", token)
    if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
        t.Fatalf("session user rejected on permitted module: %d %s", w.Code, w.Body.String())
    }

    // alice's permissions do not include fusion-other — 403 module_forbidden.
    w = proxyAuthRequest(s, "/shell/fusion-other/api/runs", token)
    if w.Code != http.StatusForbidden {
        t.Fatalf("session module check code = %d, want 403", w.Code)
    }
    var env map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &env)
    if env["code"] != "module_forbidden" {
        t.Fatalf("code = %v, want module_forbidden", env["code"])
    }
}
