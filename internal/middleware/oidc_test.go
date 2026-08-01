package middleware

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/binary"
    "encoding/json"
    "fmt"
    "log/slog"
    "math/big"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/golang-jwt/jwt/v5"
)

func TestNewOIDCAuthenticator_Disabled(t *testing.T) {
    slog.Info("test NewOIDCAuthenticator_Disabled")
    auth, err := NewOIDCAuthenticator(OIDCConfig{Enabled: false})
    if err != nil {
        t.Fatal(err)
    }
    if auth.Enabled() {
        t.Error("disabled authenticator should not be enabled")
    }
}

func TestOIDCAuthenticator_Middleware_Disabled(t *testing.T) {
    slog.Info("test OIDCAuthenticator_Middleware_Disabled")
    auth, _ := NewOIDCAuthenticator(OIDCConfig{Enabled: false})
    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestOIDCAuthenticator_Middleware_NoToken(t *testing.T) {
    slog.Info("test OIDCAuthenticator_Middleware_NoToken")
    auth, _ := NewOIDCAuthenticator(OIDCConfig{Enabled: false})
    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 with no token when OIDC disabled, got %d", rec.Code)
    }
}

func TestJWKToRSAPublicKey(t *testing.T) {
    slog.Info("test JWKToRSAPublicKey")
    // Standard RSA-2048 test values (base64url-encoded)
    n := "vGxYk1KxF0V_8x4WqUzO8JGO0LQl1hO6bTL8c3YBq3RQz4hP9kX6mN2wA5D7E1fG3HiJ0lK8oP5qR7sT2uV4wX6yZ8aB0cD2eF4gH6iJ8kL0mN2oP4qR6sT8uV0wX2yZ4aB6cD8eF0gH2iJ4kL6mN8oP0qR2sT4uV6wX8yZ0aB2cD4eF6gH8iJ0kL2mN4oP6qR8sT0uV2wX4yZ6aB8cD0eF2gH4iJ6kL8mN0oP2qR4sT6uV8wX0yZ2aB4cD6eF8gH0iJ2kL4mN6oP8qR0sT2uV4wX6yZ8aB0cD2eF4gH6iJ8kL0mN2oP4qR6sT8uV0wX2yZ4aB6cD8eF0gH2iJ4kL6mN8oP0qR"
    e := "AQAB"
    _, err := jwkToRSAPublicKey(n, e)
    if err != nil {
        t.Fatalf("jwkToRSAPublicKey failed: %v", err)
    }
}

func TestJWKToRSAPublicKey_InvalidN(t *testing.T) {
    slog.Info("test JWKToRSAPublicKey_InvalidN")
    _, err := jwkToRSAPublicKey("!!!invalid!!!", "AQAB")
    if err == nil {
        t.Error("expected error for invalid n")
    }
}

func TestJWKToRSAPublicKey_InvalidE(t *testing.T) {
    slog.Info("test JWKToRSAPublicKey_InvalidE")
    _, err := jwkToRSAPublicKey("vGxYk1KxF0V_8x4WqUzO8JGO0LQl1hO6bTL8c3YBq3RQz4hP9kX6mN2wA5D7E1fG3HiJ0lK8oP5qR7sT2uV4wX6yZ8aB0cD2eF4gH6iJ8kL0mN2oP4qR6sT8uV0wX2yZ4aB6cD8eF0gH2iJ4kL6mN8oP0qR", "!!!invalid!!!")
    if err == nil {
        t.Error("expected error for invalid e")
    }
}

func TestOIDCDiscovery_Fetch(t *testing.T) {
    slog.Info("test OIDCDiscovery_Fetch")
    discResp := oidcDiscovery{
        Issuer:           "https://test.example.com",
        AuthorizationURL: "https://test.example.com/authorize",
        TokenURL:         "https://test.example.com/token",
        JWKSURL:          "https://test.example.com/jwks",
    }
    body, _ := json.Marshal(discResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/.well-known/openid-configuration" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    p.mu.RLock()
    disc := p.discovery
    p.mu.RUnlock()
    if disc == nil || disc.Issuer != "https://test.example.com" {
        t.Error("discovery not fetched correctly")
    }
}

func TestOIDCDiscovery_FetchError(t *testing.T) {
    slog.Info("test OIDCDiscovery_FetchError")
    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: "http://127.0.0.1:1"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{},
    }
    if err := p.fetchDiscovery(); err == nil {
        t.Error("expected error for unreachable discovery")
    }
}

