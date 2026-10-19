package adapter

import (
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestVolcengineSignRequest_APIKey(t *testing.T) {
    slog.Info("test VolcengineSignRequest_APIKey")
    p := NewVolcengineProvider("test", config.BackendConfig{
        BaseURL: "http://localhost:8080",
        APIKey:  "test-api-key",
    })
    req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
    p.signRequest(req)
    if req.Header.Get("Authorization") != "Bearer test-api-key" {
        t.Errorf("expected Bearer token, got %s", req.Header.Get("Authorization"))
    }
}

// B16 follow-up (regression report 1019): the AK/SK HMAC branch was
// unreachable (constructor never populated the fields) and its signature
// scheme matched no real Volcengine API. The provider is Bearer-only now;
// a missing api_key leaves the request unauthenticated (config validation
// refuses enabled backends without one).
func TestVolcengineSignRequest_NoKeys(t *testing.T) {
    slog.Info("test VolcengineSignRequest_NoKeys")
    p := NewVolcengineProvider("test", config.BackendConfig{
        BaseURL: "http://localhost:8080",
    })
    req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
    p.signRequest(req)
    if req.Header.Get("Authorization") != "" {
        t.Error("expected no Authorization header without keys")
    }
    if req.Header.Get("X-Date") != "" {
        t.Error("expected no X-Date header (HMAC path removed)")
    }
}
