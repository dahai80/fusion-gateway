package middleware

// RBAC middleware for v0.5.0 Task #69 (Team/Org/RBAC).
// Importers: internal/server/server.go middleware chain.
// User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md specifies
// "internal/store/memory/teams.go — Team/Org CRUD. RBAC: admin/viewer/editor roles.
// Admin API extension: /teams, /orgs. Key association Team, cost aggregated by Team".
// Data schema: Role (admin/editor/viewer), TeamInfo (id/name/role/quota/allowed_models).
// API: RBACAuth middleware, context helpers GetRBACRole/GetRBACTeam/IsAdmin/CanWrite.

import (
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type Role string

const (
    RoleAdmin     Role = "admin"
    RoleEditor    Role = "editor"
    RoleViewer    Role = "viewer"
    RoleInference Role = "inference"
)

type TeamInfo struct {
    ID            string   `json:"id"`
    Name          string   `json:"name"`
    Role          Role     `json:"role"`
    QuotaLimit    float64  `json:"quota_limit,omitempty"`
    QuotaUsed     float64  `json:"quota_used,omitempty"`
    AllowedModels []string `json:"allowed_models,omitempty"`
}

func RBACAuth(cfg *config.RBACConfig, teamCfg *config.TeamConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 硬伤1 fix: populate Principal instead of separate context keys
            ctx, p := EnsurePrincipal(r.Context())

            // withAdminOnly may have already bridged an admin-login JWT into a
            // Principal{AuthMethod:"admin-jwt", Role:RoleAdmin}. RBAC derives
            // role from OIDC claims / default role for the API-key path; for an
            // already-bridged admin identity it must NOT overwrite the role
            // (which would drop RoleAdmin back to RoleViewer → admin route 403).
            if p.AuthMethod == "admin-jwt" {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            if !cfg.Enabled {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            role := Role(cfg.DefaultRole)
            if role == "" {
                role = RoleViewer
            }

            claims := GetOIDCClaims(r.Context())
            if claims != nil {
                if roleClaim, ok := claims["role"].(string); ok {
                    r := Role(strings.ToLower(roleClaim))
                    if r == RoleAdmin || r == RoleEditor || r == RoleViewer {
                        role = r
                    }
                }
                if groupsClaim, ok := claims["groups"].([]interface{}); ok {
                    for _, g := range groupsClaim {
                        group, _ := g.(string)
                        if group == "fusion-admin" || group == "admins" {
                            role = RoleAdmin
                            break
                        }
                        if group == "fusion-editor" || group == "editors" {
                            if role != RoleAdmin {
                                role = RoleEditor
                            }
                        }
                    }
                }
            }

            // RR3 (audit P0): the inference MasterKey gets the inference role,
            // NOT RoleAdmin. Previously this mapped master to admin, so a leaked
            // inference key could reach /admin/teams|orgs|gc|config-reload via
            // withAdminOnly→IsAdmin→EffectiveRole (which also mapped master to
            // admin). The inference and management planes must stay separated:
            // an inference key authenticates inference only; admin surface
            // requires an admin JWT (admin.Handler.requireAdminRole, separate).
            // The master key's inference-side bypasses (CheckModelAllowlist,
            // CheckBackendAccess, rate-limit) read p.IsMaster directly and are
            // unaffected — those are inference privileges, not admin ones.
            if p.IsMaster {
                role = RoleInference
            }

            p.Role = role

            if teamCfg != nil && teamCfg.Enabled {
                teamID := teamCfg.DefaultTeam
                if claims != nil {
                    if tid, ok := claims["team"].(string); ok && tid != "" {
                        teamID = tid
                    }
                }
                if teamID != "" {
                    team := &TeamInfo{
                        ID:   teamID,
                        Name: teamID,
                        Role: role,
                    }
                    p.Team = team
                    // #150 Gap1: attach the OIDC/RBAC-derived tenant to ctx so
                    // outbound X-Fusion-Tenant is stamped on upstream requests.
                    // Only fill when auth did not already bind a credential-
                    // derived tenant (the key->team binding is the stronger,
                    // credential-backed identity; default_team is config-based).
                    if adapter.TenantFromContext(ctx) == "" {
                        ctx = adapter.WithTenant(ctx, teamID)
                    }
                    slog.Debug("rbac team assigned", "team", teamID, "role", string(role))
                }
            }

            if role == RoleViewer && isMutationMethod(r.Method) {
                slog.Warn("rbac denied: viewer cannot mutate", "method", r.Method, "path", r.URL.Path)
                http.Error(w, `{"error":{"message":"Insufficient permissions","type":"rbac_error"}}`, http.StatusForbidden)
                return
            }

            slog.Debug("rbac check passed", "role", string(role), "path", r.URL.Path)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func isMutationMethod(method string) bool {
    return method == http.MethodPost || method == http.MethodPut ||
        method == http.MethodPatch || method == http.MethodDelete
}
