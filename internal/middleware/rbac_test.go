package middleware

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
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
    ctx := context.WithValue(req.Context(), IsMasterKeyKey, true)
    req = req.WithContext(ctx)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatal("master key should get admin role")
    }
}

func TestGetRBACRole(t *testing.T) {
    ctx := context.WithValue(context.Background(), RBACRoleKey, RoleAdmin)
    if GetRBACRole(ctx) != RoleAdmin {
        t.Fatal("expected admin role")
    }
    if GetRBACRole(context.Background()) != RoleViewer {
        t.Fatal("default should be viewer")
    }
}

func TestIsAdmin(t *testing.T) {
    ctx := context.WithValue(context.Background(), RBACRoleKey, RoleAdmin)
    if !IsAdmin(ctx) {
        t.Fatal("should be admin")
    }
    ctx2 := context.WithValue(context.Background(), RBACRoleKey, RoleViewer)
    if IsAdmin(ctx2) {
        t.Fatal("should not be admin")
    }
}

func TestCanWrite(t *testing.T) {
    if !CanWrite(context.WithValue(context.Background(), RBACRoleKey, RoleAdmin)) {
        t.Fatal("admin can write")
    }
    if !CanWrite(context.WithValue(context.Background(), RBACRoleKey, RoleEditor)) {
        t.Fatal("editor can write")
    }
    if CanWrite(context.WithValue(context.Background(), RBACRoleKey, RoleViewer)) {
        t.Fatal("viewer cannot write")
    }
}
