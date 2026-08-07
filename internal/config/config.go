package config

import (
    "fmt"
	"strings"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
    "log/slog"
)

type TLSConfig struct {
    CertFile string `mapstructure:"cert_file"`
    KeyFile  string `mapstructure:"key_file"`
}

type EncryptionConfig struct {
    MasterKey string `mapstructure:"master_key"`
}

type ConnectorConfig struct {
    PersistencePath string `mapstructure:"persistence_path"`
}

type ServerConfig struct {
    Host                    string          `mapstructure:"host"`
    Port                    int             `mapstructure:"port"`
    LogLevel                string          `mapstructure:"log_level"`
    GracefulShutdownTimeout int             `mapstructure:"graceful_shutdown_timeout"`
    MaxRequestBodySize      int64           `mapstructure:"max_request_body_size"`
    EnablePProf             bool            `mapstructure:"enable_pprof"`
    TLS                     *TLSConfig      `mapstructure:"tls"`
    AutoStart               *AutoStartConfig `mapstructure:"auto_start"`
}

type AutoStartConfig struct {
    Enabled  bool   `mapstructure:"enabled"`
    Command  string `mapstructure:"command"`  // e.g. "~/claude-home/fusion-mlx/start.sh start"
    StopCmd  string `mapstructure:"stop_cmd"` // e.g. "~/claude-home/fusion-mlx/start.sh stop"
    WaitURL  string `mapstructure:"wait_url"` // e.g. "http://127.0.0.1:11434/health"
    WaitSecs int    `mapstructure:"wait_secs"` // max seconds to wait for health check
}

type AuthKeyConfig struct {
    Key             string            `mapstructure:"key"`
    Name            string            `mapstructure:"name"`
    AllowedBackends []string          `mapstructure:"allowed_backends"`
    AllowedModels   []string          `mapstructure:"allowed_models"`
    ModelModules    []string          `mapstructure:"model_modules"`
    RPM             int               `mapstructure:"rpm"`
    TPM             int               `mapstructure:"tpm"`
    ExpiresAt       string            `mapstructure:"expires_at"`
    BudgetLimit     float64           `mapstructure:"budget_limit"`
    Metadata        map[string]string `mapstructure:"metadata"`
}

type AuthConfig struct {
    Enabled     bool            `mapstructure:"enabled"`
    APIKeys     []AuthKeyConfig `mapstructure:"api_keys"`
    Passthrough bool            `mapstructure:"passthrough"`
    MasterKey   string          `mapstructure:"master_key"`
}

type RateLimitConfig struct {
    Enabled        bool `mapstructure:"enabled"`
    GlobalRPM      int  `mapstructure:"global_rpm"`
    GlobalTPM      int  `mapstructure:"global_tpm"`
    KeyEnforcement bool `mapstructure:"key_enforcement"`
}

type LocalPriorityConfig struct {
    Enabled               bool    `mapstructure:"enabled"`
    MaxSystemMemoryRatio  float64 `mapstructure:"max_system_memory_ratio"`
    MaxMLXMemoryRatio     float64 `mapstructure:"max_mlx_memory_ratio"`
    MaxConcurrent         int     `mapstructure:"max_concurrent"`
    SwapPageRateThreshold uint64  `mapstructure:"swap_page_rate_threshold"`
}

type CircuitBreakerConfig struct {
    FailureThreshold    int           `mapstructure:"failure_threshold"`
    Timeout             time.Duration `mapstructure:"timeout"`
    HalfOpenMaxRequests int           `mapstructure:"half_open_max_requests"`
    SuccessThreshold    int           `mapstructure:"success_threshold"`
}

type FallbackConfig struct {
    Enabled               bool              `mapstructure:"enabled"`
    CloudDefault          string            `mapstructure:"cloud_default"`
    ModelMapping          map[string]string `mapstructure:"model_mapping"`
    ContextWindowFallback map[string]string `mapstructure:"context_window_fallback"`
}

