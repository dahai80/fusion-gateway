package server

// #715: session-authed bridge to fusion-identity's tenant-scoped messaging
// plane (/api/messaging/*, identity#109). The shell session JWT is not an
// identity token, and identity requires a real user bearer (role resolved
// from tenant_members) — a service credential lands as role=service and
// breaks operator semantics. The bridge closes the gap for sessions that
// were issued THROUGH identity: the SSO and portal-code login flows already
// receive {access_token, refresh_token} from the identity exchange; instead
// of discarding them, the vault keeps the refresh token server-side (keyed
// by the shell session token hash) and mints short-lived access tokens on
// demand via POST /api/v1/auth/refresh. The browser never sees an identity
// token; a stolen session cookie alone yields nothing after session expiry.
//
// Local-password sessions carry no identity principal — no vault entry —
// and get an explicit 403 envelope (an upstream service-ticket minting
// primitive would extend coverage; tracked in the fusion-identity issue
// filed alongside this change).

import (
    "bytes"
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// identityVaultCap bounds the in-memory vault. Each entry holds one session's
// refresh token; entries are pruned at session expiry. 4096 concurrent shell
// sessions is far above any single-node deployment; overflow evicts the
// soonest-to-expire entries first (fail-safe, logged).
const identityVaultCap = 4096

// identityBridgeBodyCap caps the proxied request body. The messaging wire
// contract caps message content at 8000 chars; 1 MiB matches the SSE line
// hardening posture and leaves room for batch list endpoints.
const identityBridgeBodyCap = 1 << 20

// identityBridgeRespCap caps the proxied response body read.
const identityBridgeRespCap = 10 << 20

// identityAccessSlack refreshes the access token this many seconds before
// its real expiry so an in-flight hop never presents a token about to lapse.
const identityAccessSlack = 60 * time.Second

// identityBridgeTimeout bounds each upstream identity round-trip.
const identityBridgeTimeout = 15 * time.Second

type identityVaultEntry struct {
    mu           sync.Mutex
    tenant       string
    sub          string
    refreshToken string
    accessToken  string
    accessExp    time.Time
    sessionExp   int64
}

// identityVault holds per-shell-session identity refresh tokens and resolves
// them to fresh access tokens. Refresh is single-use with CAS rotation on
// the identity side — reuse of a rotated token revokes the whole family —
// so resolution is serialized per entry (per-session singleflight) and a
// 401 from identity drops the entry (the user must re-login via SSO).
type identityVault struct {
    mu      sync.Mutex
    entries map[string]*identityVaultEntry
    url     string
    client  *http.Client
    nowFn   func() time.Time
}

func newIdentityVault(url string) *identityVault {
    return &identityVault{
        entries: make(map[string]*identityVaultEntry),
        url:     strings.TrimRight(url, "/"),
        client: &http.Client{
            Timeout:   identityBridgeTimeout,
            Transport: httpx.TransportForBackend(config.BackendConfig{Type: "openai-compatible", BaseURL: url}),
        },
        nowFn: time.Now,
    }
}

func identityVaultKey(sessionToken string) string {
    sum := sha256.Sum256([]byte(sessionToken))
    return hex.EncodeToString(sum[:])
}

// Store records the identity refresh token for a freshly issued shell
// session. Called only from the SSO / portal-code login flows.
func (v *identityVault) Store(sessionToken, sub, tenant, refreshToken string, sessionExp int64) {
    if v == nil || sessionToken == "" || refreshToken == "" {
        return
    }
    key := identityVaultKey(sessionToken)
    v.mu.Lock()
    defer v.mu.Unlock()
    v.pruneLocked()
    if _, exists := v.entries[key]; !exists && len(v.entries) >= identityVaultCap {
        // Evict the entry with the soonest session expiry.
        var oldestKey string
        var oldest int64 = 1 << 62
        for k, e := range v.entries {
            if e.sessionExp < oldest {
                oldest = e.sessionExp
                oldestKey = k
            }
        }
        if oldestKey != "" {
            delete(v.entries, oldestKey)
            slog.Warn("identity vault overflow: evicted soonest-expiring session", "capacity", identityVaultCap)
        }
    }
    v.entries[key] = &identityVaultEntry{
        tenant:       tenant,
        sub:          sub,
        refreshToken: refreshToken,
        sessionExp:   sessionExp,
    }
}

// Drop removes the vault entry for a session (logout / revocation).
func (v *identityVault) Drop(sessionToken string) {
    if v == nil || sessionToken == "" {
        return
    }
    v.mu.Lock()
    delete(v.entries, identityVaultKey(sessionToken))
    v.mu.Unlock()
}

// pruneLocked removes expired sessions. Caller holds v.mu.
func (v *identityVault) pruneLocked() {
    now := v.nowFn().Unix()
    for k, e := range v.entries {
        if now >= e.sessionExp {
            delete(v.entries, k)
        }
    }
}

// AccessToken resolves a usable identity access token for the session.
// Serialized per entry: two concurrent messaging calls share one refresh
// round-trip (identity rotates refresh tokens single-use — a parallel
// refresh would trip the reuse detector and revoke the family).
func (v *identityVault) AccessToken(ctx context.Context, sessionToken string) (string, error) {
    if v == nil {
        return "", fmt.Errorf("identity vault unavailable")
    }
    v.mu.Lock()
    entry := v.entries[identityVaultKey(sessionToken)]
    v.mu.Unlock()
    if entry == nil {
        return "", fmt.Errorf("no identity principal for session")
    }
    entry.mu.Lock()
    defer entry.mu.Unlock()
    if entry.accessToken != "" && v.nowFn().Add(identityAccessSlack).Before(entry.accessExp) {
        return entry.accessToken, nil
    }
    if v.nowFn().Unix() >= entry.sessionExp {
        return "", fmt.Errorf("session expired")
    }
    reqBody, _ := json.Marshal(map[string]string{"refresh_token": entry.refreshToken})
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url+"/api/v1/auth/refresh", bytes.NewReader(reqBody))
    if err != nil {
        return "", err
    }
    req.Header.Set("Content-Type", "application/json")
    resp, err := v.client.Do(req)
    if err != nil {
        return "", fmt.Errorf("identity refresh unreachable: %w", err)
    }
    defer resp.Body.Close()
    data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
    if err != nil {
        return "", fmt.Errorf("identity refresh body: %w", err)
    }
    if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
        // Refresh rejected: family revoked or membership gone. Drop the
        // entry so the session stops replaying a dead refresh token.
        v.mu.Lock()
        delete(v.entries, identityVaultKey(sessionToken))
        v.mu.Unlock()
        return "", fmt.Errorf("identity refresh status %d", resp.StatusCode)
    }
    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("identity refresh status %d", resp.StatusCode)
    }
    var out identityExchangeResponse
    if err := json.Unmarshal(data, &out); err != nil {
        return "", fmt.Errorf("identity refresh decode: %w", err)
    }
    if out.AccessToken == "" || out.RefreshToken == "" {
        return "", fmt.Errorf("identity refresh returned empty token pair")
    }
    entry.accessToken = out.AccessToken
    entry.refreshToken = out.RefreshToken
    entry.accessExp = v.nowFn().Add(time.Duration(out.ExpiresIn) * time.Second)
    slog.Info("identity bridge: access token refreshed", "sub", entry.sub, "tenant", entry.tenant)
    return entry.accessToken, nil
}