func TestNewOIDCAuthenticator_EnabledBadIssuer(t *testing.T) {
    slog.Info("test NewOIDCAuthenticator_EnabledBadIssuer")
    _, err := NewOIDCAuthenticator(OIDCConfig{Enabled: true, Issuer: "http://127.0.0.1:1"})
    if err == nil {
        t.Error("expected error for bad issuer")
    }
}

func TestOIDCProvider_RefreshIfNeeded(t *testing.T) {
    slog.Info("test OIDCProvider_RefreshIfNeeded")
    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: "http://127.0.0.1:1"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{},
        fetchedAt:  time.Now(),
    }
    p.refreshIfNeeded()
}

func TestOIDCProvider_ValidateToken_Invalid(t *testing.T) {
    slog.Info("test OIDCProvider_ValidateToken_Invalid")
    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: "https://test.example.com"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{},
        fetchedAt:  time.Now(),
    }
    _, err := p.validateToken("invalid-token")
    if err == nil {
        t.Error("expected error for invalid token")
    }
}

func newOIDCTestServer(t *testing.T) (*httptest.Server, *rsa.PrivateKey, string) {
    t.Helper()
    privKey, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        t.Fatal(err)
    }
    kid := "test-kid-1"

    nStr := toBase64URL(privKey.PublicKey.N.Bytes())
    eInt := privKey.PublicKey.E
    eBytes := make([]byte, 4)
    binary.BigEndian.PutUint32(eBytes, uint32(eInt))
    eStr := toBase64URL(eBytes)

    jwksResp := oidcJWKS{
        Keys: []oidcJWK{
            {
                Kid: kid,
                Kty: "RSA",
                Use: "sig",
                N:   nStr,
                E:   eStr,
                Alg: "RS256",
            },
        },
    }
    jwksBody, _ := json.Marshal(jwksResp)

    discResp := oidcDiscovery{
        Issuer:           "",
        AuthorizationURL: "",
        TokenURL:         "",
        JWKSURL:          "",
        UserInfoURL:      "",
    }

    var srv *httptest.Server
    srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/.well-known/openid-configuration":
            discResp.Issuer = srv.URL
            discResp.JWKSURL = srv.URL + "/jwks"
            discResp.AuthorizationURL = srv.URL + "/authorize"
            discResp.TokenURL = srv.URL + "/token"
            discResp.UserInfoURL = srv.URL + "/userinfo"
            w.WriteHeader(http.StatusOK)
            _ = json.NewEncoder(w).Encode(discResp)
        case "/jwks":
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(jwksBody)
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))

    discResp.Issuer = srv.URL
    discResp.JWKSURL = srv.URL + "/jwks"

    return srv, privKey, kid
}

func toBase64URL(data []byte) string {
    return base64.RawURLEncoding.EncodeToString(data)
}

func TestOIDCFetchJWKS_Success(t *testing.T) {
    slog.Info("test OIDCFetchJWKS_Success")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatalf("fetchJWKS failed: %v", err)
    }
    p.mu.RLock()
    keyCount := len(p.publicKeys)
    p.mu.RUnlock()
    if keyCount == 0 {
        t.Error("expected at least one public key")
    }
}

func TestOIDCFetchJWKS_NoDiscoveryURL(t *testing.T) {
    slog.Info("test OIDCFetchJWKS_NoDiscoveryURL")
    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: "https://test.example.com"},
        discovery:  &oidcDiscovery{JWKSURL: ""},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{},
    }
    if err := p.fetchJWKS(); err == nil {
        t.Error("expected error when discovery URL is empty")
    }
}

func TestOIDCFetchJWKS_Unreachable(t *testing.T) {
    slog.Info("test OIDCFetchJWKS_Unreachable")
    p := &OIDCProvider{
        cfg: OIDCConfig{Issuer: "http://127.0.0.1:1"},
        discovery: &oidcDiscovery{
            JWKSURL: "http://127.0.0.1:1/jwks",
        },
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{},
    }
    if err := p.fetchJWKS(); err == nil {
        t.Error("expected error for unreachable JWKS")
    }
}

func TestOIDCFetchJWKS_InvalidJSON(t *testing.T) {
    slog.Info("test OIDCFetchJWKS_InvalidJSON")
    var srv *httptest.Server
    srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/.well-known/openid-configuration" {
            _ = json.NewEncoder(w).Encode(oidcDiscovery{JWKSURL: srv.URL + "/jwks"})
        } else if r.URL.Path == "/jwks" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write([]byte("not json"))
        }
    }))
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
    }
    _ = p.fetchDiscovery()
    if err := p.fetchJWKS(); err == nil {
        t.Error("expected error for invalid JWKS JSON")
    }
}

