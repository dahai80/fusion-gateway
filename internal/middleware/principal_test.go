package middleware

import (
    "context"
    "log/slog"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/golang-jwt/jwt/v5"
)

func TestPrincipalFromContext_Nil(t *testing.T) {
    p := PrincipalFromContext(context.Background())
    if p != nil {
        t.Error("expected nil principal")
    }
}

func TestPrincipalFromContext_Present(t *testing.T) {
    p := &Principal{AuthMethod: "apikey"}
    ctx := ContextWithPrincipal(context.Background(), p)
    got := PrincipalFromContext(ctx)
    if got == nil || got.AuthMethod != "apikey" {
        t.Error("expected apikey auth method")
    }
}

func TestEnsurePrincipal_New(t *testing.T) {
    ctx, p := EnsurePrincipal(context.Background())
    if p == nil {
        t.Fatal("expected non-nil principal")
    }
    if p.Role != RoleViewer {
        t.Errorf("expected viewer role, got %s", p.Role)
    }
    got := PrincipalFromContext(ctx)
    if got != p {
        t.Error("principal not stored in context")
    }
}

func TestEnsurePrincipal_Existing(t *testing.T) {
    existing := &Principal{AuthMethod: "oidc", Role: RoleAdmin}
    ctx := ContextWithPrincipal(context.Background(), existing)
    ctx2, p := EnsurePrincipal(ctx)
    if p != existing {
        t.Error("should return existing principal")
    }
    if ctx2 != ctx {
        t.Error("context should be unchanged")
    }
}

func TestPrincipal_Subject_OIDC(t *testing.T) {
    p := &Principal{
        OIDCClaims: jwt.MapClaims{"sub": "user123"},
    }
    if p.Subject() != "user123" {
        t.Errorf("expected user123, got %s", p.Subject())
    }
}

func TestPrincipal_Subject_KeyName(t *testing.T) {
    p := &Principal{
        KeyConfig: &config.AuthKeyConfig{Name: "mykey"},
    }
    if p.Subject() != "mykey" {
        t.Errorf("expected mykey, got %s", p.Subject())
    }
}

func TestPrincipal_Subject_Master(t *testing.T) {
    p := &Principal{IsMaster: true}
    if p.Subject() != "master" {
        t.Errorf("expected master, got %s", p.Subject())
    }
}

func TestPrincipal_Subject_Anonymous(t *testing.T) {
    p := &Principal{}
    if p.Subject() != "anonymous" {
        t.Errorf("expected anonymous, got %s", p.Subject())
    }
}

func TestPrincipal_KeyName(t *testing.T) {
    t.Run("with_key_config", func(t *testing.T) {
        p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "mykey"}}
        if p.KeyName() != "mykey" {
            t.Errorf("expected mykey, got %s", p.KeyName())
        }
    })
    t.Run("master", func(t *testing.T) {
        p := &Principal{IsMaster: true}
        if p.KeyName() != "master" {
            t.Errorf("expected master, got %s", p.KeyName())
        }
    })
    t.Run("anonymous", func(t *testing.T) {
        p := &Principal{}
        if p.KeyName() != "anonymous" {
            t.Errorf("expected anonymous, got %s", p.KeyName())
        }
    })
}

func TestPrincipal_EffectiveRole(t *testing.T) {
    // RR3 (audit P0): the inference MasterKey maps to the inference role, NOT
    // RoleAdmin. Previously master_is_admin asserted master→admin, which let a
    // leaked inference key reach /admin/* via withAdminOnly→IsAdmin. Inference
    // and management planes are now separated; admin requires an admin JWT.
    t.Run("master_is_inference_not_admin", func(t *testing.T) {
        p := &Principal{IsMaster: true}
        if p.EffectiveRole() != RoleInference {
            t.Errorf("expected inference role for master key, got %s", p.EffectiveRole())
        }
        if p.EffectiveRole() == RoleAdmin {
            t.Error("master key must NOT map to admin role (RR3 plane crossing)")
        }
    })
    t.Run("explicit_role", func(t *testing.T) {
        p := &Principal{Role: RoleEditor}
        if p.EffectiveRole() != RoleEditor {
            t.Errorf("expected editor, got %s", p.EffectiveRole())
        }
    })
    t.Run("default_viewer", func(t *testing.T) {
        p := &Principal{}
        if p.EffectiveRole() != RoleViewer {
            t.Errorf("expected viewer, got %s", p.EffectiveRole())
        }
    })
}

func TestGetAuthKeyConfig(t *testing.T) {
    t.Run("nil_principal", func(t *testing.T) {
        cfg := GetAuthKeyConfig(context.Background())
        if cfg != nil {
            t.Error("expected nil")
        }
    })
    t.Run("with_key_config", func(t *testing.T) {
        keyCfg := &config.AuthKeyConfig{Name: "test"}
        p := &Principal{KeyConfig: keyCfg}
        ctx := ContextWithPrincipal(context.Background(), p)
        got := GetAuthKeyConfig(ctx)
        if got != keyCfg {
            t.Error("expected key config")
        }
    })
}

