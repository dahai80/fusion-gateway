package server

// #517: shell session issue via identity SSO ticket (cloud-try Phase 2).
// fusion-site (fusion-identity session) mints a one-time ticket
// (POST /api/v1/auth/ticket, fusion-identity#48) and appends ?ticket= to the
// workbench URL; the shell host exchanges it HERE for a shell session +
// identity JWT — no second login. The gateway calls the identity exchange
// server-side (the ticket is the secret; it must not transit the browser
// twice).

import (
    "bytes"
    "context"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// identityExchangeResponse is the fusion-identity TokenResponse
// (POST /api/v1/auth/ticket/exchange).
type identityExchangeResponse struct {
    AccessToken  string `json:"access_token"`
    RefreshToken string `json:"refresh_token"`
    TokenType    string `json:"token_type"`
    ExpiresIn    int    `json:"expires_in"`
}

// identityClaims is the subset of the access-token JWT payload the session
// mapping reads (fusion-identity jwt_utils: sub/tid/role).
type identityClaims struct {
    Sub  string `json:"sub"`
    TID  string `json:"tid"`
    Role string `json:"role"`
}

// identityExchangeTimeout bounds the upstream round-trip; a hung identity
// service must not hold shell-login connections open.
const identityExchangeTimeout = 15 * time.Second

// identityBaseURL resolves the fusion-identity base URL for the shell SSO
// exchange: web_shell.identity_url, falling back to the auth-portal
// setting, then the default port.
func identityBaseURL(cfgShell, cfgPortal string) string {
    if v := strings.TrimSpace(cfgShell); v != "" {
        return v
    }
    if v := strings.TrimSpace(cfgPortal); v != "" {
        return v
    }
    return "http://127.0.0.1:11470"
}

// exchangeIdentityTicket performs the server-side ticket exchange.
func exchangeIdentityTicket(ctx context.Context, baseURL, ticket string) (*identityExchangeResponse, error) {
    client := &http.Client{
        Timeout:   identityExchangeTimeout,
        Transport: httpx.TransportForBackend(config.BackendConfig{Type: "openai-compatible", BaseURL: baseURL}),
    }
    body, _ := json.Marshal(map[string]string{"ticket": ticket})
    req, err := http.NewRequestWithContext(ctx, http.MethodPost,
        strings.TrimRight(baseURL, "/")+"/api/v1/auth/ticket/exchange", bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
    if err != nil {
        return nil, err
    }
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("identity exchange status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
    }
    var out identityExchangeResponse
    if err := json.Unmarshal(data, &out); err != nil {
        return nil, fmt.Errorf("identity exchange body: %w", err)
    }
    if out.AccessToken == "" {
        return nil, fmt.Errorf("identity exchange returned no access_token")
    }
    return &out, nil
}

// decodeIdentityClaims reads the JWT payload without signature verification:
// the token arrives on the identity→gateway hop we just performed over the
// trusted internal network, and the meter (#514) re-verifies it on every
// product call anyway. Verification here would require the shared HS256
// secret to be configured; when it is, TryQuota already enforces it.
func decodeIdentityClaims(token string) identityClaims {
    var c identityClaims
    parts := strings.Split(token, ".")
    if len(parts) != 3 {
        return c
    }
    raw, err := base64.RawURLEncoding.DecodeString(parts[1])
    if err != nil {
        return c
    }
    _ = json.Unmarshal(raw, &c)
    return c
}

// mapIdentityRoleToShell maps the identity tenant role onto the shell role
// model (admin/editor/viewer). Identity "admin" is the only elevated role —
// every other identity role (member, custom) lands on viewer so SSO can
// never grant shell write classes the identity never had.
func mapIdentityRoleToShell(role string) string {
    if strings.EqualFold(role, "admin") {
        return "admin"
    }
    return "viewer"
}

// handleShellSessionSSO: POST /api/v1/shell/session/sso {ticket}. Exchanges
// the one-time identity ticket server-side and issues the shell session +
// identity token pair. Replayed/expired/unknown tickets are 401.
func (s *Server) handleShellSessionSSO(w http.ResponseWriter, r *http.Request) {
    if s.shellSessions == nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "shell_disabled", "shell subsystem is not enabled", "", false)
        return
    }
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        Ticket string `json:"ticket"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil || strings.TrimSpace(req.Ticket) == "" {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be JSON with ticket", "", false)
        return
    }

    baseURL := identityBaseURL(s.cfg.Config.Shell.IdentityURL, s.cfg.Config.AuthPortal.IdentityURL)
    ex, err := exchangeIdentityTicket(r.Context(), baseURL, req.Ticket)
    if err != nil {
        slog.Warn("shell sso ticket exchange failed", "error", err)
        if s.shellAudit != nil {
            _ = s.shellAudit.Record("", "", shell.AuditClassAuth, http.MethodPost,
                "/api/v1/shell/session/sso", shell.AuditOutcomeDenied, 0)
        }
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_ticket",
            "ticket is invalid, expired, or already used", "", false)
        return
    }
    claims := decodeIdentityClaims(ex.AccessToken)
    if claims.Sub == "" {
        slog.Error("shell sso: identity token carries no sub")
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_ticket",
            "identity token is missing the subject claim", "", false)
        return
    }
    shellRole := mapIdentityRoleToShell(claims.Role)
    modules := s.cfg.Config.Shell.SSOModules
    if len(modules) == 0 {
        modules = []string{"*"}
    }

    token, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub:       claims.Sub,
        Roles:     []string{shellRole},
        Modules:   modules,
        TenantID:  claims.TID,
    })
    if err != nil {
        slog.Error("shell sso session issue failed", "sub", claims.Sub, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "session_issue_failed", "failed to issue session token", "", true)
        return
    }
    slog.Info("shell session issued via identity sso", "sub", claims.Sub, "tenant", claims.TID, "role", shellRole)
    if s.shellAudit != nil {
        _ = s.shellAudit.Record(claims.Sub, shellRole, shell.AuditClassAuth, http.MethodPost,
            "/api/v1/shell/session/sso", shell.AuditOutcomeOk, 0)
    }
    // Same HttpOnly session cookie as the password path (#220) so iframe
    // navigation authenticates; the identity JWT rides the BODY only (the
    // shell host stores it for metering headers) — never a cookie, so it
    // cannot leak to module iframes via the browser.
    http.SetCookie(w, &http.Cookie{
        Name:     shellSessionCookieName,
        Value:    token,
        Path:     "/",
        MaxAge:   int(s.shellSessions.TTL().Seconds()),
        HttpOnly: true,
        Secure:   r.TLS != nil,
        SameSite: http.SameSiteStrictMode,
    })
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "token":          token,
        "identity_token": ex.AccessToken,
        "username":       claims.Sub,
        "roles":          []string{shellRole},
        "tenant_id":      claims.TID,
        "token_type":     "bearer",
    })
}
