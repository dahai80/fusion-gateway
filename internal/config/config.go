package config

import (
    "fmt"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
    "log/slog"
)

type ServerConfig struct {
    Host                   string `mapstructure:"host"`
    Port                   int    `mapstructure:"port"`
    LogLevel               string `mapstructure:"log_level"`
    GracefulShutdownTimeout int   `mapstructure:"graceful_shutdown_timeout"`
    MaxRequestBodySize     int64  `mapstructure:"max_request_body_size"`
}

type AuthKeyConfig struct {
    Key             string   `mapstructure:"key"`
    Name            string   `mapstructure:"name"`
    AllowedBackends []string `mapstructure:"allowed_backends"`
    RPM             int      `mapstructure:"rpm"`
    TPM             int      `mapstructure:"tpm"`
}

type AuthConfig struct {
    Enabled     bool            `mapstructure:"enabled"`
    APIKeys     []AuthKeyConfig `mapstructure:"api_keys"`
    Passthrough bool            `mapstructure:"passthrough"`
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
    Enabled      bool              `mapstructure:"enabled"`
    CloudDefault string            `mapstructure:"cloud_default"`
    ModelMapping map[string]string `mapstructure:"model_mapping"`
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

type RoutingConfig struct {
    TokenThreshold int                  `mapstructure:"token_threshold"`
    TokenTiers     TokenTierConfig      `mapstructure:"token_tiers"`
    LocalPriority  LocalPriorityConfig  `mapstructure:"local_priority"`
    CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
    Fallback       FallbackConfig       `mapstructure:"fallback"`
    Negotiation    NegotiationConfig    `mapstructure:"negotiation"`
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
}

type RealtimeConfig struct {
    Enabled       bool   `mapstructure:"enabled"`
    BackendURL    string `mapstructure:"backend_url"`
    APIKey        string `mapstructure:"api_key"`
    MaxMessageMB  int    `mapstructure:"max_message_mb"`
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
    return &ConfigSnapshot{Config: DefaultConfig(), Version: 0, LoadedAt: time.Now()}
}

func OnReload(fn func(old, new *ConfigSnapshot)) {
    configMu.Lock()
    defer configMu.Unlock()
    onReloadHandlers = append(onReloadHandlers, fn)
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
        globalConfig.Store(newSnap)

        slog.Info("config reloaded", "version", ver)

        // Audit: record field-level diff between old and new config
        AuditConfigChange(oldSnap, newSnap)

        configMu.RLock()
        handlers := make([]func(old, new *ConfigSnapshot), len(onReloadHandlers))
        copy(handlers, onReloadHandlers)
        configMu.RUnlock()

        for _, fn := range handlers {
            fn(oldSnap, newSnap)
        }
    })
}

func validate(cfg *Config) error {
    if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
        return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
    }

    if cfg.Routing.TokenThreshold <= 0 {
        return fmt.Errorf("token_threshold must be positive, got: %d", cfg.Routing.TokenThreshold)
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

    return nil
}

func DefaultConfig() Config {
    return Config{
        Server: ServerConfig{
            Host:                   "0.0.0.0",
            Port:                   8100,
            LogLevel:               "info",
            GracefulShutdownTimeout: 15,
            MaxRequestBodySize:     5242880,
        },
        Routing: RoutingConfig{
            TokenThreshold: 8000,
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
    }
}
