package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

func TestBridgeShellSession_AdminTokenAccepted(t *testing.T) {
    s := newShellTestServer(t, false)
    testMiddlewareChain(s)
    _, resp := loginShell(t, s, "admin", "s3cret")
    token := resp["token"].(string)
    handler := s.withAdminOnly(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true}`))
    })
    req := httptest.NewRequest(http.MethodGet, "/admin/api/teams", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    rr := httptest.NewRecorder()
    handler.ServeHTTP(rr, req)
    if rr.Code != http.StatusOK {
        t.Fatalf("admin shell token code = %d body=%s", rr.Code, rr.Body.String())
    }
}

func TestBridgeShellSession_NonAdminRejected(t *testing.T) {
    s := newShellTestServer(t, false)
    testMiddlewareChain(s)
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)
    handler := s.withAdminOnly(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/admin/api/teams", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    rr := httptest.NewRecorder()
    handler.ServeHTTP(rr, req)
    if rr.Code != http.StatusForbidden {
        t.Fatalf("non-admin shell token code = %d, want 403", rr.Code)
    }
}

func TestBridgeShellSession_DemotedAdminRejected(t *testing.T) {
    s := newShellTestServer(t, false)
    testMiddlewareChain(s)
    _, resp := loginShell(t, s, "admin", "s3cret")
    token := resp["token"].(string)
    if err := s.shellUsers.Update("admin", func(a *shell.UserAccount) {
        a.Roles = []string{"user"}
    }); err != nil {
        t.Fatalf("demote: %v", err)
    }
    handler := s.withAdminOnly(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/admin/api/teams", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    rr := httptest.NewRecorder()
    handler.ServeHTTP(rr, req)
    if rr.Code != http.StatusForbidden {
        t.Fatalf("demoted admin shell token code = %d, want 403", rr.Code)
    }
}

func TestBridgeShellSession_GarbageBearerNotPromoted(t *testing.T) {
    s := newShellTestServer(t, false)
    testMiddlewareChain(s)
    handler := s.withAdminOnly(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    for _, tok := range []string{"garbage", "a.b", "not-a-token"} {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/teams", nil)
        req.Header.Set("Authorization", "Bearer "+tok)
        rr := httptest.NewRecorder()
        handler.ServeHTTP(rr, req)
        if rr.Code != http.StatusForbidden {
            t.Fatalf("token %q code = %d, want 403", tok, rr.Code)
        }
    }
}

func TestAutoRegister_AdminManifest(t *testing.T) {
    s := newShellTestServer(t, false)
    views := s.shellRegistry.List()
    var admin *shell.ManifestView
    for i := range views {
        if views[i].ID == "fusion-admin" {
            admin = &views[i]
        }
    }
    if admin == nil {
        t.Fatalf("fusion-admin not auto-registered; views = %+v", views)
    }
    if admin.Category != "infra" || admin.Backend != "gateway" {
        t.Fatalf("admin manifest = %+v", admin.ShellManifest)
    }
    if !strings.HasPrefix(admin.Entry, "/") {
        t.Fatalf("entry = %s, want relative /admin/ path (same-origin)", admin.Entry)
    }
}

func TestAutoRegister_NoAllowlist_Skips(t *testing.T) {
    cfg := newShellCfg(t)
    cfg.Config.Shell.EntryOriginAllowlist = nil
    s := &Server{cfg: cfg}
    s.setupShell()
    if s.shellRegistry == nil {
        t.Fatal("registry expected")
    }
    for _, v := range s.shellRegistry.List() {
        if v.ID == "fusion-admin" {
            t.Fatal("auto-register must skip when allowlist empty (A7 fail-closed)")
        }
    }
}

// testMiddlewareChain installs a pass-through middleware chain so
// withAdminOnly's withMiddleware call works on a bare test Server.
func testMiddlewareChain(s *Server) {
    s.middlewareChainMu.Lock()
    s.middlewareChain = func(next http.Handler) http.Handler { return next }
    s.middlewareChainMu.Unlock()
}