type NegotiationConfig struct {
    DisableFusionMLXRouting bool   `mapstructure:"disable_fusion_mlx_routing"`
    RouteHeader             string `mapstructure:"route_header"`
    RouteHeaderValue        string `mapstructure:"route_header_value"`
}

type TokenTierRule struct {
    MaxTokens int    `mapstructure:"max_tokens"`
    Backend   string `mapstructure:"backend"`
}

type TokenTierConfig struct {
    Enabled bool            `mapstructure:"enabled"`
    Metric  string          `mapstructure:"metric"`
    Rules   []TokenTierRule `mapstructure:"rules"`
}

type RatioTierRule struct {
    MaxRatio float64 `mapstructure:"max_ratio"`
    Backend  string  `mapstructure:"backend"`
}

type RatioTierConfig struct {
    Enabled bool            `mapstructure:"enabled"`
    Rules   []RatioTierRule `mapstructure:"rules"`
}

type RoutingConfig struct {
    Mode                      string               `mapstructure:"mode"`
    DefaultModel              string               `mapstructure:"default_model"`
    TokenThreshold            int                  `mapstructure:"token_threshold"`
    OutputInputRatioThreshold float64              `mapstructure:"output_input_ratio_threshold"`
    RatioTiers                RatioTierConfig      `mapstructure:"ratio_tiers"`
    TokenTiers                TokenTierConfig      `mapstructure:"token_tiers"`
    LocalPriority              LocalPriorityConfig  `mapstructure:"local_priority"`
    CircuitBreaker             CircuitBreakerConfig `mapstructure:"circuit_breaker"`
    Fallback                   FallbackConfig       `mapstructure:"fallback"`
    Negotiation                NegotiationConfig    `mapstructure:"negotiation"`
    RateLimit                  RateLimitConfig      `mapstructure:"rate_limit"`
    Retry                      RetryConfig          `mapstructure:"retry"`
    IntentClassifier           IntentClassifierConfig `mapstructure:"intent_classifier"`
}

// IntentClassifierConfig configures the D4 semantic intent layer (issue #22).
// The upstream fusion-router-light 1B LoRA adapter (fusion-trainer#11, base
// mlx-community/Llama-3.2-1B-Instruct-4bit) is now available, classifying user
// queries into 5 lightweight task types: code/chat/math/translate/summary.
// When disabled, the engine uses NoopClassifier and the existing P0-P7 rule
// chain decides routing unchanged.
type IntentClassifierConfig struct {
    Enabled bool `mapstructure:"enabled"`
    // Endpoint is the fusion-mlx /v1/chat/completions base URL (e.g.
    // "http://127.0.0.1:11434"). Defaults to the local fusion-mlx address.
    Endpoint string `mapstructure:"endpoint"`
    // BaseModel is the LoRA base model id served by fusion-mlx (the adapter is
    // applied on top of it). e.g. "mlx-community/Llama-3.2-1B-Instruct-4bit".
    BaseModel string `mapstructure:"base_model"`
    // Model is a backward-compatible alias for the scaffolding config; when set
    // it overrides BaseModel.
    Model string `mapstructure:"model"`
    // Adapter is the absolute path to the served LoRA adapter directory
    // (the directory containing adapters.safetensors). Sent as the OpenAI
    // request "adapters" field so fusion-mlx hot-loads the derived engine.
    Adapter string `mapstructure:"adapter"`
    // APIKey authenticates the classify request to fusion-mlx if auth enabled.
    APIKey        string        `mapstructure:"api_key"`
    Timeout       time.Duration `mapstructure:"timeout"`
    MinConfidence float64       `mapstructure:"min_confidence"`
}

type RetryConfig struct {
    MaxRetries           int           `mapstructure:"max_retries"`
    InitialBackoff       time.Duration `mapstructure:"initial_backoff"`
    MaxBackoff           time.Duration `mapstructure:"max_backoff"`
    RetryableStatusCodes []int         `mapstructure:"retryable_status_codes"`
}

