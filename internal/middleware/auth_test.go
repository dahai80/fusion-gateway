package middleware

import (
    "context"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestAPIKeyAuth_Disabled(t *testing.T) {
    slog.Info("test APIKeyAuth_Disabled")
    cfg := &config.AuthConfig{Enabled: false}
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestAPIKeyAuth_Passthrough(t *testing.T) {
    slog.Info("test APIKeyAuth_Passthrough")
    cfg := &config.AuthConfig{Enabled: true, Passthrough: true}
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
    slog.Info("test APIKeyAuth_MissingKey")
    cfg := &config.AuthConfig{Enabled: true}
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401, got %d", rec.Code)
    }
}

func TestAPIKeyAuth_MasterKey(t *testing.T) {
    slog.Info("test APIKeyAuth_MasterKey")
    cfg := &config.AuthConfig{
        Enabled:   true,
        MasterKey: "master-secret",
    }
    var called bool
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        p := PrincipalFromContext(r.Context())
        if p == nil || !p.IsMaster {
            t.Error("expected master principal")
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer master-secret")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("next handler not called")
    }
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestAPIKeyAuth_ValidAPIKey(t *testing.T) {
    slog.Info("test APIKeyAuth_ValidAPIKey")
    cfg := &config.AuthConfig{
        Enabled: true,
        APIKeys: []config.AuthKeyConfig{
            {Key: "key1", Name: "test-key"},
        },
    }
    var called bool
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        p := PrincipalFromContext(r.Context())
        if p == nil || p.KeyConfig == nil || p.KeyConfig.Name != "test-key" {
            t.Error("expected key config in principal")
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer key1")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("next handler not called")
    }
}

func TestAPIKeyAuth_InvalidAPIKey(t *testing.T) {
    slog.Info("test APIKeyAuth_InvalidAPIKey")
    cfg := &config.AuthConfig{
        Enabled: true,
        APIKeys: []config.AuthKeyConfig{
            {Key: "key1", Name: "test-key"},
        },
    }
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer wrong-key")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401, got %d", rec.Code)
    }
}

func TestAPIKeyAuth_ExpiredKey(t *testing.T) {
    slog.Info("test APIKeyAuth_ExpiredKey")
    past := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
    cfg := &config.AuthConfig{
        Enabled: true,
        APIKeys: []config.AuthKeyConfig{
            {Key: "expired-key", Name: "expired", ExpiresAt: past},
        },
    }
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer expired-key")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401 for expired key, got %d", rec.Code)
    }
}

func TestAPIKeyAuth_XAPIKeyHeader(t *testing.T) {
    slog.Info("test APIKeyAuth_XAPIKeyHeader")
    cfg := &config.AuthConfig{
        Enabled: true,
        APIKeys: []config.AuthKeyConfig{
            {Key: "key1", Name: "test-key"},
        },
    }
    var called bool
    handler := APIKeyAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("x-api-key", "key1")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatal("next handler not called with x-api-key")
    }
}

func TestCheckModelAllowlist_MasterKey(t *testing.T) {
    slog.Info("test CheckModelAllowlist_MasterKey")
    p := &Principal{IsMaster: true}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "any-model") {
        t.Error("master key should allow all models")
    }
}

func TestCheckModelAllowlist_NoPrincipal(t *testing.T) {
    slog.Info("test CheckModelAllowlist_NoPrincipal")
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    if !CheckModelAllowlist(req, "any-model") {
        t.Error("no principal should allow all models")
    }
}

func TestCheckModelAllowlist_NoKeyConfig(t *testing.T) {
    slog.Info("test CheckModelAllowlist_NoKeyConfig")
    p := &Principal{}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "any-model") {
        t.Error("no key config should allow all models")
    }
}

func TestCheckModelAllowlist_EmptyAllowedModels(t *testing.T) {
    slog.Info("test CheckModelAllowlist_EmptyAllowedModels")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "test"}}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "any-model") {
        t.Error("empty allowed models should allow all models")
    }
}

func TestCheckModelAllowlist_ExactMatch(t *testing.T) {
    slog.Info("test CheckModelAllowlist_ExactMatch")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{
        Name:          "test",
        AllowedModels: []string{"gpt-4", "claude-3"},
    }}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "gpt-4") {
        t.Error("should allow exact match")
    }
    if CheckModelAllowlist(req, "other-model") {
        t.Error("should deny non-matching model")
    }
}

func TestCheckModelAllowlist_Wildcard(t *testing.T) {
    slog.Info("test CheckModelAllowlist_Wildcard")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{
        Name:          "test",
        AllowedModels: []string{"*"},
    }}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "any-model") {
        t.Error("wildcard should allow all models")
    }
}

func TestCheckModelAllowlist_PrefixWildcard(t *testing.T) {
    slog.Info("test CheckModelAllowlist_PrefixWildcard")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{
        Name:          "test",
        AllowedModels: []string{"gpt-*"},
    }}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "gpt-4") {
        t.Error("prefix wildcard should allow gpt-4")
    }
    if !CheckModelAllowlist(req, "gpt-3.5-turbo") {
        t.Error("prefix wildcard should allow gpt-3.5-turbo")
    }
    if CheckModelAllowlist(req, "claude-3") {
        t.Error("prefix wildcard should deny claude-3")
    }
}

func TestExtractAPIKey_Bearer(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    req.Header.Set("Authorization", "Bearer mykey")
    if key := extractAPIKey(req); key != "mykey" {
        t.Errorf("expected mykey, got %s", key)
    }
}

func TestExtractAPIKey_XAPIKey(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    req.Header.Set("x-api-key", "mykey")
    if key := extractAPIKey(req); key != "mykey" {
        t.Errorf("expected mykey, got %s", key)
    }
}

func TestExtractAPIKey_None(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    if key := extractAPIKey(req); key != "" {
        t.Errorf("expected empty, got %s", key)
    }
}
