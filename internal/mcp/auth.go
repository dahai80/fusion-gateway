package mcp

import (
    "crypto/subtle"
    "log/slog"
    "net/http"
    "strings"
)

// #118: MCP-specific auth gate. MCP routes must not be reachable anonymously
// even when the main gateway auth chain is disabled (auth.enabled=false). The
// shared withMiddleware chain short-circuits to anonymous when auth is disabled
// or in passthrough mode, which would open /mcp/v1/call (an SSRF amplifier via
// forwardToNode). This gate is layered ON TOP of the shared chain on the
// shared-listener path, and is the SOLE gate on the dedicated-listener path.
//
// Credential resolution (first match wins, fail-closed):
//  1. mcp.token — a dedicated MCP bearer token (Authorization: Bearer <token>).
//     Preferred: isolates MCP access from inference API keys.
//  2. auth.master_key — admin-equivalent fallback when no mcp.token is set.
//     Lets an operator reuse the master key for MCP without a second secret.
// If both are empty, MCPEnabled is true, and the listener started anyway, every
// request is rejected (401) — config validation rejects this at load, but the
// gate defends in depth at request time too.
//
// The gate is constant-time on both credential comparisons.

// AuthConfig holds the credentials the MCP auth gate resolves against. Both
// fields may be empty (memory-only / disabled); the gate then rejects all
// requests. Set by the server wiring from config.MCP.Token + config.Auth.MasterKey.
type AuthConfig struct {
    Token     string
    MasterKey string
}

// AuthGate returns middleware that enforces the MCP credential independent of
// the main auth chain. next is reached only with a valid MCP token or master key.
func AuthGate(ac AuthConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            presented := extractMCPToken(r)
            if presented == "" {
                slog.Warn("MCP request missing bearer token", "path", r.URL.Path, "remote", r.RemoteAddr)
                writeMCPAuthError(w, "Missing MCP bearer token")
                return
            }
            if ac.Token != "" && subtle.ConstantTimeCompare([]byte(ac.Token), []byte(presented)) == 1 {
                slog.Debug("MCP token authenticated", "path", r.URL.Path)
                next.ServeHTTP(w, r)
                return
            }
            if ac.MasterKey != "" && subtle.ConstantTimeCompare([]byte(ac.MasterKey), []byte(presented)) == 1 {
                slog.Debug("MCP master-key authenticated", "path", r.URL.Path)
                next.ServeHTTP(w, r)
                return
            }
            slog.Warn("MCP request invalid token", "path", r.URL.Path, "remote", r.RemoteAddr)
            writeMCPAuthError(w, "Invalid MCP bearer token")
        })
    }
}

// extractMCPToken pulls the bearer credential from the Authorization header.
// Accepts "Bearer <token>" (preferred) and a bare token (tolerant of clients
// that send the key directly, matching the main APIKeyAuth extractAPIKey
// convention).
func extractMCPToken(r *http.Request) string {
    h := r.Header.Get("Authorization")
    if h == "" {
        return ""
    }
    h = strings.TrimSpace(h)
    if strings.HasPrefix(strings.ToLower(h), "bearer ") {
        return strings.TrimSpace(h[len("bearer "):])
    }
    return h
}

func writeMCPAuthError(w http.ResponseWriter, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusUnauthorized)
    _, _ = w.Write([]byte(`{"error":{"message":"` + msg + `","type":"mcp_auth_error"}}`))
}