type CacheConfig struct {
    Enabled      bool          `mapstructure:"enabled"`
    MaxEntries   int           `mapstructure:"max_entries"`
    TTL          time.Duration `mapstructure:"ttl"`
    MaxMemoryMB  int           `mapstructure:"max_memory_mb"`
    Backend      string        `mapstructure:"backend"`
    Redis        RedisConfig   `mapstructure:"redis"`
    WarmupFile   string        `mapstructure:"warmup_file"`
}

type RedisConfig struct {
    Addr     string `mapstructure:"addr"`
    Password string `mapstructure:"password"`
    DB       int    `mapstructure:"db"`
    PoolSize int    `mapstructure:"pool_size"`
}

type CostConfig struct {
    Enabled              bool    `mapstructure:"enabled"`
    PricingFile          string  `mapstructure:"pricing_file"`
    BudgetAlertThreshold float64 `mapstructure:"budget_alert_threshold"`
}

type PIIConfig struct {
    Enabled  bool           `mapstructure:"enabled"`
    Action   string         `mapstructure:"action"`
    Patterns []PIIPattern   `mapstructure:"patterns"`
}

type PIIPattern struct {
    Name  string `mapstructure:"name"`
    Regex string `mapstructure:"regex"`
}

type CloudRoutingConfig struct {
    Strategy     string         `mapstructure:"strategy"`
    CloudWeights map[string]int `mapstructure:"cloud_weights"`
}

type GCConfig struct {
    Enabled            bool          `mapstructure:"enabled"`
    IdleInterval       time.Duration `mapstructure:"idle_interval"`
    MinIdleSinceLastGC time.Duration `mapstructure:"min_idle_since_last_gc"`
}

type BackendConfig struct {
    Type                string        `mapstructure:"type"`
    BaseURL             string        `mapstructure:"base_url"`
    APIKey              string        `mapstructure:"api_key"`
    Timeout             time.Duration `mapstructure:"timeout"`
    HealthCheckInterval time.Duration `mapstructure:"health_check_interval"`
    Priority            int           `mapstructure:"priority"`
    Enabled             bool          `mapstructure:"enabled"`
    Models              []string      `mapstructure:"models"`
    GC                  GCConfig      `mapstructure:"gc"`
}

type IOKitConfig struct {
    Enabled bool `mapstructure:"enabled"`
}

type GopsutilConfig struct {
    Enabled bool `mapstructure:"enabled"`
}

type MLXMetricsConfig struct {
    Enabled  bool          `mapstructure:"enabled"`
    Interval time.Duration `mapstructure:"interval"`
}

type SwapConfig struct {
    PageRateSampling  bool   `mapstructure:"page_rate_sampling"`
    PageRateThreshold uint64 `mapstructure:"page_rate_threshold"`
}

type HardwareConfig struct {
    Enabled                   bool             `mapstructure:"enabled"`
    CollectInterval           time.Duration    `mapstructure:"collect_interval"`
    IOKit                     IOKitConfig      `mapstructure:"iokit"`
    Gopsutil                  GopsutilConfig   `mapstructure:"gopsutil"`
    MLXMetrics                MLXMetricsConfig `mapstructure:"mlx_metrics"`
    Swap                      SwapConfig       `mapstructure:"swap"`
    CollectionErrorProtection bool             `mapstructure:"collection_error_protection"`
}

type CalibrationConfig struct {
    Enabled             bool    `mapstructure:"enabled"`
    SampleInterval      int     `mapstructure:"sample_interval"`
    SampleSize          int     `mapstructure:"sample_size"`
    DeviationThreshold  float64 `mapstructure:"deviation_threshold"`
    AutoSwitchThreshold float64 `mapstructure:"auto_switch_threshold"`
}

type ScenePresetsConfig struct {
    Chat     int `mapstructure:"chat"`
    Code     int `mapstructure:"code"`
    ToolCall int `mapstructure:"tool_call"`
}

