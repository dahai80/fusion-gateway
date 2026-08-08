package middleware

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
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

func TestCheckModelAllowlist_CaseInsensitive(t *testing.T) {
    slog.Info("test CheckModelAllowlist_CaseInsensitive")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{
        Name:          "test",
        AllowedModels: []string{"qwen*"},
    }}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "Qwen3.5-9B-4bit") {
        t.Error("case-insensitive prefix wildcard should allow Qwen3.5-9B-4bit for qwen*")
    }
    if !CheckModelAllowlist(req, "QWEN2.5-7B") {
        t.Error("case-insensitive prefix wildcard should allow QWEN2.5-7B for qwen*")
    }
    if CheckModelAllowlist(req, "llama-3") {
        t.Error("prefix wildcard should deny llama-3")
    }
    exactP := &Principal{KeyConfig: &config.AuthKeyConfig{
        Name:          "test",
        AllowedModels: []string{"GPT-4"},
    }}
    ctx2 := ContextWithPrincipal(context.Background(), exactP)
    req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req2 = req2.WithContext(ctx2)
    if !CheckModelAllowlist(req2, "gpt-4") {
        t.Error("case-insensitive exact match should allow gpt-4 for GPT-4")
    }
}

func TestCheckModelAllowlist_MLXCommunityPrefix(t *testing.T) {
    slog.Info("test CheckModelAllowlist_MLXCommunityPrefix")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{
        Name:          "demo",
        AllowedModels: []string{"gpt-4o*", "claude-3*", "qwen*", "mlx-community--*"},
    }}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    if !CheckModelAllowlist(req, "mlx-community--Qwen2.5-VL-7B-Instruct-4bit") {
        t.Error("mlx-community--* should allow MLX VL model mlx-community--Qwen2.5-VL-7B-Instruct-4bit")
    }
    if !CheckModelAllowlist(req, "MLX-Community--Qwen2.5-VL-7B-Instruct-4bit") {
        t.Error("mlx-community--* should match case-insensitively for MLX VL model")
    }
    if CheckModelAllowlist(req, "llama-3") {
        t.Error("allowlist should deny llama-3 (no matching glob)")
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

func TestCheckModelModuleAccess_MasterKey(t *testing.T) {
    p := &Principal{IsMaster: true}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
    if !CheckModelModuleAccess(req, "chat") {
        t.Error("master key should allow any module")
    }
}

func TestCheckModelModuleAccess_NoPrincipal(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    if !CheckModelModuleAccess(req, "chat") {
        t.Error("no principal should allow access")
    }
}

func TestCheckModelModuleAccess_NoModules(t *testing.T) {
    p := &Principal{ModelModules: nil}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
    if !CheckModelModuleAccess(req, "chat") {
        t.Error("empty ModelModules should allow access")
    }
}

func TestCheckModelModuleAccess_Allowed(t *testing.T) {
    p := &Principal{ModelModules: []string{"chat", "code"}}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
    if !CheckModelModuleAccess(req, "chat") {
        t.Error("chat should be allowed")
    }
    if !CheckModelModuleAccess(req, "code") {
        t.Error("code should be allowed")
    }
}

func TestCheckModelModuleAccess_Denied(t *testing.T) {
    p := &Principal{ModelModules: []string{"chat"}}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
    if CheckModelModuleAccess(req, "agent") {
        t.Error("agent should be denied")
    }
}

func TestCheckModelModuleAccess_Wildcard(t *testing.T) {
    p := &Principal{ModelModules: []string{"*"}}
    ctx := ContextWithPrincipal(context.Background(), p)
    req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
    if !CheckModelModuleAccess(req, "agent") {
        t.Error("wildcard should allow any module")
    }
}

func TestValidModule(t *testing.T) {
    for _, m := range []string{"chat", "code", "design", "rag", "agent"} {
        if !ValidModule(m) {
            t.Errorf("%s should be valid", m)
        }
    }
    if ValidModule("invalid") {
        t.Error("invalid should not be valid module")
    }
}

func TestCheckBackendAccess_NilPrincipal(t *testing.T) {
    slog.Info("test CheckBackendAccess_NilPrincipal")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    if !CheckBackendAccess(req, "local") {
        t.Fatal("nil principal should allow all backends")
    }
}

func TestCheckBackendAccess_MasterKey(t *testing.T) {
    slog.Info("test CheckBackendAccess_MasterKey")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{IsMaster: true})
    req = req.WithContext(ctx)
    if !CheckBackendAccess(req, "cloud") {
        t.Fatal("master key should allow all backends")
    }
}

