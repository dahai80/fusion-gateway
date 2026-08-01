package middleware

import (
    "context"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/golang-jwt/jwt/v5"
)

func TestRBACAuth_Disabled(t *testing.T) {
    cfg := &config.RBACConfig{Enabled: false}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("should call next when disabled")
    }
}

func TestRBACAuth_ViewerBlockedOnMutation(t *testing.T) {
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("viewer should not reach handler on POST")
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403, got %d", rec.Code)
    }
}

func TestRBACAuth_ViewerAllowedOnGet(t *testing.T) {
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("viewer should be allowed on GET")
    }
}

func TestRBACAuth_AdminCanMutate(t *testing.T) {
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "admin"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("admin should be allowed on POST")
    }
}

func TestRBACAuth_MasterKeyGetsAdmin(t *testing.T) {
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{IsMaster: true})
    req = req.WithContext(ctx)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("master key should get admin role")
    }
}

func TestGetRBACRole(t *testing.T) {
    ctx := ContextWithPrincipal(context.Background(), &Principal{Role: RoleAdmin})
    if GetRBACRole(ctx) != RoleAdmin {
        t.Fatal("expected admin role")
    }
    if GetRBACRole(context.Background()) != RoleViewer {
        t.Fatal("default should be viewer")
    }
}

func TestIsAdmin(t *testing.T) {
    ctx := ContextWithPrincipal(context.Background(), &Principal{Role: RoleAdmin})
    if !IsAdmin(ctx) {
        t.Fatal("should be admin")
    }
    ctx2 := ContextWithPrincipal(context.Background(), &Principal{Role: RoleViewer})
    if IsAdmin(ctx2) {
        t.Fatal("should not be admin")
    }
}

func TestCanWrite(t *testing.T) {
    if !CanWrite(ContextWithPrincipal(context.Background(), &Principal{Role: RoleAdmin})) {
        t.Fatal("admin can write")
    }
    if !CanWrite(ContextWithPrincipal(context.Background(), &Principal{Role: RoleEditor})) {
        t.Fatal("editor can write")
    }
    if CanWrite(ContextWithPrincipal(context.Background(), &Principal{Role: RoleViewer})) {
        t.Fatal("viewer cannot write")
    }
}

func TestRBACAuth_OIDCRoleClaim(t *testing.T) {
    slog.Info("test RBACAuth_OIDCRoleClaim")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Role == RoleAdmin {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx, _ := EnsurePrincipal(req.Context())
    ctx = ContextWithPrincipal(ctx, &Principal{
        OIDCClaims: jwt.MapClaims{"role": "admin"},
    })
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("OIDC role claim admin should allow POST")
    }
}

func TestRBACAuth_OIDCGroupsAdmin(t *testing.T) {
    slog.Info("test RBACAuth_OIDCGroupsAdmin")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Role == RoleAdmin {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"groups": []interface{}{"fusion-admin"}},
    })
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("fusion-admin group should grant admin role")
    }
}

func TestRBACAuth_OIDCGroupsAdmins(t *testing.T) {
    slog.Info("test RBACAuth_OIDCGroupsAdmins")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Role == RoleAdmin {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"groups": []interface{}{"admins"}},
    })
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("admins group should grant admin role")
    }
}

func TestRBACAuth_OIDCGroupsEditor(t *testing.T) {
    slog.Info("test RBACAuth_OIDCGroupsEditor")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Role == RoleEditor {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"groups": []interface{}{"fusion-editor"}},
    })
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("fusion-editor group should grant editor role")
    }
}

func TestRBACAuth_OIDCGroupsEditors(t *testing.T) {
    slog.Info("test RBACAuth_OIDCGroupsEditors")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Role == RoleEditor {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"groups": []interface{}{"editors"}},
    })
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("editors group should grant editor role")
    }
}

func TestRBACAuth_OIDCInvalidRole(t *testing.T) {
    slog.Info("test RBACAuth_OIDCInvalidRole")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("viewer with invalid OIDC role should be blocked on POST")
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"role": "superuser"},
    })
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403 for invalid OIDC role, got %d", rec.Code)
    }
}

func TestRBACAuth_TeamAssignment(t *testing.T) {
    slog.Info("test RBACAuth_TeamAssignment")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "admin"}
    teamCfg := &config.TeamConfig{Enabled: true, DefaultTeam: "default-team"}
    called := false
    handler := RBACAuth(cfg, teamCfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Team != nil && p.Team.ID == "default-team" {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("default team should be assigned")
    }
}

func TestRBACAuth_TeamFromOIDCClaim(t *testing.T) {
    slog.Info("test RBACAuth_TeamFromOIDCClaim")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "admin"}
    teamCfg := &config.TeamConfig{Enabled: true, DefaultTeam: "default-team"}
    called := false
    handler := RBACAuth(cfg, teamCfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Team != nil && p.Team.ID == "custom-team" {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"team": "custom-team"},
    })
    req = req.WithContext(ctx)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("team from OIDC claim should override default")
    }
}

func TestRBACAuth_EditorCanMutate(t *testing.T) {
    slog.Info("test RBACAuth_EditorCanMutate")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "editor"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    req := httptest.NewRequest(http.MethodPut, "/v1/something", nil)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("editor should be allowed to PUT")
    }
}

func TestRBACAuth_ViewerBlockedOnPut(t *testing.T) {
    slog.Info("test RBACAuth_ViewerBlockedOnPut")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("viewer should not reach handler on PUT")
    }))
    req := httptest.NewRequest(http.MethodPut, "/v1/something", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403, got %d", rec.Code)
    }
}

func TestRBACAuth_ViewerBlockedOnDelete(t *testing.T) {
    slog.Info("test RBACAuth_ViewerBlockedOnDelete")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("viewer should not reach handler on DELETE")
    }))
    req := httptest.NewRequest(http.MethodDelete, "/v1/something", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403, got %d", rec.Code)
    }
}

func TestRBACAuth_ViewerBlockedOnPatch(t *testing.T) {
    slog.Info("test RBACAuth_ViewerBlockedOnPatch")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "viewer"}
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("viewer should not reach handler on PATCH")
    }))
    req := httptest.NewRequest(http.MethodPatch, "/v1/something", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403, got %d", rec.Code)
    }
}

func TestRBACAuth_NoTeamWhenDisabled(t *testing.T) {
    slog.Info("test RBACAuth_NoTeamWhenDisabled")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "admin"}
    teamCfg := &config.TeamConfig{Enabled: false, DefaultTeam: "default-team"}
    called := false
    handler := RBACAuth(cfg, teamCfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Team == nil {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("team should not be assigned when team disabled")
    }
}

func TestRBACAuth_OIDCGroupDoesNotOverrideAdmin(t *testing.T) {
    slog.Info("test RBACAuth_OIDCGroupDoesNotOverrideAdmin")
    cfg := &config.RBACConfig{Enabled: true, DefaultRole: "admin"}
    called := false
    handler := RBACAuth(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p.Role == RoleAdmin {
            called = true
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        OIDCClaims: jwt.MapClaims{"groups": []interface{}{"fusion-editor"}},
    })
    req = req.WithContext(ctx)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("editor group should not downgrade admin from OIDC role claim")
    }
}