type TokenizerConfig struct {
    Provider                 string            `mapstructure:"provider"`
    DefaultMaxTokensStrategy string            `mapstructure:"default_max_tokens_strategy"`
    ContextWindowRatio       float64           `mapstructure:"context_window_ratio"`
    MinMaxTokens             int               `mapstructure:"min_max_tokens"`
    ScenePresets             ScenePresetsConfig `mapstructure:"scene_presets"`
    VisionTokenEstimate      int               `mapstructure:"vision_token_estimate"`
    Calibration              CalibrationConfig `mapstructure:"calibration"`
}

type ObservabilityConfig struct {
    LogFormat             string `mapstructure:"log_format"`
    LogFile               string `mapstructure:"log_file"`
    LogRotationMaxSize    int    `mapstructure:"log_rotation_max_size"`
    LogRotationMaxBackups int    `mapstructure:"log_rotation_max_backups"`
    MetricsEnabled        bool   `mapstructure:"metrics_enabled"`
    MetricsPath           string `mapstructure:"metrics_path"`
    AuditLogEnabled       bool   `mapstructure:"audit_log_enabled"`
    ConfigAuditLog        bool   `mapstructure:"config_audit_log"`
    ConfigAuditFile       string `mapstructure:"config_audit_file"`
    OtelEnabled           bool   `mapstructure:"otel_enabled"`
    OtelEndpoint          string `mapstructure:"otel_endpoint"`
    OtelProtocol          string `mapstructure:"otel_protocol"`
    OtelServiceName       string `mapstructure:"otel_service_name"`
}

type CORSConfig struct {
    AllowedOrigins []string `mapstructure:"allowed_origins"`
    AllowedMethods []string `mapstructure:"allowed_methods"`
    AllowedHeaders []string `mapstructure:"allowed_headers"`
}

type HotReloadConfig struct {
    Enabled              bool          `mapstructure:"enabled"`
    WatchPath            string        `mapstructure:"watch_path"`
    Debounce             time.Duration `mapstructure:"debounce"`
    Versioning           bool          `mapstructure:"versioning"`
    BreakerDrainTimeout  time.Duration `mapstructure:"breaker_drain_timeout"`
    BreakerWarmupSuccess int           `mapstructure:"breaker_warmup_success"`
}

type ValidationConfig struct {
    BaseURLConflictCheck bool `mapstructure:"base_url_conflict_check"`
}

type ClusterNodeConfig struct {
    ID       string `mapstructure:"id"`
    Address  string `mapstructure:"address"`
    GPU      string `mapstructure:"gpu"`
    MemoryGB int    `mapstructure:"memory_gb"`
    // Platform identifies the node's runtime platform for D4 dispatch
    // (issue #23/#25): "mac" (fusion-mlx Base+LoRA+SpecDec) or
    // "windows-cuda" (DeepSeek 70B / Qwen 72B FP8 + diffusion). Empty means
    // legacy untyped node (eligible for all cluster fallback).
    Platform string `mapstructure:"platform"`
}

type ClusterMode string

const (
    ClusterModeStandalone ClusterMode = "standalone"
    ClusterModeMaster     ClusterMode = "master"
)

type ClusterMasterConfig struct {
    Address    string `mapstructure:"address"`
    SharedToken string `mapstructure:"shared_token"`
}

type ClusterConfig struct {
    Enabled             bool                `mapstructure:"enabled"`
    Mode                ClusterMode         `mapstructure:"mode"`
    Nodes               []ClusterNodeConfig `mapstructure:"nodes"`
    Master              ClusterMasterConfig `mapstructure:"master"`
    LoadBalancer        string              `mapstructure:"load_balancer"`
    HealthCheckInterval time.Duration       `mapstructure:"health_check_interval"`
    FailureThreshold    int                 `mapstructure:"failure_threshold"`
    RecoveryInterval    time.Duration       `mapstructure:"recovery_interval"`
    // PlatformRouting enables D4 dispatch-by-platform (issue #23/#25). When
    // enabled, the semantic intent layer routes heavy_model / diffusion
    // intents to the configured platform's cluster nodes.
    PlatformRouting PlatformRoutingConfig `mapstructure:"platform_routing"`
}

