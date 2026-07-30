package config

import (
    "context"
    "testing"
)

func TestDefaultConfig_Valid(t *testing.T) {
    cfg := DefaultConfig()
    if err := validate(&cfg); err != nil {
        t.Errorf("default config should be valid: %v", err)
    }
}

func TestDefaultConfig_Port(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Server.Port != 8100 {
        t.Errorf("expected port 8100, got %d", cfg.Server.Port)
    }
}

func TestDefaultConfig_TokenThreshold(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Routing.TokenThreshold != 8000 {
        t.Errorf("expected token threshold 8000, got %d", cfg.Routing.TokenThreshold)
    }
}

func TestContextSnapshot(t *testing.T) {
    cfg := DefaultConfig()
    snap := &ConfigSnapshot{
        Config:  cfg,
        Version: 42,
    }

    ctx := WithSnapshot(context.TODO(), snap)
    got := SnapshotFromContext(ctx)
    if got == nil {
        t.Fatal("expected snapshot in context")
    }
    if got.Version != 42 {
        t.Errorf("expected version 42, got %d", got.Version)
    }
}

func TestContextVersion(t *testing.T) {
    cfg := DefaultConfig()
    snap := &ConfigSnapshot{
        Config:  cfg,
        Version: 99,
    }

    ctx := WithSnapshot(context.TODO(), snap)
    ver := VersionFromContext(ctx)
    if ver != 99 {
        t.Errorf("expected version 99, got %d", ver)
    }
}