// handleIdentityMessagingProxy forwards a session-authed /api/messaging/*
// call to fusion-identity with the vault-resolved user bearer. Only the
// hop headers it builds itself are sent — client-supplied Authorization and
// X-Fusion-* headers are never copied (same posture as the auth-portal
// proxy).
func (s *Server) handleIdentityMessagingProxy(w http.ResponseWriter, r *http.Request) {
    if s.identityVault == nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "bridge_disabled",
            "identity messaging bridge is not enabled", "", false)
        return
    }
    // The vault key is the session token the middleware just validated —
    // pulled from the request context, never from a raw client header.
    sessionToken := shellTokenFrom(r.Context())
    access, err := s.identityVault.AccessToken(r.Context(), sessionToken)
    if err != nil {
        slog.Info("identity bridge: bearer resolution failed", "path", r.URL.Path, "error", err)
        if strings.Contains(err.Error(), "no identity principal") {
            shell.WriteEnvelope(w, http.StatusForbidden, "identity_linked_login_required",
                "this session has no identity principal; log in via identity SSO to use messaging", "", false)
            return
        }
        if strings.Contains(err.Error(), "status 401") {
            // Refresh rejected: family revoked or membership gone. Drop the
            // entry so the session stops replaying a dead refresh token.
            s.identityVault.Drop(sessionToken)
            shell.WriteEnvelope(w, http.StatusUnauthorized, "identity_session_expired",
                "identity session expired; log in again via SSO", "", false)
            return
        }
        shell.WriteEnvelope(w, http.StatusBadGateway, "identity_unreachable",
            "identity refresh failed", "", true)
        return
    }

    upstreamPath := r.URL.Path
    if r.URL.RawQuery != "" {
        upstreamPath += "?" + r.URL.RawQuery
    }
    body, err := io.ReadAll(io.LimitReader(r.Body, identityBridgeBodyCap))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "bad_request",
            "unreadable request body", "", false)
        return
    }
    req, err := http.NewRequestWithContext(r.Context(), r.Method, s.identityVault.url+upstreamPath, bytes.NewReader(body))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusInternalServerError, "bridge_error",
            "failed to build upstream request", "", true)
        return
    }
    req.Header.Set("Authorization", "Bearer "+access)
    if len(body) > 0 {
        req.Header.Set("Content-Type", "application/json")
    }
    resp, err := s.identityVault.client.Do(req)
    if err != nil {
        slog.Warn("identity bridge: upstream round-trip failed", "path", r.URL.Path, "error", err)
        shell.WriteEnvelope(w, http.StatusBadGateway, "identity_unreachable",
            "identity messaging upstream failed", "", true)
        return
    }
    defer resp.Body.Close()
    data, err := io.ReadAll(io.LimitReader(resp.Body, identityBridgeRespCap))
    if err != nil {
        shell.WriteEnvelope(w, http.StatusBadGateway, "identity_unreachable",
            "identity messaging response read failed", "", true)
        return
    }
    if ct := resp.Header.Get("Content-Type"); ct != "" {
        w.Header().Set("Content-Type", ct)
    }
    w.WriteHeader(resp.StatusCode)
    _, _ = w.Write(data)
}