// PlatformRoutingConfig maps semantic intents to target cluster platforms.
type PlatformRoutingConfig struct {
    Enabled            bool   `mapstructure:"enabled"`
    HeavyModelPlatform string `mapstructure:"heavy_model_platform"` // default "windows-cuda"
    DiffusionPlatform  string `mapstructure:"diffusion_platform"`   // default "windows-cuda"
}

type RealtimeConfig struct {
    Enabled       bool   `mapstructure:"enabled"`
    BackendURL    string `mapstructure:"backend_url"`
    APIKey        string `mapstructure:"api_key"`
    MaxMessageMB  int    `mapstructure:"max_message_mb"`
}

type AdminConfig struct {
    Users     map[string]string `mapstructure:"users"`
    Enabled   bool   `mapstructure:"enabled"`
    Listen    string `mapstructure:"listen"`
    LogMaxLen int    `mapstructure:"log_max_len"`
    JWTSecret string `mapstructure:"jwt_secret"`
}

type OIDCConfig struct {
    Enabled       bool   `mapstructure:"enabled"`
    Issuer        string `mapstructure:"issuer"`
    ClientID      string `mapstructure:"client_id"`
    Audiences     string `mapstructure:"audiences"`
    Scopes        string `mapstructure:"scopes"`
    ClaimMappings string `mapstructure:"claim_mappings"`
}

type RBACConfig struct {
    Enabled bool   `mapstructure:"enabled"`
    DefaultRole string `mapstructure:"default_role"`
}

type TeamConfig struct {
    Enabled bool   `mapstructure:"enabled"`
    DefaultTeam string `mapstructure:"default_team"`
}

type SemanticCacheConfig struct {
    Enabled           bool    `mapstructure:"enabled"`
    SimilarityThreshold float64 `mapstructure:"similarity_threshold"`
    MaxEntries        int     `mapstructure:"max_entries"`
    Provider          string  `mapstructure:"provider"`
    Endpoint          string  `mapstructure:"endpoint"`
}

type PromptInjectionConfig struct {
    Enabled    bool   `mapstructure:"enabled"`
    Action     string `mapstructure:"action"`
    Provider   string `mapstructure:"provider"`
    APIKey     string `mapstructure:"api_key"`
    Threshold  float64 `mapstructure:"threshold"`
}

type CostMarkupConfig struct {
    Enabled      bool    `mapstructure:"enabled"`
    GlobalMarkup float64 `mapstructure:"global_markup"`
}

type BatchConfig struct {
    Enabled       bool          `mapstructure:"enabled"`
    MaxBatchSize  int           `mapstructure:"max_batch_size"`
    PollInterval  time.Duration `mapstructure:"poll_interval"`
    Timeout       time.Duration `mapstructure:"timeout"`
}

type StoreConfig struct {
    Backend string      `mapstructure:"backend"`
    Redis   RedisConfig `mapstructure:"redis"`
}

type MCPConfig struct {
    Enabled     bool  `mapstructure:"enabled"`
    Host        string `mapstructure:"host"`
    Port        int    `mapstructure:"port"`
    TokenBudget int64  `mapstructure:"token_budget"`
    MaxRequests int    `mapstructure:"max_requests"`
    NodePort    int    `mapstructure:"node_port"`
    LocalPort   int    `mapstructure:"local_port"`
}

