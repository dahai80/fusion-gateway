package config

import "testing"

func TestValidatePersistence_DisabledNoop(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Persistence.Enabled = false
    cfg.Persistence.Queue.BatchSize = -5
    if err := validatePersistence(&cfg); err != nil {
        t.Fatalf("disabled persistence must never error: %v", err)
    }
    // disabled: degenerate values are NOT repaired (nothing reads them)
    if cfg.Persistence.Queue.BatchSize != -5 {
        t.Fatal("disabled config must stay untouched")
    }
}

func TestValidatePersistence_RepairsDegenerate(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Persistence.Enabled = true
    cfg.Persistence.Queue.Capacity = 0
    cfg.Persistence.Queue.BatchSize = -1
    cfg.Persistence.Queue.FlushIntervalMS = 0
    cfg.Persistence.Capture.MaxBodyBytes = 0
    cfg.Persistence.Postgres.MaxOpenConns = -3
    cfg.Persistence.Postgres.DsnEnv = ""

    if err := validatePersistence(&cfg); err != nil {
        t.Fatalf("validate: %v", err)
    }
    p := cfg.Persistence
    if p.Queue.Capacity != 8192 || p.Queue.BatchSize != 500 || p.Queue.FlushIntervalMS != 500 {
        t.Fatalf("queue defaults not repaired: %+v", p.Queue)
    }
    if p.Capture.MaxBodyBytes != 512<<10 {
        t.Fatalf("capture cap not repaired: %d", p.Capture.MaxBodyBytes)
    }
    if p.Postgres.DsnEnv != "FUSION_GW_PG_DSN" {
        t.Fatalf("dsn_env default not applied: %q", p.Postgres.DsnEnv)
    }
}

func TestValidatePersistence_PayloadsRetentionClamped(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Persistence.Enabled = true
    cfg.Persistence.Retention.RequestLogsDays = 30
    // payloads > logs would dangle: must clamp to logs
    cfg.Persistence.Retention.RequestPayloadsDays = 90
    if err := validatePersistence(&cfg); err != nil {
        t.Fatalf("validate: %v", err)
    }
    if got := cfg.Persistence.Retention.RequestPayloadsDays; got != 30 {
        t.Fatalf("payloads retention = %d, want clamped to 30", got)
    }
}

func TestValidatePersistence_P3bRetentionDefaults(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Persistence.Enabled = true
    cfg.Persistence.Retention.HwSamplesDays = -1
    cfg.Persistence.Retention.GatewayEventsDays = 0
    cfg.Persistence.Retention.InsightsDays = 0
    if err := validatePersistence(&cfg); err != nil {
        t.Fatalf("validate: %v", err)
    }
    r := cfg.Persistence.Retention
    if r.HwSamplesDays != 30 || r.GatewayEventsDays != 180 || r.InsightsDays != 180 {
        t.Fatalf("P3b retention defaults not repaired: %+v", r)
    }
}
