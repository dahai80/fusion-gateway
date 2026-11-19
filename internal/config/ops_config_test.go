package config

import "testing"

func Test689_OpsDefaults(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Ops.Enabled {
        t.Fatal("ops must default off")
    }
    if cfg.Ops.DsnEnv != "FUSION_OPS_PG_DSN" {
        t.Fatalf("dsn_env default not applied: %q", cfg.Ops.DsnEnv)
    }
    if cfg.Ops.MaxQueue != 10000 || cfg.Ops.BatchSize != 100 || cfg.Ops.FlushIntervalMS != 1000 {
        t.Fatalf("queue defaults not applied: %+v", cfg.Ops)
    }
}

func Test689_OpsValidateRepairsDegenerateValues(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Ops.Enabled = true
    cfg.Ops.DsnEnv = ""
    cfg.Ops.MaxQueue = 0
    cfg.Ops.BatchSize = -5
    cfg.Ops.FlushIntervalMS = 0
    if err := validateOps(&cfg); err != nil {
        t.Fatalf("validate must repair, not error: %v", err)
    }
    if cfg.Ops.DsnEnv != "FUSION_OPS_PG_DSN" || cfg.Ops.MaxQueue != 10000 ||
        cfg.Ops.BatchSize != 100 || cfg.Ops.FlushIntervalMS != 1000 {
        t.Fatalf("repair-in-place failed: %+v", cfg.Ops)
    }
    // Disabled config must pass untouched (no repair needed).
    cfg2 := DefaultConfig()
    if err := validateOps(&cfg2); err != nil {
        t.Fatalf("disabled validate errored: %v", err)
    }
}