type Config struct {
    Server        ServerConfig             `mapstructure:"server"`
    Auth          AuthConfig               `mapstructure:"auth"`
    Routing       RoutingConfig            `mapstructure:"routing"`
    Backends      map[string]BackendConfig `mapstructure:"backends"`
    Validation    ValidationConfig         `mapstructure:"validation"`
    Hardware      HardwareConfig           `mapstructure:"hardware"`
    Tokenizer     TokenizerConfig          `mapstructure:"tokenizer"`
    Observability ObservabilityConfig      `mapstructure:"observability"`
    CORS          CORSConfig               `mapstructure:"cors"`
    HotReload     HotReloadConfig          `mapstructure:"hot_reload"`
    Cluster       ClusterConfig            `mapstructure:"cluster"`
    Realtime      RealtimeConfig           `mapstructure:"realtime"`
    Cache         CacheConfig              `mapstructure:"cache"`
    Cost          CostConfig               `mapstructure:"cost"`
    PII           PIIConfig                `mapstructure:"pii"`
    CloudRouting  CloudRoutingConfig       `mapstructure:"cloud_routing"`
    Admin         *AdminConfig             `mapstructure:"admin"`
    OIDC          OIDCConfig               `mapstructure:"oidc"`
    RBAC          RBACConfig               `mapstructure:"rbac"`
    Team          TeamConfig               `mapstructure:"team"`
    SemanticCache SemanticCacheConfig      `mapstructure:"semantic_cache"`
    PromptInjection PromptInjectionConfig  `mapstructure:"prompt_injection"`
    CostMarkup    CostMarkupConfig         `mapstructure:"cost_markup"`
    Batch         BatchConfig              `mapstructure:"batch"`
    Store         StoreConfig              `mapstructure:"store"`
    Encryption    *EncryptionConfig        `mapstructure:"encryption"`
    Connector     *ConnectorConfig         `mapstructure:"connector"`
    MCP           MCPConfig                `mapstructure:"mcp"`
}

type ConfigSnapshot struct {
    Config   Config
    Version  uint64
    LoadedAt time.Time
}

var (
    globalConfig     atomic.Pointer[ConfigSnapshot]
    configVersion    atomic.Uint64
    configMu         sync.RWMutex
    onReloadHandlers []func(old, new *ConfigSnapshot)
)

func Load(path string) (*ConfigSnapshot, error) {
    v := viper.New()
    v.SetConfigFile(path)
    v.SetConfigType("yaml")
    v.AutomaticEnv()

    if err := v.ReadInConfig(); err != nil {
        return nil, fmt.Errorf("read config: %w", err)
    }

    var cfg Config
    if err := v.Unmarshal(&cfg); err != nil {
        return nil, fmt.Errorf("unmarshal config: %w", err)
    }

    if err := validate(&cfg); err != nil {
        return nil, fmt.Errorf("validate config: %w", err)
    }

    ver := configVersion.Add(1)
    snap := &ConfigSnapshot{
        Config:   cfg,
        Version:  ver,
        LoadedAt: time.Now(),
    }
    globalConfig.Store(snap)

    slog.Info("config loaded", "version", ver, "path", path)
    return snap, nil
}

func GetSnapshot() *ConfigSnapshot {
    if s := globalConfig.Load(); s != nil {
        return s
    }
    cfg := DefaultConfig()
    // Never expose a hardcoded JWT secret — clear it in the fallback path
    // so admin auth cannot be used before Load() provides a real secret.
    if cfg.Admin != nil {
        cfg.Admin.JWTSecret = ""
    }
    return &ConfigSnapshot{Config: cfg, Version: 0, LoadedAt: time.Now()}
}

func OnReload(fn func(old, new *ConfigSnapshot)) {
    configMu.Lock()
    defer configMu.Unlock()
    onReloadHandlers = append(onReloadHandlers, fn)
}

func FireReload(old, newSnap *ConfigSnapshot) {
    configMu.RLock()
    handlers := make([]func(old, new *ConfigSnapshot), len(onReloadHandlers))
    copy(handlers, onReloadHandlers)
    configMu.RUnlock()

    for _, fn := range handlers {
        fn(old, newSnap)
    }
}