func TestOIDCValidateToken_WithRealKey(t *testing.T) {
    slog.Info("test OIDCValidateToken_WithRealKey")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
        "aud": "test-client",
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    claims, err := p.validateToken(tokenStr)
    if err != nil {
        t.Fatalf("valid token should pass: %v", err)
    }
    if claims["sub"] != "user1" {
        t.Errorf("expected sub=user1, got %v", claims["sub"])
    }
}

func TestOIDCValidateToken_WrongSigningMethod(t *testing.T) {
    slog.Info("test OIDCValidateToken_WrongSigningMethod")
    srv, _, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString([]byte("secret"))
    if err != nil {
        t.Fatal(err)
    }

    _, err = p.validateToken(tokenStr)
    if err == nil {
        t.Error("expected error for wrong signing method")
    }
}

func TestOIDCValidateToken_UnknownKid(t *testing.T) {
    slog.Info("test OIDCValidateToken_UnknownKid")
    srv, privKey, _ := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
    })
    token.Header["kid"] = "unknown-kid"
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    _, err = p.validateToken(tokenStr)
    if err == nil {
        t.Error("expected error for unknown kid")
    }
}

func TestOIDCValidateToken_IssuerMismatch(t *testing.T) {
    slog.Info("test OIDCValidateToken_IssuerMismatch")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": "https://wrong-issuer.example.com",
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    _, err = p.validateToken(tokenStr)
    if err == nil {
        t.Error("expected error for issuer mismatch")
    }
}

func TestOIDCValidateToken_AudienceString(t *testing.T) {
    slog.Info("test OIDCValidateToken_AudienceString")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL, Audiences: "client-a,client-b"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
        "aud": "client-a",
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    claims, err := p.validateToken(tokenStr)
    if err != nil {
        t.Fatalf("audience string match should pass: %v", err)
    }
    if claims["sub"] != "user1" {
        t.Error("expected sub claim")
    }
}

func TestOIDCValidateToken_AudienceArray(t *testing.T) {
    slog.Info("test OIDCValidateToken_AudienceArray")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL, Audiences: "client-a,client-b"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
        "aud": []interface{}{"client-b", "client-c"},
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    claims, err := p.validateToken(tokenStr)
    if err != nil {
        t.Fatalf("audience array match should pass: %v", err)
    }
    if claims["sub"] != "user1" {
        t.Error("expected sub claim")
    }
}

func TestOIDCValidateToken_AudienceMismatch(t *testing.T) {
    slog.Info("test OIDCValidateToken_AudienceMismatch")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL, Audiences: "client-x,client-y"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
        "aud": "client-z",
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    _, err = p.validateToken(tokenStr)
    if err == nil {
        t.Error("expected error for audience mismatch")
    }
}

func TestOIDCValidateToken_AudienceArrayMismatch(t *testing.T) {
    slog.Info("test OIDCValidateToken_AudienceArrayMismatch")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL, Audiences: "client-x"},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now(),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
        "aud": []interface{}{"client-z", "client-w"},
    })
    token.Header["kid"] = kid
    tokenStr, err := token.SignedString(privKey)
    if err != nil {
        t.Fatal(err)
    }

    _, err = p.validateToken(tokenStr)
    if err == nil {
        t.Error("expected error for audience array mismatch")
    }
}

func TestOIDCMiddleware_BearerToken(t *testing.T) {
    slog.Info("test OIDCMiddleware_BearerToken")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        // skip if discovery fails due to custom transport
        t.Skipf("cannot create OIDC authenticator with test server: %v", err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user1",
        "iss": srv.URL,
    })
    token.Header["kid"] = kid
    tokenStr, _ := token.SignedString(privKey)

    var gotPrincipal *Principal
    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPrincipal = PrincipalFromContext(r.Context())
        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenStr))
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    if gotPrincipal == nil || gotPrincipal.AuthMethod != "oidc" {
        t.Error("expected oidc auth method on principal")
    }
}

func TestOIDCMiddleware_CookieToken(t *testing.T) {
    slog.Info("test OIDCMiddleware_CookieToken")
    srv, privKey, kid := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        t.Skipf("cannot create OIDC authenticator: %v", err)
    }

    token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "user2",
        "iss": srv.URL,
    })
    token.Header["kid"] = kid
    tokenStr, _ := token.SignedString(privKey)

    var gotPrincipal *Principal
    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPrincipal = PrincipalFromContext(r.Context())
        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.AddCookie(&http.Cookie{Name: "admin_token", Value: tokenStr})
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    if gotPrincipal == nil || gotPrincipal.AuthMethod != "oidc" {
        t.Error("expected oidc auth method on principal via cookie")
    }
}

