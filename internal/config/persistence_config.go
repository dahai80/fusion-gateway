package config

// PersistenceConfig is the persistence.* namespace (plan §6,
// architecture/fusion-gateway-seriazation-archi-1015.md): PostgreSQL sink for
// request logs + request/response payloads so every log survives a gateway
// restart. Default off — with persistence.enabled=false the gateway behavior
// is byte-identical to before (no capture, no sink, no PG connection).
type PersistenceConfig struct {
    Enabled bool `mapstructure:"enabled"`
    Postgres PersistencePostgresConfig `mapstructure:"postgres"`
    Queue    PersistenceQueueConfig    `mapstructure:"queue"`
    Capture  PersistenceCaptureConfig  `mapstructure:"capture"`
    Retention PersistenceRetentionConfig `mapstructure:"retention"`
}

// PersistencePostgresConfig holds the connection shape. The DSN itself is
// NEVER a config-file key: dsn_env names the environment variable that
// carries it (bindSecretEnv pattern — secrets live in env, not config.yaml).
// Changing dsn_env requires a restart (the pool is built once at startup);
// validate warns when it differs from the running value.
type PersistencePostgresConfig struct {
    // DsnEnv is the env var name holding the PostgreSQL DSN. Default
    // FUSION_GW_PG_DSN. An empty DSN with enabled=true is a validate error —
    // fail loudly instead of silently writing nothing.
    DsnEnv string `mapstructure:"dsn_env"`
    MaxOpenConns int `mapstructure:"max_open_conns"`
    MaxIdleConns int `mapstructure:"max_idle_conns"`
    ConnTimeoutSecs int `mapstructure:"conn_timeout_secs"`
}

// PersistenceQueueConfig bounds the async write pipeline. The hot path never
// blocks: Enqueue drops the OLDEST queued entry when full (keep-new policy —
// the newest request is the one an operator is most likely investigating)
// and increments a drop counter surfaced in /metrics.
type PersistenceQueueConfig struct {
    Capacity int `mapstructure:"capacity"`
    BatchSize int `mapstructure:"batch_size"`
    FlushIntervalMS int `mapstructure:"flush_interval_ms"`
}

// PersistenceCaptureConfig controls request/response body capture. Bodies are
// the privacy-sensitive part — each direction can be disabled independently
// (metadata-only compliance mode = both false).
type PersistenceCaptureConfig struct {
    RequestBody bool `mapstructure:"request_body"`
    ResponseBody bool `mapstructure:"response_body"`
    // MaxBodyBytes is the per-direction truncation cap. Oversize bodies are
    // stored truncated with the truncated flag set — never dropped entirely.
    MaxBodyBytes int `mapstructure:"max_body_bytes"`
    // StripAuthFields removes credential-shaped fields (api_key,
    // authorization) from the stored request JSON before persist.
    StripAuthFields bool `mapstructure:"strip_auth_fields"`
}

// PersistenceRetentionConfig drives the partition-DROP retention manager.
// Each table drops its own daily partitions past the window; DROP is O(1)
// with no bloat, unlike DELETE.
type PersistenceRetentionConfig struct {
    RequestLogsDays int `mapstructure:"request_logs_days"`
    RequestPayloadsDays int `mapstructure:"request_payloads_days"`
}

// defaultPersistenceConfig seeds the values a minimal config.yaml omits.
// Mirrors plan §6 defaults.
func defaultPersistenceConfig() PersistenceConfig {
    return PersistenceConfig{
        Enabled: false,
        Postgres: PersistencePostgresConfig{
            DsnEnv:       "FUSION_GW_PG_DSN",
            MaxOpenConns: 8,
            MaxIdleConns: 4,
            ConnTimeoutSecs: 5,
        },
        Queue: PersistenceQueueConfig{
            Capacity:        8192,
            BatchSize:       500,
            FlushIntervalMS: 500,
        },
        Capture: PersistenceCaptureConfig{
            RequestBody:     true,
            ResponseBody:    true,
            MaxBodyBytes:    512 << 10,
            StripAuthFields: true,
        },
        Retention: PersistenceRetentionConfig{
            RequestLogsDays:    90,
            RequestPayloadsDays: 30,
        },
    }
}

// validatePersistence enforces the numeric ranges (EI8 pattern: a bad value
// fails Load loudly instead of producing a degenerate runtime — e.g. a batch
// size of 0 would spin the flush loop hot).
func validatePersistence(cfg *Config) error {
    p := cfg.Persistence
    if !p.Enabled {
        return nil
    }
    if p.Postgres.DsnEnv == "" {
        p.Postgres.DsnEnv = "FUSION_GW_PG_DSN"
    }
    if p.Postgres.MaxOpenConns <= 0 || p.Postgres.MaxIdleConns <= 0 ||
        p.Postgres.MaxIdleConns > p.Postgres.MaxOpenConns {
        // fall back to defaults rather than rejecting: these are tuning knobs
        // and a typo should not take the gateway down
        p.Postgres.MaxOpenConns = 8
        p.Postgres.MaxIdleConns = 4
    }
    if p.Postgres.ConnTimeoutSecs <= 0 {
        p.Postgres.ConnTimeoutSecs = 5
    }
    if p.Queue.Capacity <= 0 {
        p.Queue.Capacity = 8192
    }
    if p.Queue.BatchSize <= 0 {
        p.Queue.BatchSize = 500
    }
    if p.Queue.FlushIntervalMS <= 0 {
        p.Queue.FlushIntervalMS = 500
    }
    if p.Capture.MaxBodyBytes <= 0 {
        p.Capture.MaxBodyBytes = 512 << 10
    }
    if p.Retention.RequestLogsDays <= 0 {
        p.Retention.RequestLogsDays = 90
    }
    if p.Retention.RequestPayloadsDays <= 0 || p.Retention.RequestPayloadsDays > p.Retention.RequestLogsDays {
        // payloads retention must not exceed the main table's, or payload
        // rows would outlive their log row (dangling rows, plan §4.3)
        p.Retention.RequestPayloadsDays = min(p.Retention.RequestPayloadsDays, p.Retention.RequestLogsDays)
        if p.Retention.RequestPayloadsDays <= 0 {
            p.Retention.RequestPayloadsDays = p.Retention.RequestLogsDays
        }
    }
    cfg.Persistence = p
    return nil
}
