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

func TestVolcengineSignRequest_HMAC(t *testing.T) {
    slog.Info("test VolcengineSignRequest_HMAC")
    p := NewVolcengineProvider("test", config.BackendConfig{
        BaseURL: "http://localhost:8080",
    })
    p.accessKey = "test-access-key"
    p.secretKey = "test-secret-key"
    req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
    p.signRequest(req)
    if req.Header.Get("X-Date") == "" {
        t.Error("expected X-Date header")
    }
    auth := req.Header.Get("Authorization")
    if auth == "" {
        t.Error("expected Authorization header")
    }
}

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
}