func TestCheckBackendAccess_NoRestriction(t *testing.T) {
    slog.Info("test CheckBackendAccess_NoRestriction")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        KeyConfig: &config.AuthKeyConfig{},
    })
    req = req.WithContext(ctx)
    if !CheckBackendAccess(req, "cloud") {
        t.Fatal("empty allowed_backends should allow all")
    }
}

func TestCheckBackendAccess_Allowed(t *testing.T) {
    slog.Info("test CheckBackendAccess_Allowed")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        KeyConfig: &config.AuthKeyConfig{AllowedBackends: []string{"local", "cloud"}},
    })
    req = req.WithContext(ctx)
    if !CheckBackendAccess(req, "local") {
        t.Fatal("local should be allowed")
    }
    if !CheckBackendAccess(req, "cloud") {
        t.Fatal("cloud should be allowed")
    }
}

func TestCheckBackendAccess_Denied(t *testing.T) {
    slog.Info("test CheckBackendAccess_Denied")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        KeyConfig: &config.AuthKeyConfig{AllowedBackends: []string{"local"}},
    })
    req = req.WithContext(ctx)
    if CheckBackendAccess(req, "cloud") {
        t.Fatal("cloud should be denied when only local allowed")
    }
}

func TestCheckBackendAccess_Wildcard(t *testing.T) {
    slog.Info("test CheckBackendAccess_Wildcard")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    ctx := ContextWithPrincipal(req.Context(), &Principal{
        KeyConfig: &config.AuthKeyConfig{AllowedBackends: []string{"*"}},
    })
    req = req.WithContext(ctx)
    if !CheckBackendAccess(req, "cloud") {
        t.Fatal("wildcard should allow all backends")
    }
}

type mockKeyLookupStore struct {
    entry *store.APIKeyEntry
    err   error
}

func (m *mockKeyLookupStore) GetKeyByHash(hash string) (*store.APIKeyEntry, error) {
    if m.err != nil {
        return nil, m.err
    }
    return m.entry, nil
}

func TestAPIKeyAuthWithStore_HashLookupSuccess(t *testing.T) {
    slog.Info("test APIKeyAuthWithStore_HashLookupSuccess")
    rawKey := "sk-admin-generated-test-key"
    sum := sha256.Sum256([]byte(rawKey))
    hash := hex.EncodeToString(sum[:])
    st := &mockKeyLookupStore{
        entry: &store.APIKeyEntry{
            Name:          "managed-key",
            KeyHash:       hash,
            Status:        "active",
            AllowedModels: []string{"gpt-4o"},
        },
    }
    cfg := &config.AuthConfig{Enabled: true}
    called := false
    handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        p := PrincipalFromContext(r.Context())
        if p == nil || p.KeyConfig == nil || p.KeyConfig.Name != "managed-key" {
            t.Fatalf("expected principal with managed-key, got %+v", p)
        }
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+rawKey)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatalf("store-managed key must authenticate, got status %d body %s", rec.Code, rec.Body.String())
    }
}

func TestAPIKeyAuthWithStore_DisabledKeyRejected(t *testing.T) {
    slog.Info("test APIKeyAuthWithStore_DisabledKeyRejected")
    rawKey := "sk-disabled-test-key"
    sum := sha256.Sum256([]byte(rawKey))
    hash := hex.EncodeToString(sum[:])
    st := &mockKeyLookupStore{
        entry: &store.APIKeyEntry{
            Name:    "disabled-key",
            KeyHash: hash,
            Status:  "revoked",
        },
    }
    cfg := &config.AuthConfig{Enabled: true}
    handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("revoked key must not reach handler")
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+rawKey)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401 for revoked key, got %d", rec.Code)
    }
}

func TestAPIKeyAuthWithStore_StaticKeyWins(t *testing.T) {
    slog.Info("test APIKeyAuthWithStore_StaticKeyWins")
    st := &mockKeyLookupStore{entry: &store.APIKeyEntry{Name: "store-key", Status: "active"}}
    cfg := &config.AuthConfig{
        Enabled: true,
        APIKeys: []config.AuthKeyConfig{{Key: "fg-static-key", Name: "static"}},
    }
    called := false
    handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        p := PrincipalFromContext(r.Context())
        if p.KeyConfig.Name != "static" {
            t.Fatalf("expected static key match, got %s", p.KeyConfig.Name)
        }
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req.Header.Set("Authorization", "Bearer fg-static-key")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Fatalf("static key must authenticate, got %d", rec.Code)
    }
}
