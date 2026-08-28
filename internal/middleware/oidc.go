package middleware

import (
    "crypto/rsa"
    "encoding/base64"
    "encoding/binary"
    "encoding/json"
    "fmt"
    "log/slog"
    "math/big"
    "net/http"
    "strings"
    "sync"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type OIDCConfig struct {
    Enabled       bool   `mapstructure:"enabled"`
    Issuer        string `mapstructure:"issuer"`
    ClientID      string `mapstructure:"client_id"`
    Audiences     string `mapstructure:"audiences"`
    Scopes        string `mapstructure:"scopes"`
    ClaimMappings string `mapstructure:"claim_mappings"`
}

type oidcDiscovery struct {
    Issuer           string `json:"issuer"`
    AuthorizationURL string `json:"authorization_endpoint"`
    TokenURL         string `json:"token_endpoint"`
    JWKSURL          string `json:"jwks_uri"`
    UserInfoURL      string `json:"userinfo_endpoint"`
}

type oidcJWKS struct {
    Keys []oidcJWK `json:"keys"`
}

type oidcJWK struct {
    Kid string `json:"kid"`
    Kty string `json:"kty"`
    Use string `json:"use"`
    N   string `json:"n"`
    E   string `json:"e"`
    Alg string `json:"alg"`
}

type OIDCProvider struct {
    cfg        OIDCConfig
    discovery  *oidcDiscovery
    publicKeys map[string]*rsa.PublicKey
    mu         sync.RWMutex
    fetchedAt  time.Time
    httpClient *http.Client
}

// A1 fix: OIDCAuthenticator replaces global oidcProvider variable.
// Constructed via NewOIDCAuthenticator, injected into Server, used in middleware chain.
type OIDCAuthenticator struct {
    provider *OIDCProvider
    cfg      OIDCConfig
}

func NewOIDCAuthenticator(cfg OIDCConfig) (*OIDCAuthenticator, error) {
    if !cfg.Enabled {
        slog.Info("oidc disabled")
        return &OIDCAuthenticator{cfg: cfg}, nil
    }

    p := &OIDCProvider{
        cfg:        cfg,
        publicKeys: make(map[string]*rsa.PublicKey),
        httpClient: &http.Client{Timeout: 10 * time.Second},
    }

    if err := p.fetchDiscovery(); err != nil {
        return nil, fmt.Errorf("oidc discovery failed: %w", err)
    }

    if err := p.fetchJWKS(); err != nil {
        return nil, fmt.Errorf("oidc jwks fetch failed: %w", err)
    }

    slog.Info("oidc initialized", "issuer", cfg.Issuer, "client_id", cfg.ClientID)
    return &OIDCAuthenticator{provider: p, cfg: cfg}, nil
}

func (a *OIDCAuthenticator) Enabled() bool {
    return a.provider != nil && a.cfg.Enabled
}

func (a *OIDCAuthenticator) Middleware(authCfg *config.AuthConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx, p := EnsurePrincipal(r.Context())

            if a.provider == nil || !a.cfg.Enabled {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            // F10: OIDC middleware must ONLY accept OIDC tokens — never collapse
            // API-key/master-key equality into this path. The prior code compared
            // the raw bearer token against every API key and master_key (oidc.go
            // 116-127) and short-circuited before validateToken, so any API key
            // bypassed OIDC signature/iss/aud checks AND leaked timing via `==`.
            // Dual-mode auth is preserved the safe way: the upstream APIKeyAuth
            // middleware already authenticated the Principal (AuthMethod=="apikey",
            // KeyConfig!=nil or IsMaster). Trust that fully-authenticated identity
            // rather than re-comparing raw credentials across auth domains.
            alreadyAuthed := p != nil && p.AuthMethod != "" && (p.IsMaster || p.KeyConfig != nil)
            if alreadyAuthed {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            _ = authCfg // API-key auth is handled by a separate middleware; OIDC validates only OIDC tokens.

            var tokenStr string
            auth := r.Header.Get("Authorization")
            if strings.HasPrefix(auth, "Bearer ") {
                tokenStr = strings.TrimPrefix(auth, "Bearer ")
            } else if cookie, err := r.Cookie("admin_token"); err == nil && cookie.Value != "" {
                tokenStr = cookie.Value
            } else {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            claims, err := a.provider.validateToken(tokenStr)
            if err != nil {
                slog.Warn("oidc token validation failed", "error", err, "path", r.URL.Path)
                http.Error(w, `{"error":{"message":"Invalid OIDC token","type":"auth_error"}}`, http.StatusUnauthorized)
                return
            }

            slog.Debug("oidc token validated", "sub", claims["sub"], "path", r.URL.Path)
            p.AuthMethod = "oidc"
            p.OIDCClaims = claims
            p.OIDCToken = tokenStr
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func (p *OIDCProvider) fetchDiscovery() error {
    wellKnown := strings.TrimSuffix(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
    resp, err := p.httpClient.Get(wellKnown)
    if err != nil {
        return fmt.Errorf("fetch discovery: %w", err)
    }
    defer resp.Body.Close()

    var disc oidcDiscovery
    // RR9 (audit P0): bound success-path read (10 MiB) — a hostile/misconfigured
    // OIDC issuer could return a giant discovery doc; see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&disc); err != nil {
        return fmt.Errorf("decode discovery: %w", err)
    }

    p.mu.Lock()
    p.discovery = &disc
    p.mu.Unlock()
    slog.Info("oidc discovery fetched", "issuer", disc.Issuer, "jwks_uri", disc.JWKSURL)
    return nil
}

func (p *OIDCProvider) fetchJWKS() error {
    p.mu.RLock()
    jwksURL := p.discovery.JWKSURL
    p.mu.RUnlock()

    if jwksURL == "" {
        return fmt.Errorf("jwks_uri not available from discovery")
    }

    resp, err := p.httpClient.Get(jwksURL)
    if err != nil {
        return fmt.Errorf("fetch jwks: %w", err)
    }
    defer resp.Body.Close()

    var jwks oidcJWKS
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&jwks); err != nil {
        return fmt.Errorf("decode jwks: %w", err)
    }

    keys := make(map[string]*rsa.PublicKey)
    for _, jwk := range jwks.Keys {
        if jwk.Kty != "RSA" || jwk.Use != "sig" {
            continue
        }
        pubKey, err := jwkToRSAPublicKey(jwk.N, jwk.E)
        if err != nil {
            slog.Warn("oidc skip jwk", "kid", jwk.Kid, "error", err)
            continue
        }
        keys[jwk.Kid] = pubKey
    }

    p.mu.Lock()
    p.publicKeys = keys
    p.fetchedAt = time.Now()
    p.mu.Unlock()
    slog.Info("oidc jwks fetched", "key_count", len(keys))
    return nil
}

func jwkToRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
    nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
    if err != nil {
        return nil, fmt.Errorf("decode n: %w", err)
    }
    eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
    if err != nil {
        return nil, fmt.Errorf("decode e: %w", err)
    }

    n := new(big.Int).SetBytes(nBytes)
    var eInt int
    if len(eBytes) < 4 {
        eInt = int(binary.BigEndian.Uint32(append(make([]byte, 4-len(eBytes)), eBytes...)))
    } else {
        eInt = int(new(big.Int).SetBytes(eBytes).Int64())
    }

    return &rsa.PublicKey{N: n, E: eInt}, nil
}

func (p *OIDCProvider) refreshIfNeeded() {
    p.mu.RLock()
    age := time.Since(p.fetchedAt)
    p.mu.RUnlock()

    if age > 1*time.Hour {
        slog.Info("oidc refreshing jwks")
        if err := p.fetchJWKS(); err != nil {
            slog.Error("oidc jwks refresh failed", "error", err)
        }
    }
}

func (p *OIDCProvider) validateToken(tokenStr string) (jwt.MapClaims, error) {
    p.refreshIfNeeded()

    token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
        if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
        }
        kid, _ := token.Header["kid"].(string)
        p.mu.RLock()
        pubKey, ok := p.publicKeys[kid]
        p.mu.RUnlock()
        if !ok {
            return nil, fmt.Errorf("unknown kid: %s", kid)
        }
        return pubKey, nil
    })
    if err != nil {
        return nil, fmt.Errorf("token validation: %w", err)
    }

    claims, ok := token.Claims.(jwt.MapClaims)
    if !ok || !token.Valid {
        return nil, fmt.Errorf("invalid token claims")
    }

    if p.cfg.Issuer != "" {
        if iss, _ := claims["iss"].(string); iss != p.cfg.Issuer {
            return nil, fmt.Errorf("invalid issuer: %s", iss)
        }
    }

    if p.cfg.Audiences != "" {
        auds := strings.Split(p.cfg.Audiences, ",")
        found := false
        switch v := claims["aud"].(type) {
        case string:
            for _, a := range auds {
                if v == strings.TrimSpace(a) { found = true; break }
            }
        case []interface{}:
            for _, a := range v {
                for _, want := range auds {
                    if fmt.Sprintf("%v", a) == strings.TrimSpace(want) { found = true; break }
                }
            }
        }
        if !found {
            return nil, fmt.Errorf("audience mismatch: %v", claims["aud"])
        }
    }

    return claims, nil
}