func WatchAndReload(path string) {
    snap := GetSnapshot()
    if !snap.Config.HotReload.Enabled {
        slog.Info("hot reload disabled")
        return
    }

    watchPath := path
    if snap.Config.HotReload.WatchPath != "" {
        watchPath = snap.Config.HotReload.WatchPath
    }

    v := viper.New()
    v.SetConfigFile(watchPath)
    v.SetConfigType("yaml")
    v.WatchConfig()

    v.OnConfigChange(func(e fsnotify.Event) {
        slog.Info("config file changed, reloading", "path", watchPath)
        oldSnap := GetSnapshot()

        var cfg Config
        if err := v.Unmarshal(&cfg); err != nil {
            slog.Error("failed to unmarshal reloaded config", "error", err)
            return
        }

        if err := validate(&cfg); err != nil {
            slog.Error("config validation failed on reload", "error", err)
            return
        }

        ver := configVersion.Add(1)
        newSnap := &ConfigSnapshot{
            Config:   cfg,
            Version:  ver,
            LoadedAt: time.Now(),
        }

        // C5 fix: run handlers BEFORE committing new snapshot
        configMu.RLock()
        handlers := make([]func(old, new *ConfigSnapshot), len(onReloadHandlers))
        copy(handlers, onReloadHandlers)
        configMu.RUnlock()

        for _, fn := range handlers {
            fn(oldSnap, newSnap)
        }

        // commit new snapshot only after all handlers succeed
        globalConfig.Store(newSnap)

        slog.Info("config reloaded", "version", ver)

        // Audit: record field-level diff between old and new config
        AuditConfigChange(oldSnap, newSnap)
    })
}

func validate(cfg *Config) error {
    if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
        return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
    }

    if cfg.Routing.TokenThreshold <= 0 {
        return fmt.Errorf("token_threshold must be positive, got: %d", cfg.Routing.TokenThreshold)
    }

    if cfg.Routing.Mode != "" && cfg.Routing.Mode != "local" && cfg.Routing.Mode != "cloud" && cfg.Routing.Mode != "hybrid" {
        return fmt.Errorf("routing.mode must be local, cloud, or hybrid, got: %q", cfg.Routing.Mode)
    }

    if cfg.Routing.OutputInputRatioThreshold < 0 {
        return fmt.Errorf("output_input_ratio_threshold must be non-negative, got: %f", cfg.Routing.OutputInputRatioThreshold)
    }
    for i, r := range cfg.Routing.RatioTiers.Rules {
        if r.MaxRatio <= 0 {
            return fmt.Errorf("ratio_tiers.rules[%d].max_ratio must be positive, got: %f", i, r.MaxRatio)
        }
        if r.Backend == "" {
            return fmt.Errorf("ratio_tiers.rules[%d].backend is required", i)
        }
    }

    if cfg.Routing.LocalPriority.MaxSystemMemoryRatio <= 0 || cfg.Routing.LocalPriority.MaxSystemMemoryRatio > 1 {
        return fmt.Errorf("max_system_memory_ratio must be in (0,1], got: %f", cfg.Routing.LocalPriority.MaxSystemMemoryRatio)
    }

    if cfg.Validation.BaseURLConflictCheck {
        urls := make(map[string]string)
        for name, backend := range cfg.Backends {
            if !backend.Enabled {
                continue
            }
            if existing, ok := urls[backend.BaseURL]; ok {
                return fmt.Errorf("base_url conflict: %s and %s both use %s", existing, name, backend.BaseURL)
            }
            urls[backend.BaseURL] = name
        }
    }

    // V1 fix: validate admin config security
    if cfg.Admin != nil && cfg.Admin.Enabled {
        if cfg.Admin.JWTSecret == "" {
            return fmt.Errorf("admin.jwt_secret is required when admin is enabled")
        }
        if len(cfg.Admin.JWTSecret) < 32 {
            return fmt.Errorf("admin.jwt_secret must be at least 32 characters, got %d", len(cfg.Admin.JWTSecret))
        }
        for username, password := range cfg.Admin.Users {
            if len(password) < 8 {
                return fmt.Errorf("admin user %q password must be at least 8 characters, got %d", username, len(password))
            }
        }
    }

    // V2 fix: validate routing config
    if cfg.Routing.LocalPriority.MaxConcurrent < 0 {
        return fmt.Errorf("max_concurrent must be non-negative, got %d", cfg.Routing.LocalPriority.MaxConcurrent)
    }
    if cfg.Cache.MaxMemoryMB < 0 {
        return fmt.Errorf("cache.max_memory_mb must be non-negative, got %d", cfg.Cache.MaxMemoryMB)
    }
    if cfg.Cache.MaxEntries < 0 {
        return fmt.Errorf("cache.max_entries must be non-negative, got %d", cfg.Cache.MaxEntries)
    }

    // V2 fix: validate cluster node addresses
    for _, node := range cfg.Cluster.Nodes {
        if node.Address != "" && !strings.HasPrefix(node.Address, "http://") && !strings.HasPrefix(node.Address, "https://") {
            return fmt.Errorf("cluster node %q address must start with http:// or https://, got %q", node.ID, node.Address)
        }
    }

    return nil
}

