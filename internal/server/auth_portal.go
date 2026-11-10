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
    "errors"
    "fmt"
    "io"
    "log/slog"
    "math/big"
    "net"
    "net/http"
    "net/url"
    "os"
    "strings"
    "sync"
    "sync/atomic"
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

    // #602: bounds for the identity hop. identityProxyTimeout keeps the
    // round-trip under the 15s Client.Timeout; identityBreakerThreshold
    // consecutive timeouts open the breaker for identityBreakerCooldown.
    identityProxyTimeout      = 15 * time.Second
    identityBreakerThreshold  = 3
    identityBreakerCooldown   = 30 * time.Second
)

// newIdentityHTTPClient builds the dedicated client for the identity hop
// (#602). The portal previously used &http.Client{Timeout: 15s} with a nil
// Transport — i.e. the SHARED http.DefaultTransport — so identity's stall
// left half-dead keep-alive connections in a pool every other gateway
// component dials through: each reused conn hung until client timeout even
// after identity recovered, and only a gateway restart drained the pool.
// A dedicated transport with short idle timeout + response-header bound
// contains the blast radius; forwardIdentity drops idle conns and retries
// once on a fresh connection when a round-trip fails.
func newIdentityHTTPClient() *http.Client {
    return &http.Client{
        Timeout: identityProxyTimeout,
        Transport: &http.Transport{
            Proxy:                 http.ProxyFromEnvironment,
            DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
            MaxIdleConns:          20,
            MaxIdleConnsPerHost:   8,
            MaxConnsPerHost:       16,
            IdleConnTimeout:       30 * time.Second,
            ResponseHeaderTimeout: 5 * time.Second,
            TLSHandshakeTimeout:   5 * time.Second,
            ExpectContinueTimeout: time.Second,
        },
    }
}

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

    // #590: failed-login lockout for the portal login proxy — the portal is
    // now the primary public auth oracle and only had a flat per-IP rate
    // limit. Reuses the #587 throttle (5 fails / 5 min per IP+username →
    // 60s lockout, success clears).
    loginThrottle *shellLoginThrottle

    // #603: the shared single-use CAPTCHA store (shell.captcha config gates
    // it). Nil = plane disabled, the portal login forwards without a
    // challenge (the shell frontend contract).
    captcha *shellCaptchaStore

    // #602: tiny fail-fast breaker for the identity hop. identityBreakerFails
    // counts consecutive timeout-class failures; at identityBreakerThreshold
    // the breaker opens for identityBreakerCooldown and login attempts get an
    // immediate 503 instead of queueing on a hung upstream. Any success
    // closes it. UnixNano of the reopen instant (0 = closed), atomic so the
    // read path needs no lock.
    identityBreakerFails    atomic.Int64
    identityBreakerOpenUntil atomic.Int64
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
        httpClient: newIdentityHTTPClient(),
        codes:         make(map[string]*authCodeGrant),
        hits:          make(map[string][]time.Time),
        loginThrottle: newShellLoginThrottle(),
        captcha:       s.shellCaptcha,
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
    // #603: challenge issuance for the portal login form — unauthenticated
    // by contract (fetched before any session exists), always no-store.
    mux.Handle("GET /auth/api/captcha", http.HandlerFunc(portal.handleAuthPortalCaptcha))

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
    // #590: the login proxy is a credential oracle — route it through the
    // failed-login lockout before the upstream round-trip.
    if key == "POST|/api/v1/auth/login" {
        p.handlePortalLogin(w, r, upstreamPath)
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
    p.forwardIdentity(w, r, upstreamPath, body)
}

// handlePortalLogin wraps the login proxy with the #587-style lockout: the
// lockout check runs BEFORE the upstream call so a locked source gets 429
// even for a correct password (no oracle left during lockout). Upstream 401
// (bad credentials) and 422 (malformed credentials) count as failures; 200
// clears the counters so a legitimate user's typo never accumulates.
func (p *authPortal) handlePortalLogin(w http.ResponseWriter, r *http.Request, upstreamPath string) {
    body, err := io.ReadAll(io.LimitReader(r.Body, authPortalMaxBody))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "bad_request",
            "unreadable request body", "auth_portal", false)
        return
    }
    var req struct {
        Username string `json:"username"`
    }
    _ = json.Unmarshal(body, &req)
    // #603: verify the challenge BEFORE the lockout counter and the
    // upstream credential check — a captcha failure is a bot signal, so it
    // must not consume a lockout attempt (#592: never feed the counter) and
    // must not reach identity at all.
    if p.captcha != nil {
        var cap struct {
            CaptchaID   string `json:"captcha_id"`
            CaptchaCode string `json:"captcha_code"`
        }
        _ = json.Unmarshal(body, &cap)
        if res := p.captcha.verify(cap.CaptchaID, cap.CaptchaCode); res != "" {
            slog.Warn("auth portal login captcha rejected", "code", res, "username", req.Username)
            shell.WriteEnvelope(w, http.StatusBadRequest, res,
                "captcha verification failed; refresh and retry", "auth_portal", true)
            return
        }
    }
    ip := clientIPFromRequest(r)
    if p.loginThrottle == nil {
        p.loginThrottle = newShellLoginThrottle()
    }
    if ok, wait := p.loginThrottle.allowed("ip:"+ip, "user:"+req.Username); !ok {
        slog.Warn("auth portal login throttled: source locked out", "ip", ip, "username", req.Username, "retry_after", wait.String())
        shell.WriteEnvelope(w, http.StatusTooManyRequests, "rate_limited",
            "too many failed logins; retry later", "auth_portal", true)
        return
    }
    status := p.forwardIdentity(w, r, upstreamPath, body)
    switch status {
    case http.StatusOK:
        p.loginThrottle.recordSuccess(ip, req.Username)
    case http.StatusUnauthorized, http.StatusUnprocessableEntity:
        p.loginThrottle.recordFailure(ip, req.Username)
    }
}

