package config

// OpsConfig is the ops.* namespace (issue #689, flywheel Loop A
// architecture/fusion-flywheel-1119.md §2): fire-and-forget emission of
// request-level ops events into the fusion_ops.ops_events PostgreSQL table
// so the ops flywheel can mine request metrics and error clusters. Default
// off — with ops.enabled=false no queue, no worker, no PG connection and
// gateway behavior is byte-identical to before.
type OpsConfig struct {
    Enabled bool `mapstructure:"enabled"`
    // DsnEnv names the environment variable carrying the fusion_ops
    // PostgreSQL DSN. Secrets live in env, never config.yaml (same pattern
    // as persistence.postgres.dsn_env).
    DsnEnv string `mapstructure:"dsn_env"`
    // Queue bounds the async emission pipeline. The hot path NEVER blocks:
    // on a full queue the event is dropped and counted (spec §2:
    // backpressure 时丢弃计数不阻塞业务 — drop-on-backpressure, never await
    // the writer inline).
    MaxQueue        int `mapstructure:"max_queue"`
    BatchSize       int `mapstructure:"batch_size"`
    FlushIntervalMS int `mapstructure:"flush_interval_ms"`
}

// defaultOpsConfig seeds the values a minimal config.yaml omits. Mirrors the
// fusion-core OpsEmitter defaults (issue #56) so both emitters behave alike.
func defaultOpsConfig() OpsConfig {
    return OpsConfig{
        Enabled:         false,
        DsnEnv:          "FUSION_OPS_PG_DSN",
        MaxQueue:        10000,
        BatchSize:       100,
        FlushIntervalMS: 1000,
    }
}

// validateOps enforces numeric ranges (EI8 pattern: a bad value fails Load
// loudly instead of producing a degenerate runtime — a batch size of 0 would
// spin the flush loop hot).
func validateOps(cfg *Config) error {
    o := cfg.Ops
    if !o.Enabled {
        return nil
    }
    if o.DsnEnv == "" {
        o.DsnEnv = "FUSION_OPS_PG_DSN"
    }
    if o.MaxQueue <= 0 {
        o.MaxQueue = 10000
    }
    if o.BatchSize <= 0 {
        o.BatchSize = 100
    }
    if o.FlushIntervalMS <= 0 {
        o.FlushIntervalMS = 1000
    }
    cfg.Ops = o
    return nil
}