func DefaultConfig() Config {
    return Config{
        Server: ServerConfig{
            Host:                   "0.0.0.0",
            Port:                   11432,
            LogLevel:               "info",
            GracefulShutdownTimeout: 15,
            MaxRequestBodySize:     5242880,
        },
        Routing: RoutingConfig{
            Mode:                      "hybrid",
            DefaultModel:              "",
            TokenThreshold:            8000,
            OutputInputRatioThreshold: 0.6,
            RatioTiers: RatioTierConfig{
                Enabled: false,
                Rules:   []RatioTierRule{},
            },
            TokenTiers: TokenTierConfig{
                Enabled: false,
                Metric:  "total",
            },
            LocalPriority: LocalPriorityConfig{
                Enabled:               true,
                MaxSystemMemoryRatio:  0.9,
                MaxMLXMemoryRatio:     0.7,
                MaxConcurrent:         8,
                SwapPageRateThreshold: 100,
            },
            CircuitBreaker: CircuitBreakerConfig{
                FailureThreshold:    5,
                Timeout:             30 * time.Second,
                HalfOpenMaxRequests: 1,
                SuccessThreshold:    3,
            },
            Negotiation: NegotiationConfig{
                DisableFusionMLXRouting: true,
                RouteHeader:             "X-Fusion-Route",
                RouteHeaderValue:        "gateway-decision",
            },
            IntentClassifier: IntentClassifierConfig{
                Enabled:       false,
                Endpoint:      "http://127.0.0.1:11434",
                BaseModel:     "mlx-community/Llama-3.2-1B-Instruct-4bit",
                Timeout:       2 * time.Second,
                MinConfidence: 0.7,
            },
        },
        Hardware: HardwareConfig{
            Enabled:         true,
            CollectInterval: 2 * time.Second,
            IOKit:           IOKitConfig{Enabled: true},
            Gopsutil:        GopsutilConfig{Enabled: true},
            MLXMetrics:      MLXMetricsConfig{Enabled: true, Interval: 5 * time.Second},
            Swap:            SwapConfig{PageRateSampling: true, PageRateThreshold: 100},
            CollectionErrorProtection: true,
        },
        Tokenizer: TokenizerConfig{
            Provider:                 "local",
            DefaultMaxTokensStrategy: "context_window_ratio",
            ContextWindowRatio:       0.1,
            MinMaxTokens:             256,
            ScenePresets: ScenePresetsConfig{
                Chat:     1024,
                Code:     2048,
                ToolCall: 512,
            },
            VisionTokenEstimate: 256,
            Calibration: CalibrationConfig{
                Enabled:             true,
                SampleInterval:      1000,
                SampleSize:          10,
                DeviationThreshold:  0.02,
                AutoSwitchThreshold: 0.05,
            },
        },
        Admin: &AdminConfig{
            Enabled:   true,
            JWTSecret: "default-dev-secret-change-in-production-32ch",
            LogMaxLen: 10000,
        },
    }
}
