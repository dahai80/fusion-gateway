package config

import (
    "strings"
    "testing"
    "time"
)

// Issue #595: token exchange plane config semantics.

func te595Cfg() Config {
    cfg := DefaultConfig()
    cfg.Auth.Enabled = true
    cfg.Auth.MasterKey = "a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8"
    cfg.Auth.TokenExchange.Enabled = true
    cfg.Auth.TokenExchange.MaxTTL = time.Hour
    return cfg
}

func TestValidate_TokenExchangeDefaultDisabled(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Auth.TokenExchange.Enabled {
        t.Fatal("token exchange must default to disabled")
    }
    if cfg.Auth.TokenExchange.MaxTTL != time.Hour {
        t.Fatalf("default max_ttl = %s, want 1h", cfg.Auth.TokenExchange.MaxTTL)
    }
    if err := validate(&cfg); err != nil {
        t.Fatalf("default config must validate: %v", err)
    }
}

func TestValidate_TokenExchangeRequiresAuthEnabled(t *testing.T) {
    cfg := te595Cfg()
    cfg.Auth.Enabled = false
    err := validate(&cfg)
    if err == nil || !strings.Contains(err.Error(), "auth.enabled") {
        t.Fatalf("exchange without key plane must fail, got %v", err)
    }
}

func TestValidate_TokenExchangeMaxTTLMustBePositive(t *testing.T) {
    cfg := te595Cfg()
    cfg.Auth.TokenExchange.MaxTTL = 0
    if err := validate(&cfg); err == nil || !strings.Contains(err.Error(), "max_ttl") {
        t.Fatalf("zero max_ttl must fail, got %v", err)
    }
    cfg.Auth.TokenExchange.MaxTTL = -time.Minute
    if err := validate(&cfg); err == nil {
        t.Fatal("negative max_ttl must fail")
    }
}

func TestValidate_TokenExchangeMaxTTLUnder24h(t *testing.T) {
    cfg := te595Cfg()
    cfg.Auth.TokenExchange.MaxTTL = 24*time.Hour + time.Minute
    if err := validate(&cfg); err == nil || !strings.Contains(err.Error(), "24h") {
        t.Fatalf("max_ttl above ceiling must fail, got %v", err)
    }
}

func TestValidate_ExchangeOnlyKeyWithZeroCapsRejected(t *testing.T) {
    cfg := te595Cfg()
    cfg.Auth.TokenExchange.Enabled = false
    cfg.Auth.APIKeys = []AuthKeyConfig{
        {Key: "fk-office", Name: "office-standalone", ExchangeOnly: true, RPM: 0, TPM: 0},
    }
    err := validate(&cfg)
    if err == nil || !strings.Contains(err.Error(), "exchange_only") {
        t.Fatalf("exchange-only key with 0 caps must fail like #594, got %v", err)
    }
    // Explicit unlimited opt-in is accepted.
    cfg.Auth.APIKeys[0].Unlimited = true
    if err := validate(&cfg); err != nil {
        t.Fatalf("exchange-only with unlimited opt-in must validate: %v", err)
    }
}
