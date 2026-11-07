// auth_portal.go — unified auth portal (#506): the /auth static SPA, the
// public identity auth proxy (explicit path allowlist, edge rate limit), and
// the one-time code handoff that lets fusion-site / fusion-web-shell redirect
// in and exchange a short-lived code for identity tokens server-side.
//
// Security posture:
//   - the portal surface is PUBLIC (no client key): browsers reach login,
//     register, and verify-email through the gateway instead of raw :11470;
//   - only the exact allowlisted auth endpoints proxy upstream, with the
//     Authorization header stripped — no credential confusion with the
//     service-token-injected /svc/identity surface;
//   - one-time codes are 32 bytes of crypto/rand, TTL-bounded (default 60s),
//     single-use, and bound to the redirect URI they were issued for;
//   - tokens POSTed to /auth/api/code are verified against the identity
//     JWKS (RS256, exp enforced) before a code is issued.
package server

import (
    "context"
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/binary"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "math/big"
    "net/http"
    "net/url"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"

    "github.com/golang-jwt/jwt/v5"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

const (
    authPortalJWKSRefresh = 10 * time.Minute
    authPortalMaxBody     = 64 * 1024
    authPortalCodeBytes   = 32
)

// authCodeGrant is one outstanding one-time code.
type authCodeGrant struct {
    AccessToken  string
    RefreshToken string
    ExpiresIn    int
    Username     string
    RedirectURI  string
    ExpiresAt    time.Time
}

// authPortal holds all portal runtime state. Constructed once at route
// registration; inert unless auth_portal.enabled=true.
type authPortal struct {
    cfg      config.AuthPortalConfig
    identity *url.URL
    httpClient *http.Client

    // identity JWKS cache (RS256 public keys by kid).
    jwksMu    sync.Mutex
    jwksKeys  map[string]interface{}
    jwksFetch time.Time

    // one-time code store + per-IP rate limiter.
    codesMu sync.Mutex
    codes   map[string]*authCodeGrant
    hitsMu  sync.Mutex
    hits    map[string][]time.Time
}

// registerAuthPortalRoutes mounts /auth (SPA), /auth/api/identity/<path>
// (allowlisted public auth proxy), and /auth/api/{code,code/exchange}
// (one-time handoff). No-op when auth_portal.enabled=false (fail-closed).
func (s *Server) registerAuthPortalRoutes(mux *http.ServeMux) {
    cfg := s.cfg.Config.AuthPortal
    if !cfg.Enabled {
        slog.Info("auth portal disabled")
        return
    }
    staticDir := cfg.StaticDir
    if staticDir == "" {
        staticDir = "web/auth"
    }
    identityURL := cfg.IdentityURL
    if identityURL == "" {
        identityURL = "http://127.0.0.1:11470"
    }
    target, err := url.Parse(identityURL)
    if err != nil || target.Scheme == "" || target.Host == "" {
        slog.Error("auth portal disabled: invalid identity_url", "url", identityURL)
        return
    }
    ttl := cfg.CodeTTLSecs
    if ttl <= 0 {
        ttl = 60
    }
    if ttl > 300 {
        ttl = 300
    }
    rate := cfg.RatePerMin
    if rate <= 0 {
        rate = 30
    }
    portal := &authPortal{
        cfg:        cfg,
        identity:   target,
        httpClient: &http.Client{Timeout: 15 * time.Second},
        codes:      make(map[string]*authCodeGrant),
        hits:       make(map[string][]time.Time),
    }
    if len(cfg.RedirectURIs) == 0 {
        // Allowlist empty: SPA serves, but code issuance always refuses
        // (fail-closed) — a deployment without clients has nothing to hand to.
        slog.Warn("auth portal: redirect_uris empty — code issuance disabled")
    }

    // Strip the /auth/ mount so the SPA root serves index.html (and assets
    // resolve relative to /auth/). The /auth/api/* subtrees below win by
    // ServeMux specificity over this catch-all.
    fileServer := http.StripPrefix("/auth/", http.FileServer(http.Dir(staticDir)))
    mux.Handle("/auth", http.RedirectHandler("/auth/", http.StatusMovedPermanently))
    mux.Handle("/auth/", portal.guard(fileServer.ServeHTTP))

    mux.Handle("/auth/api/identity/", portal.guard(portal.handleIdentityProxy))
    mux.Handle("/auth/api/code", portal.guard(portal.handleIssueCode))
    mux.Handle("/auth/api/code/exchange", portal.guard(portal.handleExchangeCode))

    s.authPortalWorker = lifecycle.Start(context.Background(), "auth_portal_janitor", portal.janitor)
    slog.Info("auth portal registered", "static_dir", staticDir, "identity", identityURL, "code_ttl_secs", ttl, "redirect_uris", len(cfg.RedirectURIs))
}

// guard applies the per-IP rate limiter to a portal handler.
func (p *authPortal) guard(next http.HandlerFunc) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !p.allow(clientIPFromRequest(r)) {
            slog.Warn("auth portal rate limited", "ip", clientIPFromRequest(r), "path", r.URL.Path)
            shell.WriteEnvelope(w, http.StatusTooManyRequests, "rate_limited",
                "too many auth requests; retry shortly", "auth_portal", true)
            return
        }
        next.ServeHTTP(w, r)
    })
}

