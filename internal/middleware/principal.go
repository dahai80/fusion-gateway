package middleware

// Principal model — 硬伤1 fix: unify three auth context systems (APIKey, OIDC, RBAC)
// into a single struct. Importers: auth.go, oidc.go, rbac.go populate the Principal;
// server.go and downstream code consume it via accessor helpers.
// Affected API: GetAuthKeyConfig/IsMasterKey/GetOIDCClaims/GetRBACRole/IsAdmin/CanWrite
// signatures unchanged — they now delegate to Principal instead of scattered context keys.
// User instruction: "所有审计发现都修复了吗？不管什么级别的？"

import (
    "context"
    "log/slog"

    "github.com/golang-jwt/jwt/v5"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type Principal struct {
    AuthMethod string
    KeyConfig  *config.AuthKeyConfig
    IsMaster   bool
    OIDCClaims jwt.MapClaims
    OIDCToken  string
    Role       Role
    Team       *TeamInfo
}

type principalKey string

const PrincipalKey principalKey = "principal"

func PrincipalFromContext(ctx context.Context) *Principal {
    p, _ := ctx.Value(PrincipalKey).(*Principal)
    return p
}

func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
    return context.WithValue(ctx, PrincipalKey, p)
}

func EnsurePrincipal(ctx context.Context) (context.Context, *Principal) {
    p := PrincipalFromContext(ctx)
    if p != nil {
        return ctx, p
    }
    p = &Principal{
        Role: RoleViewer,
    }
    return ContextWithPrincipal(ctx, p), p
}

func (p *Principal) Subject() string {
    if p.OIDCClaims != nil {
        if sub, ok := p.OIDCClaims["sub"].(string); ok {
            return sub
        }
    }
    if p.KeyConfig != nil && p.KeyConfig.Name != "" {
        return p.KeyConfig.Name
    }
    if p.IsMaster {
        return "master"
    }
    return "anonymous"
}

func (p *Principal) KeyName() string {
    if p.KeyConfig != nil && p.KeyConfig.Name != "" {
        return p.KeyConfig.Name
    }
    if p.IsMaster {
        return "master"
    }
    return "anonymous"
}

func (p *Principal) EffectiveRole() Role {
    if p.IsMaster {
        return RoleAdmin
    }
    if p.Role != "" {
        return p.Role
    }
    return RoleViewer
}

// Compatibility accessors — replace scattered context-key reads.
// Downstream code can migrate gradually; old helpers now delegate to Principal.

func GetAuthKeyConfig(ctx context.Context) *config.AuthKeyConfig {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return nil
    }
    return p.KeyConfig
}

func IsMasterKey(ctx context.Context) bool {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return false
    }
    return p.IsMaster
}

func GetOIDCClaims(ctx context.Context) jwt.MapClaims {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return nil
    }
    return p.OIDCClaims
}

func GetOIDCSubject(ctx context.Context) string {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return ""
    }
    return p.Subject()
}

func GetOIDCToken(ctx context.Context) string {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return ""
    }
    return p.OIDCToken
}

func GetRBACRole(ctx context.Context) Role {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return RoleViewer
    }
    return p.EffectiveRole()
}

func GetRBACTeam(ctx context.Context) *TeamInfo {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return nil
    }
    return p.Team
}

func IsAdmin(ctx context.Context) bool {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return false
    }
    return p.EffectiveRole() == RoleAdmin
}

func CanWrite(ctx context.Context) bool {
    p := PrincipalFromContext(ctx)
    if p == nil {
        return false
    }
    r := p.EffectiveRole()
    return r == RoleAdmin || r == RoleEditor
}

func init() {
    slog.Debug("principal model initialized — unified auth context")
}
