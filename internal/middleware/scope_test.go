package middleware

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #262 K2: scope matrix → module grant resolution.

func TestScopeAllowsModule(t *testing.T) {
    cases := []struct {
        name   string
        scopes []string
        module string
        want   bool
    }{
        {"wildcard", []string{"*"}, "chat", true},
        {"bare_module", []string{"chat"}, "chat", true},
        {"other_module", []string{"chat"}, "rag", false},
        {"module_wildcard_actions", []string{"chat:*"}, "chat", true},
        {"module_wildcard_other", []string{"chat:*"}, "rag", false},
        {"specific_action_grants_module", []string{"cowork:runs:create"}, "cowork", true},
        {"nested_action_other_module", []string{"cowork:runs:create"}, "chat", false},
        {"empty_module_passes", []string{"chat"}, "", true},
        // Empty scopes deny at the pure-function level; the enforcement
        // middleware short-circuits empty-scope keys before calling this
        // (legacy keys keep their historical unrestricted behavior).
        {"empty_scopes", nil, "chat", false},
        {"multi_scope_hit", []string{"rag", "design"}, "design", true},
        {"multi_scope_miss", []string{"rag", "design"}, "agent", false},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := ScopeAllowsModule(tc.scopes, tc.module); got != tc.want {
                t.Fatalf("ScopeAllowsModule(%v, %q) = %v, want %v", tc.scopes, tc.module, got, tc.want)
            }
        })
    }
}

func scopedRequest(t *testing.T, path string, scopes []string, authMethod string, isMaster bool) *httptest.ResponseRecorder {
    t.Helper()
    principal := &Principal{
        IsMaster: isMaster,
        AuthMethod: authMethod,
        KeyConfig: &config.AuthKeyConfig{Name: "scoped-key", Scopes: scopes},
    }
    handler := ScopeEnforce()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, path, nil)
    req = req.WithContext(ContextWithPrincipal(req.Context(), principal))
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    return rec
}

func TestScopeEnforce(t *testing.T) {
    t.Run("granted_module_passes", func(t *testing.T) {
        rec := scopedRequest(t, "/v1/chat/completions", []string{"chat"}, "apikey", false)
        if rec.Code != http.StatusOK {
            t.Fatalf("granted chat scope on /v1/chat/completions: %d body=%s, want 200", rec.Code, rec.Body.String())
        }
    })
    t.Run("denied_module_403", func(t *testing.T) {
        rec := scopedRequest(t, "/v1/embeddings", []string{"chat"}, "apikey", false)
        if rec.Code != http.StatusForbidden {
            t.Fatalf("chat-only scope on /v1/embeddings: %d, want 403", rec.Code)
        }
        var body map[string]any
        if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
            t.Fatalf("invalid error json: %v", err)
        }
        errObj, _ := body["error"].(map[string]any)
        if errObj == nil || errObj["type"] != "permission_error" {
            t.Fatalf("expected permission_error, got %v", body)
        }
    })
    t.Run("empty_scopes_pass", func(t *testing.T) {
        rec := scopedRequest(t, "/v1/chat/completions", nil, "apikey", false)
        if rec.Code != http.StatusOK {
            t.Fatalf("legacy key (no scopes) must pass: %d", rec.Code)
        }
    })
    t.Run("master_passes", func(t *testing.T) {
        rec := scopedRequest(t, "/v1/embeddings", []string{"chat"}, "apikey", true)
        if rec.Code != http.StatusOK {
            t.Fatalf("master must bypass scope enforcement: %d", rec.Code)
        }
    })
    t.Run("admin_jwt_passes", func(t *testing.T) {
        rec := scopedRequest(t, "/v1/chat/completions", []string{"rag"}, "admin-jwt", false)
        if rec.Code != http.StatusOK {
            t.Fatalf("admin-jwt identity is gated elsewhere, must pass: %d", rec.Code)
        }
    })
    t.Run("unclassified_path_passes", func(t *testing.T) {
        rec := scopedRequest(t, "/health", []string{"chat"}, "apikey", false)
        if rec.Code != http.StatusOK {
            t.Fatalf("non-inference path must pass: %d", rec.Code)
        }
    })
}