// allow is a sliding-window per-IP limiter (window 1 minute).
func (p *authPortal) allow(ip string) bool {
    now := time.Now()
    cutoff := now.Add(-time.Minute)
    p.hitsMu.Lock()
    defer p.hitsMu.Unlock()
    kept := p.hits[ip][:0]
    for _, t := range p.hits[ip] {
        if t.After(cutoff) {
            kept = append(kept, t)
        }
    }
    if len(kept) >= p.cfg.RatePerMin {
        p.hits[ip] = kept
        return false
    }
    p.hits[ip] = append(kept, now)
    return true
}

// janitor purges expired codes and stale rate windows periodically. Runs as
// a lifecycle.Worker (bare-goroutine gate) so Shutdown cancels + joins it.
func (p *authPortal) janitor(ctx context.Context) {
    ticker := time.NewTicker(time.Duration(p.cfg.CodeTTLSecs) * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
        }
        now := time.Now()
        p.codesMu.Lock()
        for code, g := range p.codes {
            if g.ExpiresAt.Before(now) {
                delete(p.codes, code)
            }
        }
        p.codesMu.Unlock()
        p.hitsMu.Lock()
        cutoff := now.Add(-time.Minute)
        for ip, ts := range p.hits {
            kept := ts[:0]
            for _, t := range ts {
                if t.After(cutoff) {
                    kept = append(kept, t)
                }
            }
            if len(kept) == 0 {
                delete(p.hits, ip)
            } else {
                p.hits[ip] = kept
            }
        }
        p.hitsMu.Unlock()
    }
}

// identityProxyAllowlist is the exact set of upstream paths the portal may
// reach, with method. Anything else is 404 — the public surface never
// becomes a generic proxy.
var identityProxyAllowlist = map[string]string{
    "POST|/api/v1/auth/login":              "login",
    "POST|/api/v1/auth/register":           "register",
    "POST|/api/v1/auth/register-tenant":    "register-tenant",
    "POST|/api/v1/auth/verify-email":       "verify-email",
    "POST|/api/v1/auth/verify-email/resend": "verify-email-resend",
    // #549: self-service password recovery (identity #54). Credential-shaped
    // POST bodies, same handling as login; the Authorization strip in
    // handleIdentityProxy applies as everywhere else on this surface.
    "POST|/api/v1/auth/password/forgot": "password-forgot",
    "POST|/api/v1/auth/password/reset":  "password-reset",
    "GET|/.well-known/jwks.json":        "jwks",
}

// handleIdentityProxy forwards an allowlisted public auth call to identity.
// The Authorization header is stripped: callers authenticate with
// credentials in the body, never with bearer tokens on this surface.
func (p *authPortal) handleIdentityProxy(w http.ResponseWriter, r *http.Request) {
    upstreamPath := strings.TrimPrefix(r.URL.Path, "/auth/api/identity")
    if upstreamPath == "" {
        upstreamPath = "/"
    }
    key := r.Method + "|" + upstreamPath
    if _, ok := identityProxyAllowlist[key]; !ok {
        slog.Warn("auth portal proxy refused", "method", r.Method, "path", upstreamPath)
        shell.WriteEnvelope(w, http.StatusNotFound, "not_found",
            "no such auth endpoint", "auth_portal", false)
        return
    }
    p.proxyIdentity(w, r, upstreamPath)
}

// proxyIdentity performs the actual upstream round-trip.
func (p *authPortal) proxyIdentity(w http.ResponseWriter, r *http.Request, upstreamPath string) {
    body, err := io.ReadAll(io.LimitReader(r.Body, authPortalMaxBody))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "bad_request",
            "unreadable request body", "auth_portal", false)
        return
    }
    req, err := http.NewRequestWithContext(r.Context(), r.Method, p.identity.String()+upstreamPath, strings.NewReader(string(body)))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusInternalServerError, "proxy_error",
            "auth upstream request failed", "auth_portal", false)
        return
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Forwarded-For", clientIPFromRequest(r))
    resp, err := p.httpClient.Do(req)
    if err != nil {
        slog.Warn("auth portal upstream error", "path", upstreamPath, "error", err)
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "upstream_unavailable",
            "identity service unreachable; retry shortly", "auth_portal", true)
        return
    }
    defer resp.Body.Close()
    respBody, err := io.ReadAll(io.LimitReader(resp.Body, authPortalMaxBody))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusBadGateway, "upstream_error",
            "identity response unreadable", "auth_portal", true)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(resp.StatusCode)
    _, _ = w.Write(respBody)
}

