package config

import (
    "context"
    "testing"
    "time"
)

func TestDefaultConfig_Valid(t *testing.T) {
    cfg := DefaultConfig()
    if err := validate(&cfg); err != nil {
        t.Errorf("default config should be valid: %v", err)
    }
}

func TestDefaultConfig_StreamDefaults(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Routing.Stream.KeepaliveInterval != 15*time.Second {
        t.Errorf("expected stream keepalive_interval 15s, got %v", cfg.Routing.Stream.KeepaliveInterval)
    }
    if cfg.Routing.Stream.IdleTimeout != 180*time.Second {
        t.Errorf("expected stream idle_timeout 180s, got %v", cfg.Routing.Stream.IdleTimeout)
    }
}

func TestDefaultConfig_Port(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Server.Port != 11432 {
        t.Errorf("expected port 11432, got %d", cfg.Server.Port)
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
