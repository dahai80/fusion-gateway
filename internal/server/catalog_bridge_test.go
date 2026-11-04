package server

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #504 regression: /v1/catalog must accept the bridged admin-JWT /
// shell-session principal — the browser shell holds a session token, not an
// fg-key, so the fg-key-only chain locked the shell's catalog view into its
// degrade state. Mirrors the #499 model-policy bridge treatment. The
// /api/v1/catalog shell alias registers the same composite (verified live).

func TestCatalog_AdminJWT_FullChain(t *testing.T) {
    s, tok := bridgeTestServer(t)
    r := httptest.NewRequest(http.MethodGet, "/v1/catalog", nil)
    r.Header.Set("Authorization", "Bearer "+tok)
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleCatalog)(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("admin JWT catalog GET must be 200 through the chain, got %d body=%s", w.Code, w.Body.String())
    }
    if w.Header().Get("Content-Type") == "" {
        t.Fatalf("catalog response must carry Content-Type")
    }
}

func TestCatalog_PlainAPIKey_StillAccepted(t *testing.T) {
    s, _ := bridgeTestServer(t)
    s.cfg.Config.Auth.APIKeys = []config.AuthKeyConfig{{Key: "plain-key", Name: "plain"}}
    s.buildMiddlewareChain()
    r := httptest.NewRequest(http.MethodGet, "/v1/catalog", nil)
    r.Header.Set("Authorization", "Bearer plain-key")
    w := httptest.NewRecorder()
    s.withPrincipalBridge(s.handleCatalog)(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("plain API key catalog GET must stay 200, got %d body=%s", w.Code, w.Body.String())
    }
}