type issueCodeRequest struct {
    AccessToken  string `json:"access_token"`
    RefreshToken string `json:"refresh_token"`
    ExpiresIn    int    `json:"expires_in"`
    RedirectURI  string `json:"redirect_uri"`
    Username     string `json:"username"`
}

type issueCodeResponse struct {
    Code        string `json:"code"`
    ExpiresIn   int    `json:"expires_in"`
    RedirectURI string `json:"redirect_uri"`
}

// handleIssueCode validates a fresh identity token pair and issues a
// one-time code bound to the allowlisted redirect URI.
func (p *authPortal) handleIssueCode(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        shell.WriteEnvelope(w, http.StatusMethodNotAllowed, "method_not_allowed",
            "POST required", "auth_portal", false)
        return
    }
    var req issueCodeRequest
    if err := json.NewDecoder(io.LimitReader(r.Body, authPortalMaxBody)).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "bad_request",
            "invalid JSON body", "auth_portal", false)
        return
    }
    redirectURI, ok := p.allowlistedRedirect(req.RedirectURI)
    if !ok {
        slog.Warn("auth portal code refused: redirect not allowlisted", "redirect", req.RedirectURI)
        shell.WriteEnvelope(w, http.StatusForbidden, "redirect_not_allowed",
            "redirect_uri is not in the allowlist", "auth_portal", false)
        return
    }
    if req.AccessToken == "" {
        shell.WriteEnvelope(w, http.StatusBadRequest, "bad_request",
            "access_token is required", "auth_portal", false)
        return
    }
    claims, err := p.verifyAccessToken(req.AccessToken)
    if err != nil {
        slog.Warn("auth portal code refused: token verification failed", "error", err)
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_token",
            "access token failed verification", "auth_portal", false)
        return
    }
    sub, _ := claims["sub"].(string)
    code, err := newOneTimeCode()
    if err != nil {
        slog.Error("auth portal code generation failed", "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "internal_error",
            "code generation failed", "auth_portal", false)
        return
    }
    ttl := p.cfg.CodeTTLSecs
    if ttl <= 0 {
        ttl = 60
    }
    grant := &authCodeGrant{
        AccessToken:  req.AccessToken,
        RefreshToken: req.RefreshToken,
        ExpiresIn:    req.ExpiresIn,
        Username:     req.Username,
        RedirectURI:  redirectURI,
        ExpiresAt:    time.Now().Add(time.Duration(ttl) * time.Second),
    }
    p.codesMu.Lock()
    p.codes[code] = grant
    p.codesMu.Unlock()
    slog.Info("auth portal code issued", "user", sub, "redirect", redirectURI, "ttl_secs", ttl)
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(issueCodeResponse{Code: code, ExpiresIn: ttl, RedirectURI: redirectURI})
}

type exchangeRequest struct {
    Code        string `json:"code"`
    RedirectURI string `json:"redirect_uri"`
}

// handleExchangeCode consumes a one-time code (single use, TTL, matching
// redirect) and returns the identity token pair for the server-side BFF.
func (p *authPortal) handleExchangeCode(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        shell.WriteEnvelope(w, http.StatusMethodNotAllowed, "method_not_allowed",
            "POST required", "auth_portal", false)
        return
    }
    var req exchangeRequest
    if err := json.NewDecoder(io.LimitReader(r.Body, authPortalMaxBody)).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "bad_request",
            "invalid JSON body", "auth_portal", false)
        return
    }
    p.codesMu.Lock()
    grant, ok := p.codes[req.Code]
    if ok {
        delete(p.codes, req.Code)
    }
    p.codesMu.Unlock()
    if !ok {
        slog.Warn("auth portal exchange refused: unknown or consumed code")
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_code",
            "code is unknown, expired, or already used", "auth_portal", false)
        return
    }
    if time.Now().After(grant.ExpiresAt) {
        slog.Warn("auth portal exchange refused: expired code")
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_code",
            "code is unknown, expired, or already used", "auth_portal", false)
        return
    }
    if redirectBase(req.RedirectURI) != grant.RedirectURI {
        slog.Warn("auth portal exchange refused: redirect mismatch", "got", req.RedirectURI, "want", grant.RedirectURI)
        shell.WriteEnvelope(w, http.StatusForbidden, "redirect_mismatch",
            "redirect_uri does not match the code grant", "auth_portal", false)
        return
    }
    slog.Info("auth portal code exchanged", "redirect", grant.RedirectURI)
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "access_token":  grant.AccessToken,
        "refresh_token": grant.RefreshToken,
        "expires_in":    grant.ExpiresIn,
        "username":      grant.Username,
    })
}