func TestIsMasterKey(t *testing.T) {
    t.Run("nil_principal", func(t *testing.T) {
        if IsMasterKey(context.Background()) {
            t.Error("expected false")
        }
    })
    t.Run("master", func(t *testing.T) {
        p := &Principal{IsMaster: true}
        ctx := ContextWithPrincipal(context.Background(), p)
        if !IsMasterKey(ctx) {
            t.Error("expected true")
        }
    })
}

func TestGetOIDCClaims(t *testing.T) {
    t.Run("nil_principal", func(t *testing.T) {
        if GetOIDCClaims(context.Background()) != nil {
            t.Error("expected nil")
        }
    })
    t.Run("with_claims", func(t *testing.T) {
        claims := jwt.MapClaims{"sub": "user1"}
        p := &Principal{OIDCClaims: claims}
        ctx := ContextWithPrincipal(context.Background(), p)
        got := GetOIDCClaims(ctx)
        if got["sub"] != "user1" {
            t.Error("expected sub claim")
        }
    })
}

func TestGetOIDCSubject(t *testing.T) {
    p := &Principal{OIDCClaims: jwt.MapClaims{"sub": "oidc-user"}}
    ctx := ContextWithPrincipal(context.Background(), p)
    if GetOIDCSubject(ctx) != "oidc-user" {
        t.Errorf("expected oidc-user, got %s", GetOIDCSubject(ctx))
    }
}

func TestGetOIDCToken(t *testing.T) {
    p := &Principal{OIDCToken: "token123"}
    ctx := ContextWithPrincipal(context.Background(), p)
    if GetOIDCToken(ctx) != "token123" {
        t.Errorf("expected token123, got %s", GetOIDCToken(ctx))
    }
}

func TestGetRBACTeam(t *testing.T) {
    t.Run("nil_principal", func(t *testing.T) {
        if GetRBACTeam(context.Background()) != nil {
            t.Error("expected nil")
        }
    })
    t.Run("with_team", func(t *testing.T) {
        team := &TeamInfo{ID: "team1", Name: "Team One"}
        p := &Principal{Team: team}
        ctx := ContextWithPrincipal(context.Background(), p)
        got := GetRBACTeam(ctx)
        if got == nil || got.ID != "team1" {
            t.Error("expected team info")
        }
    })
}

func TestGetOIDCSubject_EmptyCtx(t *testing.T) {
    slog.Info("test GetOIDCSubject_EmptyCtx")
    if GetOIDCSubject(context.Background()) != "" {
        t.Error("expected empty string for nil principal")
    }
}

func TestGetOIDCToken_EmptyCtx(t *testing.T) {
    slog.Info("test GetOIDCToken_EmptyCtx")
    if GetOIDCToken(context.Background()) != "" {
        t.Error("expected empty string for nil principal")
    }
}

func TestIsAdmin_NilPrincipal(t *testing.T) {
    slog.Info("test IsAdmin_NilPrincipal")
    if IsAdmin(context.Background()) {
        t.Error("nil principal should not be admin")
    }
}

func TestIsAdmin_MasterKey(t *testing.T) {
    // RR3 (audit P0): the inference MasterKey is NOT admin. Previously this
    // asserted master→admin, which let a leaked inference key pass
    // withAdminOnly and reach /admin/teams|orgs|gc|config-reload. Inference and
    // management planes are separated; admin requires an admin JWT.
    slog.Info("test IsAdmin_MasterKey (RR3: master is inference, not admin)")
    p := &Principal{IsMaster: true}
    ctx := ContextWithPrincipal(context.Background(), p)
    if IsAdmin(ctx) {
        t.Error("master key must NOT be admin (RR3 inference/management plane crossing)")
    }
}

func TestCanWrite_NilPrincipal(t *testing.T) {
    slog.Info("test CanWrite_NilPrincipal")
    if CanWrite(context.Background()) {
        t.Error("nil principal should not be able to write")
    }
}

func TestCanWrite_MasterKey(t *testing.T) {
    // RR3 (audit P0): the inference MasterKey maps to RoleInference, which is
    // neither RoleAdmin nor RoleEditor, so CanWrite is false. CanWrite has no
    // production callers today (grep-confirmed), so this encodes the new
    // boundary: inference keys do not get editor/write role by default. An
    // admin JWT (Role admin/editor) still passes CanWrite.
    slog.Info("test CanWrite_MasterKey (RR3: master is inference, not editor/admin)")
    p := &Principal{IsMaster: true}
    ctx := ContextWithPrincipal(context.Background(), p)
    if CanWrite(ctx) {
        t.Error("master key (inference role) must NOT pass CanWrite (requires admin/editor role)")
    }
}

func TestGetRBACRole_NilPrincipal(t *testing.T) {
    slog.Info("test GetRBACRole_NilPrincipal")
    if GetRBACRole(context.Background()) != RoleViewer {
        t.Error("nil principal should default to viewer")
    }
}

func TestIsMasterKey_NilPrincipal(t *testing.T) {
    slog.Info("test IsMasterKey_NilPrincipal")
    if IsMasterKey(context.Background()) {
        t.Error("nil principal should not be master")
    }
}
