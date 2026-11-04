package server

// #493 network tiers: the lan hard auth gate. In lan tier a request without
// ANY credential is rejected 401 before a handler runs — including routes
// whose own gate is weaker or config-disabled. Surfaces anonymous by design
// (probes, login, signature-trust) are allowlisted below. The guard checks
// credential PRESENCE only; the route's own gate still does the real
// validation. See docs/network-tiers.md.

import (
    "crypto/subtle"
    "log/slog"
    "net/http"
)

// lanPublicAllowlist: path prefixes reachable without a credential in lan
// tier. Keep this list minimal — every entry is an intentional anonymous
// surface, and the startup log prints it for audit.
var lanPublicAllowlist = []string{
    "/health",
    "/healthz",
    "/readyz",
    "/livez",
    "/admin/", // SPA static + admin login; admin API carries its own gates
    "/api/v1/shell/session", // shell login
    "/gateway/v1/managed-settings", // Ed25519-signature trust surface
}

// lanGuardHandler returns the guard wrapper in lan tier, nil otherwise
// (local tier behavior identical to today, regression-tested).
func (s *Server) lanGuardHandler() func(http.Handler) http.Handler {
    tier := s.cfg.Config.Network.Tier
    if tier == "" {
        tier = "local"
    }
    if tier != "lan" {
        return nil
    }
    return s.lanGuard
}

// lanGuard wraps the mux in lan tier.
func (s *Server) lanGuard(next http.Handler) http.Handler {
    slog.Info("lan tier armed: hard auth gate active",
        "anonymous_allowlist", lanPublicAllowlist)
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if s.lanAnonymousAllowed(r) {
            next.ServeHTTP(w, r)
            return
        }
        if s.lanHasCredential(r) {
            next.ServeHTTP(w, r)
            return
        }
        slog.Warn("lan tier rejected unauthenticated request", "path", r.URL.Path, "remote", r.RemoteAddr)
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusUnauthorized)
        _, _ = w.Write([]byte(`{"error":{"message":"authentication required in lan network tier","type":"auth_error"}}`))
    })
}

// lanAnonymousAllowed matches the explicit allowlist (prefix semantics;
// longest prefixes like /healthz sit beside /health deliberately).
func (s *Server) lanAnonymousAllowed(r *http.Request) bool {
    for _, p := range lanPublicAllowlist {
        if r.URL.Path == p || (len(r.URL.Path) > len(p) && r.URL.Path[:len(p)] == p) {
            return true
        }
    }
    return false
}

// lanHasCredential reports whether the request carries ANY credential shape:
// an API key (Bearer / X-API-Key), an admin session cookie, or a shell
// session cookie. Presence only — validation happens in the route's own
// gate.
func (s *Server) lanHasCredential(r *http.Request) bool {
    if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
        return true
    }
    if _, err := r.Cookie("admin_token"); err == nil {
        return true
    }
    if _, err := r.Cookie(shellSessionCookieName); err == nil {
        return true
    }
    return false
}

// serviceKeyAllowed (#493): a registered per-module service key
// (web_shell.service_keys, overlayed from FUSION_SERVICE_KEY_<MODULE> envs)
// authenticates that module's own manifest registration. Constant-time
// compare; a key registered for module A never authenticates module B.
func (s *Server) serviceKeyAllowed(r *http.Request, module string) bool {
    if module == "" || s.cfg == nil || len(s.cfg.Config.Shell.ServiceKeys) == 0 {
        return false
    }
    registered, ok := s.cfg.Config.Shell.ServiceKeys[module]
    if !ok || registered == "" {
        return false
    }
    token := shellBearerToken(r)
    if token == "" {
        return false
    }
    return subtle.ConstantTimeCompare([]byte(token), []byte(registered)) == 1
}