// allowlistedRedirect matches a redirect URI against the configured
// allowlist, comparing scheme://host/path only (queries are client state).
func (p *authPortal) allowlistedRedirect(raw string) (string, bool) {
    base := redirectBase(raw)
    if base == "" {
        return "", false
    }
    for _, allowed := range p.cfg.RedirectURIs {
        if redirectBase(allowed) == base {
            return base, true
        }
    }
    return "", false
}

// redirectBase extracts scheme://host/path, dropping query and fragment.
func redirectBase(raw string) string {
    u, err := url.Parse(strings.TrimSpace(raw))
    if err != nil || u.Scheme == "" || u.Host == "" {
        return ""
    }
    return u.Scheme + "://" + u.Host + u.Path
}

// verifyAccessToken validates an identity RS256 JWT against the cached
// JWKS, enforcing exp. alg confusion is rejected explicitly.
func (p *authPortal) verifyAccessToken(tokenStr string) (jwt.MapClaims, error) {
    keys, err := p.jwks()
    if err != nil {
        return nil, fmt.Errorf("jwks unavailable: %w", err)
    }
    token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
        if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
            return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
        }
        kid, _ := t.Header["kid"].(string)
        key, ok := keys[kid]
        if !ok {
            return nil, fmt.Errorf("unknown kid %q", kid)
        }
        return key, nil
    }, jwt.WithExpirationRequired())
    if err != nil {
        return nil, err
    }
    claims, ok := token.Claims.(jwt.MapClaims)
    if !ok || !token.Valid {
        return nil, fmt.Errorf("invalid token")
    }
    return claims, nil
}

// jwks fetches and caches the identity JWKS (10 min TTL).
func (p *authPortal) jwks() (map[string]interface{}, error) {
    p.jwksMu.Lock()
    defer p.jwksMu.Unlock()
    if p.jwksKeys != nil && time.Since(p.jwksFetch) < authPortalJWKSRefresh {
        return p.jwksKeys, nil
    }
    resp, err := p.httpClient.Get(p.identity.String() + "/.well-known/jwks.json")
    if err != nil {
        return nil, fmt.Errorf("fetch jwks: %w", err)
    }
    defer resp.Body.Close()
    var payload struct {
        Keys []struct {
            Kid string `json:"kid"`
            Kty string `json:"kty"`
            N   string `json:"n"`
            E   string `json:"e"`
        } `json:"keys"`
    }
    if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
        return nil, fmt.Errorf("decode jwks: %w", err)
    }
    keys := make(map[string]interface{})
    for _, k := range payload.Keys {
        if k.Kty != "RSA" || k.Kid == "" {
            continue
        }
        pub, err := rsaPublicKeyFromJWK(k.N, k.E)
        if err != nil {
            slog.Warn("auth portal skip jwk", "kid", k.Kid, "error", err)
            continue
        }
        keys[k.Kid] = pub
    }
    if len(keys) == 0 {
        return nil, fmt.Errorf("jwks has no usable RSA keys")
    }
    p.jwksKeys = keys
    p.jwksFetch = time.Now()
    return keys, nil
}

// rsaPublicKeyFromJWK decodes an RSA JWK modulus/exponent pair.
func rsaPublicKeyFromJWK(nStr, eStr string) (*rsa.PublicKey, error) {
    nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
    if err != nil {
        return nil, fmt.Errorf("decode n: %w", err)
    }
    eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
    if err != nil {
        return nil, fmt.Errorf("decode e: %w", err)
    }
    n := new(big.Int).SetBytes(nBytes)
    if n.Sign() <= 0 {
        return nil, fmt.Errorf("invalid modulus")
    }
    var eInt int
    if len(eBytes) < 4 {
        padded := make([]byte, 4-len(eBytes))
        eInt = int(binary.BigEndian.Uint32(append(padded, eBytes...)))
    } else {
        eInt = int(new(big.Int).SetBytes(eBytes).Int64())
    }
    return &rsa.PublicKey{N: n, E: eInt}, nil
}

// newOneTimeCode returns 32 bytes of crypto/rand as hex.
func newOneTimeCode() (string, error) {
    buf := make([]byte, authPortalCodeBytes)
    if _, err := rand.Read(buf); err != nil {
        return "", err
    }
    return hex.EncodeToString(buf), nil
}