// identityTimeoutErr reports whether the round-trip failed with a
// timeout/connection class error — the poisoned-keep-alive shape from
// #602 (peer accepts but never responds) as opposed to a local bug.
func identityTimeoutErr(err error) bool {
    if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
        return true
    }
    var ne net.Error
    if errors.As(err, &ne) && ne.Timeout() {
        return true
    }
    msg := err.Error()
    return strings.Contains(msg, "connection reset") ||
        strings.Contains(msg, "broken pipe") ||
        strings.Contains(msg, "EOF")
}

// identityBreakerTripped reports (and lazily clears) an open breaker.
func (p *authPortal) identityBreakerTripped() bool {
    until := p.identityBreakerOpenUntil.Load()
    if until == 0 {
        return false
    }
    if time.Now().UnixNano() >= until {
        p.identityBreakerOpenUntil.Store(0)
        p.identityBreakerFails.Store(0)
        return false
    }
    return true
}

// forwardIdentity performs the upstream round-trip with an in-memory body
// and writes the upstream response through; returns the upstream status so
// callers can react to credential outcomes (#590).
//
// #602 hardening: a timeout/reset-class failure on the first attempt drops
// the transport's idle connections (the half-dead keep-alive pool that
// survived identity's recovery) and retries ONCE on a fresh connection.
// Timeout-class final failures surface as 504 and count toward the
// fail-fast breaker; other transport failures stay 503.
func (p *authPortal) forwardIdentity(w http.ResponseWriter, r *http.Request, upstreamPath string, body []byte) int {
    if p.identityBreakerTripped() {
        slog.Warn("auth portal identity breaker open — failing fast", "path", upstreamPath)
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "upstream_unavailable",
            "identity temporarily unavailable; retry shortly", "auth_portal", true)
        return http.StatusServiceUnavailable
    }
    for attempt := 1; ; attempt++ {
        req, err := http.NewRequestWithContext(r.Context(), r.Method, p.identity.String()+upstreamPath, strings.NewReader(string(body)))
        if err != nil {
            shell.WriteEnvelope(w, http.StatusInternalServerError, "proxy_error",
                "auth upstream request failed", "auth_portal", false)
            return http.StatusInternalServerError
        }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("X-Forwarded-For", clientIPFromRequest(r))
        resp, err := p.httpClient.Do(req)
        if err != nil {
            slog.Warn("auth portal upstream error", "path", upstreamPath, "attempt", attempt, "error", err)
            if attempt == 1 && identityTimeoutErr(err) {
                // Drop the poisoned idle pool, then retry on a fresh conn.
                if tr, ok := p.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
                    tr.CloseIdleConnections()
                }
                continue
            }
            status := http.StatusServiceUnavailable
            code := "upstream_unavailable"
            if identityTimeoutErr(err) {
                status = http.StatusGatewayTimeout
                code = "upstream_timeout"
                fails := p.identityBreakerFails.Add(1)
                if fails >= identityBreakerThreshold {
                    p.identityBreakerOpenUntil.Store(time.Now().Add(identityBreakerCooldown).UnixNano())
                    p.identityBreakerFails.Store(0)
                    slog.Warn("auth portal identity breaker opened", "consecutive_timeouts", fails, "cooldown", identityBreakerCooldown.String())
                }
            }
            shell.WriteEnvelope(w, status, code,
                "identity service unreachable; retry shortly", "auth_portal", true)
            return status
        }
        defer resp.Body.Close()
        respBody, err := io.ReadAll(io.LimitReader(resp.Body, authPortalMaxBody))
        if err != nil {
            shell.WriteEnvelope(w, http.StatusBadGateway, "upstream_error",
                "identity response unreadable", "auth_portal", true)
            return http.StatusBadGateway
        }
        p.identityBreakerFails.Store(0)
        p.identityBreakerOpenUntil.Store(0)
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(resp.StatusCode)
        _, _ = w.Write(respBody)
        return resp.StatusCode
    }
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

// handleAuthPortalCaptcha (#603): GET /auth/api/captcha — challenge issuance
// for the portal login form, same store and shape as the shell plane (#592).
// Unauthenticated by contract (fetched before any session exists).
func (p *authPortal) handleAuthPortalCaptcha(w http.ResponseWriter, r *http.Request) {
    serveCaptchaChallenge(w, p.captcha)
}
