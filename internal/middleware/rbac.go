package middleware

// RBAC middleware for v0.5.0 Task #69 (Team/Org/RBAC).
// Importers: internal/server/server.go middleware chain.
// User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md specifies
// "internal/store/memory/teams.go — Team/Org CRUD. RBAC: admin/viewer/editor roles.
// Admin API extension: /teams, /orgs. Key association Team, cost aggregated by Team".
// Data schema: Role (admin/editor/viewer), TeamInfo (id/name/role/quota/allowed_models).
// API: RBACAuth middleware, context helpers GetRBACRole/GetRBACTeam/IsAdmin/CanWrite.

import (
    "context"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type Role string

const (
    RoleAdmin  Role = "admin"
    RoleEditor Role = "editor"
    RoleViewer Role = "viewer"
)

type TeamInfo struct {
    ID            string   `json:"id"`
    Name          string   `json:"name"`
    Role          Role     `json:"role"`
    QuotaLimit    float64  `json:"quota_limit,omitempty"`
    QuotaUsed     float64  `json:"quota_used,omitempty"`
    AllowedModels []string `json:"allowed_models,omitempty"`
}

type rbacContextKey string

const (
    RBACRoleKey rbacContextKey = "rbac_role"
    RBACTeamKey rbacContextKey = "rbac_team"
)

func RBACAuth(cfg *config.RBACConfig, teamCfg *config.TeamConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !cfg.Enabled {
                next.ServeHTTP(w, r)
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

            if IsMasterKey(r.Context()) {
                role = RoleAdmin
            }

            ctx := context.WithValue(r.Context(), RBACRoleKey, role)

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
                    ctx = context.WithValue(ctx, RBACTeamKey, team)
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

func GetRBACRole(ctx context.Context) Role {
    role, _ := ctx.Value(RBACRoleKey).(Role)
    if role == "" {
        return RoleViewer
    }
    return role
}

func GetRBACTeam(ctx context.Context) *TeamInfo {
    team, _ := ctx.Value(RBACTeamKey).(*TeamInfo)
    return team
}

func IsAdmin(ctx context.Context) bool {
    return GetRBACRole(ctx) == RoleAdmin
}

func CanWrite(ctx context.Context) bool {
    role := GetRBACRole(ctx)
    return role == RoleAdmin || role == RoleEditor
}
