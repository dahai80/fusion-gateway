package middleware

import (
    "log/slog"
    "net/http"
    "strings"
)

// #262 K2: scope enforcement for user-issued API keys.
//
// A key's Scopes is a module×action matrix, e.g.:
//   ["chat", "rag:embed", "cowork:runs:create", "*"]
// "*" grants every module. A bare module ("chat") grants the whole module.
// "module:..." (any action suffix, including nested ones like
// "cowork:runs:create") grants that module. Empty Scopes (legacy keys,
// config-static keys) pass — enforcement is opt-in per key, never a
// blanket downgrade of existing credentials.
//
// Enforcement maps the request path to an inference module (chat/rag/
// design/agent, see inferenceModule in identity_auth.go) and rejects with
// 403 permission_error when the resolved key holds scopes but none grants
// that module. Paths that map to no module (admin, /api/v1 management
// surface, health) are out of scope here — they are gated by their own
// auth chains (withAdminOnly / master key).

// ScopeAllowsModule reports whether the scope list grants the module.
func ScopeAllowsModule(scopes []string, module string) bool {
    if module == "" {
        return true
    }
    for _, scope := range scopes {
        if scope == "*" || scope == module {
            return true
        }
        if strings.HasPrefix(scope, module+":") {
            return true
        }
    }
    return false
}

// ScopeEnforce rejects /v1/* inference calls whose authenticated key holds a
// non-empty scope matrix that does not grant the request's module.
func ScopeEnforce() func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            p := PrincipalFromContext(r.Context())
            if p == nil || p.IsMaster || p.KeyConfig == nil || len(p.KeyConfig.Scopes) == 0 {
                next.ServeHTTP(w, r)
                return
            }
            if p.AuthMethod != "apikey" {
                next.ServeHTTP(w, r)
                return
            }
            module := inferenceModule(r)
            if module == "" {
                next.ServeHTTP(w, r)
                return
            }
            if ScopeAllowsModule(p.KeyConfig.Scopes, module) {
                next.ServeHTTP(w, r)
                return
            }
            slog.Warn("api key scope denied module",
                "key", p.KeyConfig.Name, "module", module,
                "scopes", strings.Join(p.KeyConfig.Scopes, ","), "path", r.URL.Path)
            http.Error(w, `{"error":{"message":"API key not authorized for this module","type":"permission_error"}}`, http.StatusForbidden)
        })
    }
}
