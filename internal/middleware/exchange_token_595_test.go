package middleware

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/exchange"
)

// Issue #595: minted-token acceptance and exchange-only enforcement on the
// /v1/* middleware chain.

func signer595(t *testing.T) *exchange.Signer {
    t.Helper()
    s, err := exchange.NewSigner(t.TempDir())
    if err != nil {
        t.Fatalf("NewSigner: %v", err)
    }
    return s
}

func cfg595(entries ...config.AuthKeyConfig) *config.AuthConfig {
    return &config.AuthConfig{
        Enabled:  true,
        MasterKey: "mk-595-master",
        APIKeys:  entries,
    }
}

func run595(t *testing.T, cfg *config.AuthConfig, verifier TokenVerifier, cred string) *httptest.ResponseRecorder {
    t.Helper()
    called := false
    var gotName string
    var gotRPM, gotTPM int
    var gotModels []string
    handler := APIKeyAuthWithStore(cfg, nil, verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        p := PrincipalFromContext(r.Context())
        if p != nil && p.KeyConfig != nil {
            gotName = p.KeyConfig.Name
            gotRPM = p.KeyConfig.RPM
            gotTPM = p.KeyConfig.TPM
            gotModels = p.KeyConfig.AllowedModels
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    if cred != "" {
        req.Header.Set("Authorization", "Bearer "+cred)
    }
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code == http.StatusOK && !called {
        t.Fatal("handler not reached on 200")
    }
    if called {
        t.Logf("resolved key=%s rpm=%d tpm=%d models=%v", gotName, gotRPM, gotTPM, gotModels)
    }
    return rec
}

func Test595_MintedTokenAcceptedOnV1(t *testing.T) {
    s := signer595(t)
    cfg := cfg595(config.AuthKeyConfig{Key: "fk-boot", Name: "boot", RPM: 30, TPM: 20000, AllowedModels: []string{"glm5.2"}})
    tok, _, err := s.Sign("boot", "fusion-api", 5*time.Minute, "team-x", []string{"glm5.2"}, 30, 20000)
    if err != nil {
        t.Fatal(err)
    }
    rec := run595(t, cfg, s, tok)
    if rec.Code != http.StatusOK {
        t.Fatalf("minted token must reach /v1, got %d: %s", rec.Code, rec.Body.String())
    }
}

func Test595_MintedTokenInheritsClaims(t *testing.T) {
    s := signer595(t)
    cfg := cfg595()
    tok, _, err := s.Sign("boot", "fusion-api", 5*time.Minute, "team-x", []string{"glm5.2"}, 30, 20000)
    if err != nil {
        t.Fatal(err)
    }
    var name string
    var rpm, tpm int
    var models []string
    handler := APIKeyAuthWithStore(cfg, nil, s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        name = p.KeyConfig.Name
        rpm = p.KeyConfig.RPM
        tpm = p.KeyConfig.TPM
        models = p.KeyConfig.AllowedModels
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req.Header.Set("Authorization", "Bearer "+tok)
    handler.ServeHTTP(httptest.NewRecorder(), req)
    if name != "boot" || rpm != 30 || tpm != 20000 || len(models) != 1 || models[0] != "glm5.2" {
        t.Fatalf("claims not inherited: name=%s rpm=%d tpm=%d models=%v", name, rpm, tpm, models)
    }
}

func Test595_MintedTokenRejectedWithoutSigner(t *testing.T) {
    cfg := cfg595()
    s := signer595(t)
    tok, _, _ := s.Sign("boot", "fusion-api", time.Minute, "", nil, 0, 0)
    rec := run595(t, cfg, nil, tok)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("minted token with verifier disabled must 401, got %d", rec.Code)
    }
}

func Test595_MintedTokenRejectedWhenForeignSigner(t *testing.T) {
    s := signer595(t)
    other := signer595(t)
    cfg := cfg595()
    tok, _, _ := other.Sign("boot", "fusion-api", time.Minute, "", nil, 0, 0)
    rec := run595(t, cfg, s, tok)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("foreign-signed token must 401, got %d", rec.Code)
    }
}

func Test595_ExchangeOnlyKeyBlockedOnV1(t *testing.T) {
    cfg := cfg595(config.AuthKeyConfig{Key: "fk-office", Name: "office", ExchangeOnly: true, RPM: 30, TPM: 20000})
    rec := run595(t, cfg, nil, "fk-office")
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("exchange-only key on /v1 must 401, got %d", rec.Code)
    }
    if !strings.Contains(rec.Body.String(), "exchange-only") {
        t.Fatalf("401 body must name the exchange-only restriction: %s", rec.Body.String())
    }
}

func Test595_ExchangeOnlyKeyAllowedOnExchangePath(t *testing.T) {
    cfg := cfg595(config.AuthKeyConfig{Key: "fk-office", Name: "office", ExchangeOnly: true, RPM: 30, TPM: 20000})
    handler := APIKeyAuthWithStore(cfg, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        p := PrincipalFromContext(r.Context())
        if p == nil || p.KeyConfig == nil || p.KeyConfig.Name != "office" {
            t.Error("exchange path must resolve the exchange-only principal")
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, ExchangePath, nil)
    req.Header.Set("Authorization", "Bearer fk-office")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("exchange-only key must pass on the exchange path, got %d", rec.Code)
    }
}

func Test595_NormalKeyUnaffectedByGate(t *testing.T) {
    cfg := cfg595(config.AuthKeyConfig{Key: "fk-full", Name: "full", RPM: 30, TPM: 20000})
    rec := run595(t, cfg, nil, "fk-full")
    if rec.Code != http.StatusOK {
        t.Fatalf("normal key on /v1 must still pass, got %d", rec.Code)
    }
}
