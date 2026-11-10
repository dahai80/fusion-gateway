package server

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/exchange"
)

// Issue #595: exchange endpoint + JWKS route behavior, dispatched through
// the real middleware chain (withMiddleware) so the bootstrap credential
// resolution and the exchange-only gate are exercised verbatim.

func te595Server(t *testing.T, exchangeEnabled bool) *Server {
    t.Helper()
    s := newTestServer()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Auth.Passthrough = false
    s.cfg.Config.Auth.MasterKey = "mk-te595-master-key"
    s.cfg.Config.Auth.TokenExchange.Enabled = exchangeEnabled
    s.cfg.Config.Auth.TokenExchange.MaxTTL = time.Hour
    s.cfg.Config.Auth.APIKeys = []config.AuthKeyConfig{
        {Key: "fk-office", Name: "office", RPM: 30, TPM: 20000, AllowedModels: []string{"glm5.2"}},
        {Key: "fk-only", Name: "only", ExchangeOnly: true, RPM: 30, TPM: 20000},
    }
    if exchangeEnabled {
        s.cfg.Config.Store.DataDir = t.TempDir()
        signer, err := exchange.NewSigner(s.cfg.Config.Store.DataDir)
        if err != nil {
            t.Fatalf("NewSigner: %v", err)
        }
        s.tokenSigner = signer
    }
    mux := http.NewServeMux()
    mux.HandleFunc("POST /auth/api/token/exchange", s.withMiddleware(s.handleTokenExchange))
    mux.HandleFunc("GET /.well-known/jwks.json", s.handleJWKS)
    mux.HandleFunc("/v1/chat/completions", s.withMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true}`))
    })))
    s.buildMiddlewareChain()
    s.mux = mux
    t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
    return s
}

func te595Do(t *testing.T, s *Server, method, path, cred string, body string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(method, path, strings.NewReader(body))
    if cred != "" {
        req.Header.Set("Authorization", "Bearer "+cred)
    }
    rec := httptest.NewRecorder()
    s.mux.ServeHTTP(rec, req)
    return rec
}

func Test595_ExchangeMintsScopedToken(t *testing.T) {
    s := te595Server(t, true)
    rec := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", "fk-office", `{"ttl_seconds":600}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("exchange must 200, got %d: %s", rec.Code, rec.Body.String())
    }
    var resp struct {
        AccessToken string `json:"access_token"`
        TokenType   string `json:"token_type"`
        ExpiresIn   int    `json:"expires_in"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatalf("response json: %v", err)
    }
    if resp.TokenType != "Bearer" || resp.AccessToken == "" {
        t.Fatalf("bad exchange response: %s", rec.Body.String())
    }
    if resp.ExpiresIn > 600 || resp.ExpiresIn <= 0 {
        t.Fatalf("expires_in = %d, want <= 600", resp.ExpiresIn)
    }
    vrec := te595Do(t, s, http.MethodPost, "/v1/chat/completions", resp.AccessToken, `{"model":"glm5.2"}`)
    if vrec.Code != http.StatusOK {
        t.Fatalf("minted token must pass /v1 auth, got %d: %s", vrec.Code, vrec.Body.String())
    }
}

func Test595_ExchangeTTLCappedByMaxTTL(t *testing.T) {
    s := te595Server(t, true)
    rec := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", "fk-office", `{"ttl_seconds":100000}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("exchange must 200, got %d", rec.Code)
    }
    var resp struct {
        ExpiresIn int `json:"expires_in"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &resp)
    if resp.ExpiresIn > 3600 {
        t.Fatalf("expires_in = %d exceeds max_ttl 3600", resp.ExpiresIn)
    }
}

func Test595_ExchangeOnlyKeyCanExchangeButNotV1(t *testing.T) {
    s := te595Server(t, true)
    vrec := te595Do(t, s, http.MethodPost, "/v1/chat/completions", "fk-only", `{"model":"glm5.2"}`)
    if vrec.Code != http.StatusUnauthorized {
        t.Fatalf("exchange-only key on /v1 must 401, got %d", vrec.Code)
    }
    if !strings.Contains(vrec.Body.String(), "exchange-only") {
        t.Fatalf("401 body must name the exchange-only restriction: %s", vrec.Body.String())
    }
    erec := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", "fk-only", `{}`)
    if erec.Code != http.StatusOK {
        t.Fatalf("exchange-only key on exchange endpoint must 200, got %d: %s", erec.Code, erec.Body.String())
    }
}

func Test595_MintedTokenCannotReExchange(t *testing.T) {
    s := te595Server(t, true)
    rec := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", "fk-office", `{}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("first exchange must succeed: %d", rec.Code)
    }
    var resp struct {
        AccessToken string `json:"access_token"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &resp)
    re := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", resp.AccessToken, `{}`)
    if re.Code != http.StatusForbidden {
        t.Fatalf("minted token re-exchange must 403, got %d: %s", re.Code, re.Body.String())
    }
}

func Test595_JWKSServedWhenEnabled(t *testing.T) {
    s := te595Server(t, true)
    rec := te595Do(t, s, http.MethodGet, "/.well-known/jwks.json", "", "")
    if rec.Code != http.StatusOK {
        t.Fatalf("jwks must 200, got %d", rec.Code)
    }
    var parsed struct {
        Keys []map[string]any `json:"keys"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
        t.Fatalf("jwks json: %v", err)
    }
    if len(parsed.Keys) != 1 || parsed.Keys[0]["kty"] != "RSA" {
        t.Fatalf("bad jwks: %s", rec.Body.String())
    }
}

func Test595_PlaneDisabled404s(t *testing.T) {
    s := te595Server(t, false)
    if rec := te595Do(t, s, http.MethodGet, "/.well-known/jwks.json", "", ""); rec.Code != http.StatusNotFound {
        t.Fatalf("disabled jwks must 404, got %d", rec.Code)
    }
    if rec := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", "fk-office", `{}`); rec.Code != http.StatusNotFound {
        t.Fatalf("disabled exchange must 404, got %d", rec.Code)
    }
}

func Test595_SigningKeyPersistsAcrossRestarts(t *testing.T) {
    s := te595Server(t, true)
    dataDir := s.cfg.Config.Store.DataDir
    rec := te595Do(t, s, http.MethodPost, "/auth/api/token/exchange", "mk-te595-master-key", `{}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("master-key exchange must succeed: %d %s", rec.Code, rec.Body.String())
    }
    var resp struct {
        AccessToken string `json:"access_token"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &resp)
    _ = s.Shutdown(context.Background())

    s2 := te595Server(t, true)
    // Same data dir: signer reloads the persisted keypair.
    s2.cfg.Config.Store.DataDir = dataDir
    signer2, err := exchange.NewSigner(dataDir)
    if err != nil {
        t.Fatalf("reload signer: %v", err)
    }
    s2.tokenSigner = signer2
    s2.buildMiddlewareChain()
    vrec := te595Do(t, s2, http.MethodPost, "/v1/chat/completions", resp.AccessToken, `{"model":"glm5.2"}`)
    if vrec.Code != http.StatusOK {
        t.Fatalf("token minted before restart must verify after restart, got %d: %s", vrec.Code, vrec.Body.String())
    }
}