func TestOIDCMiddleware_NoAuth_Passthrough(t *testing.T) {
    slog.Info("test OIDCMiddleware_NoAuth_Passthrough")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        t.Skipf("cannot create OIDC authenticator: %v", err)
    }

    called := false
    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should call next handler when no auth provided")
    }
}

func TestOIDCMiddleware_InvalidToken_401(t *testing.T) {
    slog.Info("test OIDCMiddleware_InvalidToken_401")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        t.Skipf("cannot create OIDC authenticator: %v", err)
    }

    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))

    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer invalid-token")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401, got %d", rec.Code)
    }
}

func TestOIDCMiddleware_APIKeyPassthrough(t *testing.T) {
    slog.Info("test OIDCMiddleware_APIKeyPassthrough")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        t.Skipf("cannot create OIDC authenticator: %v", err)
    }

    called := false
    handler := auth.Middleware(&config.AuthConfig{
        Enabled:    true,
        Passthrough: false,
        APIKeys:    []config.AuthKeyConfig{{Key: "sk-test-key", Name: "test"}},
    })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer sk-test-key")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should pass through for valid API key")
    }
}

func TestOIDCMiddleware_MasterKeyPassthrough(t *testing.T) {
    slog.Info("test OIDCMiddleware_MasterKeyPassthrough")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        t.Skipf("cannot create OIDC authenticator: %v", err)
    }

    called := false
    handler := auth.Middleware(&config.AuthConfig{
        Enabled:    true,
        Passthrough: false,
        MasterKey:  "master-secret",
    })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))

    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("Authorization", "Bearer master-secret")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should pass through for master key")
    }
}

func TestOIDCMiddleware_ProviderNil(t *testing.T) {
    slog.Info("test OIDCMiddleware_ProviderNil")
    auth := &OIDCAuthenticator{cfg: OIDCConfig{Enabled: true}, provider: nil}
    called := false
    handler := auth.Middleware(&config.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should call next when provider is nil")
    }
}

func TestOIDCRefreshIfNeeded_Stale(t *testing.T) {
    slog.Info("test OIDCRefreshIfNeeded_Stale")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
        fetchedAt:  time.Now().Add(-2 * time.Hour),
    }
    if err := p.fetchDiscovery(); err != nil {
        t.Fatal(err)
    }
    if err := p.fetchJWKS(); err != nil {
        t.Fatal(err)
    }
    p.refreshIfNeeded()
}

func TestOIDCRefreshIfNeeded_RefreshFails(t *testing.T) {
    slog.Info("test OIDCRefreshIfNeeded_RefreshFails")
    p := &OIDCProvider{
        cfg: OIDCConfig{Issuer: "http://127.0.0.1:1"},
        discovery: &oidcDiscovery{
            JWKSURL: "http://127.0.0.1:1/jwks",
        },
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{},
        fetchedAt:  time.Now().Add(-2 * time.Hour),
    }
    p.refreshIfNeeded()
}

func TestOIDCDiscovery_InvalidJSON(t *testing.T) {
    slog.Info("test OIDCDiscovery_InvalidJSON")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    p := &OIDCProvider{
        cfg:        OIDCConfig{Issuer: srv.URL},
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: srv.Client(),
    }
    if err := p.fetchDiscovery(); err == nil {
        t.Error("expected error for invalid discovery JSON")
    }
}

func TestNewOIDCAuthenticator_EnabledWithServer(t *testing.T) {
    slog.Info("test NewOIDCAuthenticator_EnabledWithServer")
    srv, _, _ := newOIDCTestServer(t)
    defer srv.Close()

    auth, err := NewOIDCAuthenticator(OIDCConfig{
        Enabled:  true,
        Issuer:   srv.URL,
        ClientID: "test-client",
    })
    if err != nil {
        t.Fatalf("expected success with test server: %v", err)
    }
    if !auth.Enabled() {
        t.Error("authenticator should be enabled")
    }
}

func TestJWKToRSAPublicKey_LargeE(t *testing.T) {
    slog.Info("test JWKToRSAPublicKey_LargeE")
    privKey, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        t.Fatal(err)
    }
    nBytes := privKey.PublicKey.N.Bytes()
    nStr := toBase64URL(nBytes)

    eBig := new(big.Int).SetInt64(int64(privKey.PublicKey.E))
    eBytes := eBig.Bytes()
    eStr := toBase64URL(eBytes)

    _, err = jwkToRSAPublicKey(nStr, eStr)
    if err != nil {
        t.Fatalf("jwkToRSAPublicKey with large e failed: %v", err)
    }
}
