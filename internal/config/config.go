package config

import (
    "fmt"
	"os"
	"path/filepath"
	"strings"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/crypto"
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
    // ProxyMaxBodySize caps request bodies on reverse-proxy paths (model-hub,
    // fusion-mlx admin/fine-tune, /stats, model load/unload). 0 = default
    // (maxProxyBodySize, 256 MiB). E5 (audit): proxy paths forwarded r.Body to
    // httputil.ReverseProxy with no cap → unbounded body OOMs fusion-mlx.
    ProxyMaxBodySize        int64           `mapstructure:"proxy_max_body_size"`
    EnablePProf             bool            `mapstructure:"enable_pprof"`
    TLS                     *TLSConfig      `mapstructure:"tls"`
    AutoStart               *AutoStartConfig `mapstructure:"auto_start"`
    // UnixSocket enables an inbound Unix Domain Socket listener (client -> gateway)
    // in addition to (or instead of) the TCP listener. nil (default) = disabled,
    // backward compatible. Orthogonal to outbound UDS (backends.socket_path) and
    // to auto_start (which launches fusion-mlx over TCP/UDS independently).
    UnixSocket              *UnixSocketConfig `mapstructure:"unix_socket"`
}

// UnixSocketConfig configures the inbound UDS listener. Path is the socket
// file; Mode is the permission bits applied via os.Chmod (default 0660).
type UnixSocketConfig struct {
    Enabled bool   `mapstructure:"enabled"`
    Path    string `mapstructure:"path"` // e.g. /var/run/fusion-gateway.sock
    Mode    uint32 `mapstructure:"mode"` // 0 = default 0660
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
    ExpiresAt        string            `mapstructure:"expires_at"`
    BudgetLimit      float64           `mapstructure:"budget_limit"`
    DailyBudgetLimit float64           `mapstructure:"daily_budget_limit"`
    Metadata         map[string]string `mapstructure:"metadata"`
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
    // AnonymousRPM caps requests-per-minute from a single client IP when no API
    // key is in play (auth.enabled=false or passthrough). N5 (audit): the
    // anonymous path was fully unlimited — a single unauthenticated host could
    // fan out unbounded requests. 0 disables the per-IP cap (back-compat).
    AnonymousRPM int `mapstructure:"anonymous_rpm"`
}

type LocalPriorityConfig struct {
    Enabled               bool          `mapstructure:"enabled"`
    MaxSystemMemoryRatio  float64       `mapstructure:"max_system_memory_ratio"`
    MaxMLXMemoryRatio     float64       `mapstructure:"max_mlx_memory_ratio"`
    MaxConcurrent         int           `mapstructure:"max_concurrent"`
    SwapPageRateThreshold uint64        `mapstructure:"swap_page_rate_threshold"`
    // QueueEnabled engages a bounded FIFO wait-queue for local inference slots
    // ONLY when routing.mode=local (cloud disabled). When local is at
    // max_concurrent, a request waits up to QueueTimeout instead of falling
    // back to cloud (there is no cloud in mode=local). Default OFF — hybrid
    // mode behavior is unchanged (#102 ADR-001).
    QueueEnabled bool          `mapstructure:"queue_enabled"`
    QueueTimeout time.Duration `mapstructure:"queue_timeout"`
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

// MultimodalConfig governs how image/audio-bearing /v1/messages and
// /v1/chat/completions requests are routed. Without it a multimodal payload
// has no router signal (RouteRequest carries only Model/Text/Stream) so the
// text-only routing chain maps it to a text-only cloud model (e.g. glm5.2)
// that rejects images with 400 -> gateway 502 (Claude Code screenshot 502).
// LocalModel is the local vision model id to force-route multimodal requests
// to (local-first); empty rejects with a clear 400 instead of a masked
// cloud-400-as-502.
//
// CloudBackend + CloudModel (#120): the cloud fallback when the local node
// has no vision model loaded (e.g. fusion-browser Visual Grounding, where the
// local fusion-mlx serves text-only). CloudBackend is the provider name in the
// backends: pool (e.g. "openai"); CloudModel is the cloud VLM model id (e.g.
// "gpt-4o"). When both are set and LocalModel is unavailable, a multimodal
// /v1/chat/completions is routed to CloudBackend with Model rewritten to
// CloudModel — local-first when a VLM is loaded, cloud fallback otherwise.
// Empty CloudBackend leaves the prior behavior (LocalModel-only).
type MultimodalConfig struct {
    LocalModel  string `mapstructure:"local_model"`
    CloudBackend string `mapstructure:"cloud_backend"`
    CloudModel  string `mapstructure:"cloud_model"`
}

// AgentTaskConfig governs the TaskRegistry backstop against unbounded growth
// from hung agent tasks (RR7 audit P1). An agent task registered for the
// /v1/agent/tasks/{id}/cancel endpoint is normally Released when its stream
// goroutine exits; but an upstream hang (network half-open, model stuck) never
// exits → the entry, its CancelFunc-held ctx, and the goroutine leak forever.
// Two layers bound this: MaxEntries caps the map (a full registry skips new
// Register calls — the task still runs, just uncancelable via the endpoint,
// logged WARN); TTL + ReaperInterval drive a background reaper that force-
// cancels and evicts entries older than TTL. TTL=0 disables reaping (cap-only
// protection). Set once at construction; tuning needs a restart.
type AgentTaskConfig struct {
    TTL            time.Duration `mapstructure:"ttl"`
    MaxEntries     int           `mapstructure:"max_entries"`
    ReaperInterval time.Duration `mapstructure:"reaper_interval"`
}

type RoutingConfig struct {
    Mode                      string               `mapstructure:"mode"`
    DefaultModel              string               `mapstructure:"default_model"`
    TokenThreshold            int                  `mapstructure:"token_threshold"`
    OutputInputRatioThreshold float64              `mapstructure:"output_input_ratio_threshold"`
    OutputInputRatioMinInputTokens int             `mapstructure:"output_input_ratio_min_input_tokens"`
    RatioTiers                RatioTierConfig      `mapstructure:"ratio_tiers"`
    TokenTiers                TokenTierConfig      `mapstructure:"token_tiers"`
    LocalPriority              LocalPriorityConfig  `mapstructure:"local_priority"`
    CircuitBreaker             CircuitBreakerConfig `mapstructure:"circuit_breaker"`
    Fallback                   FallbackConfig       `mapstructure:"fallback"`
    Negotiation                NegotiationConfig    `mapstructure:"negotiation"`
    RateLimit                  RateLimitConfig      `mapstructure:"rate_limit"`
    Retry                      RetryConfig          `mapstructure:"retry"`
    Stream                     StreamConfig         `mapstructure:"stream"`
    IntentClassifier           IntentClassifierConfig `mapstructure:"intent_classifier"`
    HeuristicClassifier        HeuristicClassifierConfig `mapstructure:"heuristic_classifier"`
    Webhooks                   WebhooksConfig         `mapstructure:"webhooks"`
    Multimodal                 MultimodalConfig       `mapstructure:"multimodal"`
    AgentTasks                 AgentTaskConfig        `mapstructure:"agent_tasks"`
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

// HeuristicClassifierConfig configures the in-process sub-ms intent classifier
// that replaces the sync LLM RouterLightClassifier on the code path (latency
// lever for <20ms gateway end-to-end overhead). When it recognizes a coding
// intent with confidence >= MinConfidence, the engine dispatches straight to
// LocalBackend + LoRA hot-swap (code_adapter), skipping the LLM classifier.
// Disabled by default (backward compatible).
type HeuristicClassifierConfig struct {
    Enabled       bool          `mapstructure:"enabled"`
    // CodeAdapter is the LoRA adapter name (e.g. "lora-code") to hot-mount on
    // fusion-mlx via the per-request "adapters" field when a code intent is
    // detected. Empty = code intent detected but no adapter to mount, so the
    // engine defers to the rule chain (bare base model).
    CodeAdapter   string        `mapstructure:"code_adapter"`
    // CacheSize bounds the in-memory heuristic cache (LRU). 0 = no cache.
    CacheSize     int           `mapstructure:"cache_size"`
    // CacheTTL is the per-entry time-to-live. Stale entries are evicted on read.
    CacheTTL      time.Duration `mapstructure:"cache_ttl"`
    // MinConfidence is the score threshold above which a code intent dispatch
    // fires. Below it the classifier defers to the LLM classifier / rule chain.
    MinConfidence float64       `mapstructure:"min_confidence"`
    // TextScanBytes caps how many bytes of the request text are hashed into the
    // cache key / scanned for patterns. Bounds work on large prompts.
    TextScanBytes int           `mapstructure:"text_scan_bytes"`
}

// WebhooksConfig holds inbound webhook receivers. fusion-model-hub (and other
// upstream sources) POST lifecycle events here so the gateway can react without
// polling. Each sub-config pins its own shared secret for HMAC verification.
type WebhooksConfig struct {
    ModelHub ModelHubWebhookConfig `mapstructure:"model_hub"`
}

// ModelHubWebhookConfig configures the POST /webhooks/model-hub receiver.
// fusion-model-hub's dispatcher signs payloads with HMAC-SHA256 over the raw
// body and sends X-Webhook-Signature (hex) + X-Webhook-Event; the gateway
// re-computes the MAC with this secret and rejects on mismatch. On an
// adapter.* event the receiver triggers an immediate AdapterIndex refresh so
// newly published LoRA adapters are picked up without waiting for the 60s poll.
// Enabled defaults to false (no receiver registered); Enabled=true requires a
// non-empty Secret (validated in validate()).
type ModelHubWebhookConfig struct {
    Enabled bool   `mapstructure:"enabled"`
    Secret  string `mapstructure:"secret"`
}

type RetryConfig struct {
    MaxRetries           int           `mapstructure:"max_retries"`
    InitialBackoff       time.Duration `mapstructure:"initial_backoff"`
    MaxBackoff           time.Duration `mapstructure:"max_backoff"`
    RetryableStatusCodes []int         `mapstructure:"retryable_status_codes"`
}

// StreamConfig tunes SSE forwarding for /v1/messages against upstreams that
// stall mid-stream without closing the connection (issue #69: litellm/glm5.2
// stops pushing delta, gateway blocks indefinitely, client "response stopped
// arriving"). KeepaliveInterval emits periodic ping events so a slow-but-live
// upstream keeps the client alive; IdleTimeout cancels a truly dead upstream
// and synthesizes a clean message_stop so the client gets a short-but-complete
// response it can retry. Both <=0 disable the hardened path (backward compat).
type StreamConfig struct {
    KeepaliveInterval time.Duration `mapstructure:"keepalive_interval"`
    IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
    // Resume (issue #116): mid-stream-reconnect replay for the local MLX path.
    // ResumeEnabled gates stream_id assignment + per-event id + the rolling
    // buffer + the /v1/messages/{stream_id}/events replay endpoint. Disabled by
    // default — the buffered local path keeps the upstream pump alive past a
    // client disconnect (so a reconnect can resume), which holds the local slot
    // for the whole generation even after the client left. Cloud/first-party
    // Anthropic paths are never resumable (upstream has no cursor protocol).
    ResumeEnabled   bool          `mapstructure:"resume_enabled"`
    ResumeMaxEvents int           `mapstructure:"resume_max_events"`
    ResumeMaxBytes  int           `mapstructure:"resume_max_bytes"`
    ResumeTTL       time.Duration `mapstructure:"resume_ttl"`
    // ResumeMaxEntries is the GLOBAL cap on the number of concurrent stream
    // buffers (audit R7). resume_max_events/bytes bound ONE stream's rolling
    // window; this bounds how many streams can be buffered at once so a burst of
    // 1000 concurrent resumable SSE does not grow the buffers map to 1000 ×
    // maxBytes (1 GiB at 1 MiB each) before the periodic TTL reaper ticks. On
    // reaching the cap, Open evicts the oldest entry (oldest createdAt) — the
    // newest live streams stay resumable, the oldest (likely finalized/abandoned)
    // is dropped. 0 = unlimited (preserves the prior behavior for back-comat,
    // but DefaultConfig seeds 1024).
    ResumeMaxEntries int           `mapstructure:"resume_max_entries"`
    // MaxRequestDuration (R9 audit): per-request ceiling on the whole stream
    // lifetime. The stream HTTP client now uses Timeout:0 (R3) so Client.Timeout
    // no longer caps the body read; without a server-side deadline a cloud
    // backend configured Timeout:0 could stall a handler goroutine + client
    // connection indefinitely. 600s is a generous ceiling above the 180s idle
    // watchdog so it only catches true stalls, not legit long generation. 0 =
    // no server-side deadline (back-compat, but not recommended — rely on
    // watchdog + upstream timeouts only).
    MaxRequestDuration time.Duration `mapstructure:"max_request_duration"`
    // MaxConcurrentStreams (R10 audit): global cap on concurrent in-flight
    // streams across all backends. The local path is already hard-capped by
    // max_concurrent slots (RR4); this caps cloud fan-out so a request burst
    // cannot exhaust FDs/goroutines against cloud providers. Acquired at stream
    // handler entry; excess requests get 429. 0 = unlimited (back-compat).
    MaxConcurrentStreams int `mapstructure:"max_concurrent_streams"`
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
    // SocketPath enables outbound Unix Domain Socket to this backend. When set,
    // the adapter transport dials the unix socket instead of TCP; base_url is a
    // dummy host (convention: http://unix/). Empty (default) = plain TCP.
    SocketPath          string        `mapstructure:"socket_path"`
    // MaxConnsPerHost caps the number of active (in-flight) connections the
    // gateway will open to this single backend host at once. 0 = use the
    // transport-factory default. RR11 (audit P2): without this cap Go's
    // transport treats MaxConnsPerHost=0 as unlimited — a concurrent burst to a
    // single-host backend (local fusion-mlx, or one cloud vendor) opens hundreds
    // of simultaneous connections, exhausts file descriptors, and takes down the
    // whole gateway process (accept, logs, other backends all fail).
    MaxConnsPerHost     int           `mapstructure:"max_conns_per_host"`
    // MaxIdleConnsPerHost caps idle keep-alive connections cached per host. 0 =
    // use the transport-factory default (64). Lowering it shrinks the idle pool
    // (and the idle FD footprint) for low-QPS backends.
    MaxIdleConnsPerHost int           `mapstructure:"max_idle_conns_per_host"`
    // ResponseHeaderTimeout caps how long the transport waits for the upstream
    // to return response HEADERS after the request is fully sent. R5 (audit P0):
    // the prior TCP transport set MaxConnsPerHost/IdleConnTimeout but NOT this
    // field — a stuck upstream that accepted the connection but never replied
    // occupied a full Client.Timeout (120s) slot, and with a bounded connection
    // pool (MaxConnsPerHost=16) a few stuck upstreams saturated the pool and
    // stalled ALL requests to that backend. 0 = use the factory default (30s);
    // set lower for latency-sensitive backends, higher for slow first-token LLMs.
    ResponseHeaderTimeout time.Duration `mapstructure:"response_header_timeout"`
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
    Address     string `mapstructure:"address"`
    SharedToken string `mapstructure:"shared_token"`
    // IgnoreMasterStrategy is the #119 opt-out: when true, master mode still
    // syncs node membership from the master but keeps the LOCAL load_balancer
    // strategy for inference node selection (the gateway's own strategy wins).
    // Default false (zero value) → the gateway HONORS the strategy the master
    // owns (master.RoutingSummary.Strategy), so a strategy set in fusion-studio
    // is authoritative for both task.* and /v1/chat/completions. Zero-value
    // safe: no DefaultConfig seed needed, no mapstructure "omitted vs false"
    // ambiguity — false means "honor master" which is the desired default.
    IgnoreMasterStrategy bool `mapstructure:"ignore_strategy"`
    // MaxStaleAge (R3 audit P2): the maximum age a cached master strategy may
    // reach before resolveStrategy treats it as stale and falls back to the
    // local load_balancer. Before R3 a fetch failure left the cache untouched
    // with NO staleness bound — a master outage of any duration kept routing
    // by a possibly-dead strategy (route to dead nodes, sustained outage),
    // with no operator signal the strategy was过期. Default 2m: a strategy
    // older than 2m of failed syncs is treated as untrusted. 0 disables the
    // staleness bound (legacy永久-sticky behavior) — not recommended.
    MaxStaleAge time.Duration `mapstructure:"max_stale_age"`
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
    DataDir string      `mapstructure:"data_dir"`
    Redis   RedisConfig `mapstructure:"redis"`
}

type MCPConfig struct {
    Enabled bool `mapstructure:"enabled"`
    // #118: Host/Port bind a dedicated MCP HTTP listener (security-domain
    // isolation from the main :11432 mux). Previously these fields were logged
    // by mcp.Start() but never bound — dead config. ListenEnabled gates whether
    // the dedicated listener starts; false (default) keeps MCP on the shared
    // main mux + the MCP auth gate below. When true, Host/Port must be set.
    ListenEnabled bool   `mapstructure:"listen_enabled"`
    Host          string `mapstructure:"host"`
    Port          int    `mapstructure:"port"`
    // Token is the MCP-specific bearer credential. The MCP auth gate requires
    // it independent of the main auth chain, so auth.enabled=false does NOT open
    // MCP. Empty Token falls back to the master_key (admin-equivalent). If both
    // Token and master_key are empty with MCP enabled, the listener refuses to
    // start (fail-closed) — MCP routes must not be anonymously reachable.
    Token       string `mapstructure:"token"`
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
    // H7: reloadMu serializes the onReload handler-run + commit window. Without
    // it, two concurrent Reloads (admin PUT + fsnotify) both copy the handler
    // list under a shared RLock then run handlers + globalConfig.Store with NO
    // lock between them — handlers interleave, DrainAndApply/BuildProviders run
    // twice concurrently, and the unlocked gap before commit is race-prone.
    // Holding reloadMu across handler-run+commit makes the whole apply atomic
    // and mutually exclusive, without coupling to configMu (which guards the
    // handler-list registration under its own Lock). Handlers in main.go do not
    // reenter Reload/OnReload, so holding this lock across them is safe.
    reloadMu sync.Mutex
)

// encPrefix marks a backend api_key as AES-256-GCM ciphertext (base64 of
// nonce||ciphertext) that Load should decrypt before providers see it. N4
// (audit): backend api_keys were stored plaintext in config.yaml. Operators
// can now store `api_key: enc:<base64>` produced by the encryption helper; a
// plaintext value still loads (warned) so existing configs keep working until
// rotated.
const encPrefix = "enc:"

// decryptBackendAPIKeys (N4 audit) decrypts every backends[*].api_key that
// carries the "enc:" ciphertext prefix, using encryption.master_key. Builds a
// transient cipher from the configured master key — Load runs before
// server.New wires the cipher into connector/OAuth2, so the cipher is built
// here for the key-decrypt pass only. Plaintext keys (no prefix) pass through
// unchanged with a warn log nudging rotation. A malformed "enc:" value (bad
// base64, wrong key, truncated) is a hard error: a silently-decrypted-to-empty
// key would send an unauthenticated request upstream and look like an upstream
// bug. Fail loud at Load so the operator fixes the key, not the backend.
func decryptBackendAPIKeys(cfg *Config) error {
    if cfg.Encryption == nil || cfg.Encryption.MasterKey == "" {
        for name, bc := range cfg.Backends {
            if bc.APIKey != "" {
                slog.Warn("backend api_key stored plaintext and no encryption.master_key set; rotate to enc: form",
                    "backend", name)
            }
        }
        return nil
    }
    cipher, err := crypto.NewAESCipher(cfg.Encryption.MasterKey)
    if err != nil {
        return fmt.Errorf("init backend api_key cipher: %w", err)
    }
    for name, bc := range cfg.Backends {
        if bc.APIKey == "" {
            continue
        }
        if !strings.HasPrefix(bc.APIKey, encPrefix) {
            slog.Warn("backend api_key stored plaintext; rotate to enc: form for at-rest protection",
                "backend", name)
            continue
        }
        plain, err := cipher.Decrypt(strings.TrimPrefix(bc.APIKey, encPrefix))
        if err != nil {
            return fmt.Errorf("decrypt backends.%s.api_key: %w", name, err)
        }
        bc.APIKey = plain
        cfg.Backends[name] = bc
        slog.Info("backend api_key decrypted from enc: form", "backend", name)
    }
    return nil
}

func Load(path string) (*ConfigSnapshot, error) {
    v := viper.New()
    v.SetConfigFile(path)
    v.SetConfigType("yaml")
    v.AutomaticEnv()

    if err := v.ReadInConfig(); err != nil {
        return nil, fmt.Errorf("read config: %w", err)
    }

    // Seed with DefaultConfig so fields a YAML omits land on sane defaults, not
    // zero values that validate() then rejects (e.g. circuit_breaker thresholds
    // default to 5/3/30s, not 0). Matches the GetSnapshot() fallback behavior.
    // v.Unmarshal overwrites only the keys present in the YAML; absent keys keep
    // their seeded default. EI8: validate() now enforces positive thresholds,
    // so seeding defaults is required for minimal-but-valid YAMLs to load.
    cfg := DefaultConfig()
    // Clear the baked-in default JWT secret before unmarshaling — the loaded
    // config must supply its own (RR1 rejects the known-insecure default).
    if cfg.Admin != nil {
        cfg.Admin.JWTSecret = ""
    }
    if err := v.Unmarshal(&cfg); err != nil {
        return nil, fmt.Errorf("unmarshal config: %w", err)
    }

    if err := validate(&cfg); err != nil {
        return nil, fmt.Errorf("validate config: %w", err)
    }

    // N4 (audit): decrypt backends[*].api_key stored as "enc:" ciphertext.
    // Runs after validate (master_key placeholder refusal) and before the
    // snapshot is stored, so providers built from this config see plaintext.
    if err := decryptBackendAPIKeys(&cfg); err != nil {
        return nil, fmt.Errorf("decrypt backend api_keys: %w", err)
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

    // H7: serialize handler-run under reloadMu so FireReload and Reload do not
    // run handlers concurrently. FireReload itself does not commit (callers
    // own the snapshot swap), but it shares the handler list with Reload, so
    // the same exclusion applies to keep apply paths mutually consistent.
    reloadMu.Lock()
    defer reloadMu.Unlock()
    for _, fn := range handlers {
        fn(old, newSnap)
    }
}

// Reload re-reads the config file from disk, builds a new ConfigSnapshot, runs
// all OnReload handlers (C5: before commit), commits the snapshot, and audits
// the diff. It is the deterministic reload path used by /admin/config/reload —
// fsnotify file-watching (WatchAndReload) is unreliable on macOS (kqueue misses
// edits made after process start), so the admin endpoint must trigger reload
// explicitly rather than relying on the file watch (issue #57).
func Reload(path string) (*ConfigSnapshot, error) {
    slog.Info("config reload requested", "path", path)
    oldSnap := GetSnapshot()

    v := viper.New()
    v.SetConfigFile(path)
    v.SetConfigType("yaml")
    v.AutomaticEnv()
    if err := v.ReadInConfig(); err != nil {
        slog.Error("reload: failed to read config", "error", err)
        return nil, fmt.Errorf("read config: %w", err)
    }

    // Seed with DefaultConfig (same as Load) so omitted YAML keys keep sane
    // defaults, not zero values that validate() rejects. EI8 made validate()
    // enforce positive circuit-breaker thresholds, so minimal YAMLs need the
    // seed. Clear the baked-in default JWT secret (RR1 rejects it).
    cfg := DefaultConfig()
    if cfg.Admin != nil {
        cfg.Admin.JWTSecret = ""
    }
    if err := v.Unmarshal(&cfg); err != nil {
        slog.Error("reload: failed to unmarshal config", "error", err)
        return nil, fmt.Errorf("unmarshal config: %w", err)
    }

    if err := validate(&cfg); err != nil {
        slog.Error("reload: config validation failed", "error", err)
        return nil, fmt.Errorf("validate config: %w", err)
    }

    ver := configVersion.Add(1)
    newSnap := &ConfigSnapshot{
        Config:   cfg,
        Version:  ver,
        LoadedAt: time.Now(),
    }

    // C5 fix: run handlers BEFORE committing new snapshot. H7: serialize the
    // handler-run + commit under reloadMu so concurrent Reloads (admin PUT +
    // fsnotify) cannot interleave handlers or commit mid-apply — the prior
    // unlocked gap between RUnlock and Store was race-prone.
    configMu.RLock()
    handlers := make([]func(old, new *ConfigSnapshot), len(onReloadHandlers))
    copy(handlers, onReloadHandlers)
    configMu.RUnlock()

    reloadMu.Lock()
    for _, fn := range handlers {
        fn(oldSnap, newSnap)
    }
    // commit new snapshot only after all handlers succeed
    globalConfig.Store(newSnap)
    reloadMu.Unlock()

    slog.Info("config reloaded", "version", ver)

    AuditConfigChange(oldSnap, newSnap)
    return newSnap, nil
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
        if _, err := Reload(watchPath); err != nil {
            slog.Error("fsnotify reload failed", "error", err)
        }
    })
}

func validate(cfg *Config) error {
    if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
        return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
    }

    // Inbound UDS listener: when enabled, a socket path is required.
    if cfg.Server.UnixSocket != nil && cfg.Server.UnixSocket.Enabled && cfg.Server.UnixSocket.Path == "" {
        return fmt.Errorf("server.unix_socket.enabled is true but path is empty")
    }

    if cfg.Routing.TokenThreshold <= 0 {
        return fmt.Errorf("token_threshold must be positive, got: %d", cfg.Routing.TokenThreshold)
    }

    if cfg.Routing.Mode != "" && cfg.Routing.Mode != "local" && cfg.Routing.Mode != "cloud" && cfg.Routing.Mode != "hybrid" {
        return fmt.Errorf("routing.mode must be local, cloud, or hybrid, got: %q", cfg.Routing.Mode)
    }

    // Inbound model-hub webhook: when enabled, a shared HMAC secret is required
    // to verify signed payloads (fusion-model-hub signs with HMAC-SHA256).
    if cfg.Routing.Webhooks.ModelHub.Enabled && cfg.Routing.Webhooks.ModelHub.Secret == "" {
        return fmt.Errorf("routing.webhooks.model_hub.enabled is true but secret is empty")
    }

    if cfg.Routing.OutputInputRatioThreshold < 0 {
        return fmt.Errorf("output_input_ratio_threshold must be non-negative, got: %f", cfg.Routing.OutputInputRatioThreshold)
    }
    if cfg.Routing.OutputInputRatioMinInputTokens < 0 {
        return fmt.Errorf("output_input_ratio_min_input_tokens must be non-negative, got: %d", cfg.Routing.OutputInputRatioMinInputTokens)
    }

    // R7: resume stream buffers. ResumeMaxEntries < 0 is invalid (0 = unlimited
    // is the documented opt-out). ResumeTTL must be positive when resume is
    // enabled — a zero/negative TTL makes ReapExpired a no-op (ttl<=0 guard)
    // so the buffer cap is the only bound, defeating the time-based eviction.
    if cfg.Routing.Stream.ResumeMaxEntries < 0 {
        return fmt.Errorf("resume_max_entries must be >= 0 (0 = unlimited), got: %d", cfg.Routing.Stream.ResumeMaxEntries)
    }
    if cfg.Routing.Stream.ResumeEnabled && cfg.Routing.Stream.ResumeTTL <= 0 {
        return fmt.Errorf("resume_ttl must be positive when resume_enabled is true, got: %s", cfg.Routing.Stream.ResumeTTL)
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
    // RR1 (audit P0): reject the baked-in default JWT secret — an unchanged
    // deployment ships with a publicly-known signing key, allowing anyone who
    // reads the source to forge admin JWTs. Fail-closed at startup rather than
    // relying on operators to override an already-effective default.
    if cfg.Admin != nil && cfg.Admin.Enabled {
        if cfg.Admin.JWTSecret == "" {
            return fmt.Errorf("admin.jwt_secret is required when admin is enabled")
        }
        if isKnownInsecureJWTSecret(cfg.Admin.JWTSecret) {
            return fmt.Errorf("admin.jwt_secret must not be a known placeholder/default; set a unique secret of at least 32 characters")
        }
        if len(cfg.Admin.JWTSecret) < 32 {
            return fmt.Errorf("admin.jwt_secret must be at least 32 characters, got %d", len(cfg.Admin.JWTSecret))
        }
        for username, password := range cfg.Admin.Users {
            if len(password) < 8 {
                return fmt.Errorf("admin user %q password must be at least 8 characters, got %d", username, len(password))
            }
            // R7 (audit): reject known placeholder admin passwords. bcrypt
            // already rejects empty, but "change-me" / "password" would hash
            // and authenticate — a publicly-known admin credential is an open
            // door. (Plaintext passwords here are hashed at startup by AdminAuth.)
            if isKnownInsecureSecret(password) {
                return fmt.Errorf("admin user %q password must not be a known placeholder; set a unique strong password", username)
            }
        }
    }

    // V2 fix: validate routing config
    if cfg.Routing.LocalPriority.MaxConcurrent < 0 {
        return fmt.Errorf("max_concurrent must be non-negative, got %d", cfg.Routing.LocalPriority.MaxConcurrent)
    }
    if cfg.Routing.LocalPriority.QueueTimeout < 0 {
        return fmt.Errorf("queue_timeout must be non-negative, got %d", cfg.Routing.LocalPriority.QueueTimeout)
    }
    if cfg.Routing.AgentTasks.TTL < 0 {
        return fmt.Errorf("agent_tasks.ttl must be non-negative, got %d", cfg.Routing.AgentTasks.TTL)
    }
    if cfg.Routing.AgentTasks.MaxEntries < 0 {
        return fmt.Errorf("agent_tasks.max_entries must be non-negative, got %d", cfg.Routing.AgentTasks.MaxEntries)
    }
    if cfg.Routing.AgentTasks.ReaperInterval < 0 {
        return fmt.Errorf("agent_tasks.reaper_interval must be non-negative, got %d", cfg.Routing.AgentTasks.ReaperInterval)
    }
    if cfg.Cache.MaxMemoryMB < 0 {
        return fmt.Errorf("cache.max_memory_mb must be non-negative, got %d", cfg.Cache.MaxMemoryMB)
    }
    if cfg.Cache.MaxEntries < 0 {
        return fmt.Errorf("cache.max_entries must be non-negative, got %d", cfg.Cache.MaxEntries)
    }

    // EI8 (audit P3): validate the remaining numeric knobs that the prior
    // validate() left bare. A typo like max_mlx_memory_ratio: -1 or a circuit
    // breaker timeout of 0 silently passes validate and produces runtime
    // behavior opposite to intent (0 timeout = immediate/open-or-never-open
    // depending on the comparison; negative ratio never trips). validate exists
    // to catch these at startup, not let them surface as "routing never goes
    // cloud" mysteries.
    if cfg.Routing.LocalPriority.MaxMLXMemoryRatio <= 0 || cfg.Routing.LocalPriority.MaxMLXMemoryRatio > 1 {
        return fmt.Errorf("max_mlx_memory_ratio must be in (0,1], got: %f", cfg.Routing.LocalPriority.MaxMLXMemoryRatio)
    }
    if cfg.Routing.CircuitBreaker.FailureThreshold <= 0 {
        return fmt.Errorf("circuit_breaker.failure_threshold must be positive, got: %d", cfg.Routing.CircuitBreaker.FailureThreshold)
    }
    if cfg.Routing.CircuitBreaker.SuccessThreshold <= 0 {
        return fmt.Errorf("circuit_breaker.success_threshold must be positive, got: %d", cfg.Routing.CircuitBreaker.SuccessThreshold)
    }
    if cfg.Routing.CircuitBreaker.Timeout < 0 {
        return fmt.Errorf("circuit_breaker.timeout must be non-negative, got: %s", cfg.Routing.CircuitBreaker.Timeout)
    }
    if cfg.Routing.CircuitBreaker.HalfOpenMaxRequests <= 0 {
        return fmt.Errorf("circuit_breaker.half_open_max_requests must be positive, got: %d", cfg.Routing.CircuitBreaker.HalfOpenMaxRequests)
    }
    // RR11 MaxConnsPerHost: 0 means unlimited (Go transport default), which is
    // exactly the FD-exhaustion vector RR11 closed. Reject negative (invalid);
    // 0 is allowed only as the explicit "unlimited" opt-out the operator typed
    // knowingly — but we surface it as a warning so a missing/zero value is not
    // silently unlimited. Negative is a hard error.
    for name, backend := range cfg.Backends {
        if backend.Enabled {
            if backend.MaxConnsPerHost < 0 {
                return fmt.Errorf("backends.%s.max_conns_per_host must be non-negative, got: %d", name, backend.MaxConnsPerHost)
            }
            if backend.MaxIdleConnsPerHost < 0 {
                return fmt.Errorf("backends.%s.max_idle_conns_per_host must be non-negative, got: %d", name, backend.MaxIdleConnsPerHost)
            }
            // R5: ResponseHeaderTimeout guards against stuck upstreams. 0 = factory
            // default (30s). Negative is invalid; an absurdly large value (>10m)
            // defeats the cap's purpose and is rejected to catch unit mistakes (s vs ms).
            if backend.ResponseHeaderTimeout < 0 {
                return fmt.Errorf("backends.%s.response_header_timeout must be non-negative, got: %d", name, backend.ResponseHeaderTimeout)
            }
            if backend.ResponseHeaderTimeout > 10*time.Minute {
                return fmt.Errorf("backends.%s.response_header_timeout must be <= 10m, got: %d", name, backend.ResponseHeaderTimeout)
            }
            if backend.MaxConnsPerHost == 0 {
                slog.Warn("backend max_conns_per_host is 0 (unlimited); set a positive cap to bound FDs", "backend", name)
            }
            // C1 (audit P1): reject a known-placeholder backend api_key on an
            // ENABLED backend. Cloud provider keys ("your-volcengine-key",
            // "sk-your-openai-key", "sk-ant-your-key", ...) are shipped as
            // template stubs; a careless operator enabling the backend without
            // rotating the key would send requests with a publicly-known
            // credential. Disabled backends keep their stub (reserved, not
            // live). Empty api_key is allowed (local backends need none). The
            // auth.APIKeys loop above covers the gateway auth keys; this covers
            // the upstream provider credentials — a separate credential surface
            // the prior R7 blacklist never touched.
            if backend.APIKey != "" && isKnownInsecureSecret(backend.APIKey) {
                return fmt.Errorf("backends.%s.api_key must not be a known placeholder; set the real upstream credential or disable the backend", name)
            }
        }
    }

    // V2 fix: validate cluster node addresses
    for _, node := range cfg.Cluster.Nodes {
        if node.Address != "" && !strings.HasPrefix(node.Address, "http://") && !strings.HasPrefix(node.Address, "https://") {
            return fmt.Errorf("cluster node %q address must start with http:// or https://, got %q", node.ID, node.Address)
        }
    }

    // #87: validate auth key budgets non-negative
    // RR2 (audit P0): reject empty Key entries. A configured api_keys entry
    // with an empty key creates a key that authenticates nothing yet still
    // occupies a config slot (and a name/index the admin UI references). The
    // runtime auth middleware already rejects an empty *submitted* key, but a
    // config-side reject fails fast at startup so the operator learns before a
    // request is ever attempted. Only enforced when auth is enabled; a disabled
    // auth block has no live keys to validate.
    // R7 (audit): reject a known-placeholder master_key when auth is enabled.
    // The master_key is admin-equivalent (it bypasses rate limits + opens MCP),
    // so a publicly-known value is a full bypass. Empty is allowed (master_key
    // is optional) — only the placeholder literals are refused.
    if cfg.Auth.Enabled && isKnownInsecureSecret(cfg.Auth.MasterKey) {
        return fmt.Errorf("auth.master_key must not be a known placeholder; set a unique strong secret or leave empty to disable")
    }

    for _, k := range cfg.Auth.APIKeys {
        if cfg.Auth.Enabled && strings.TrimSpace(k.Key) == "" {
            return fmt.Errorf("auth key %q has an empty key; set a non-empty key or remove the entry", k.Name)
        }
        // R7 (audit): reject known-placeholder API keys when auth is enabled.
        // A publicly-known key authenticates anyone who read the repo.
        if cfg.Auth.Enabled && isKnownInsecureSecret(k.Key) {
            return fmt.Errorf("auth key %q must not be a known placeholder; set a unique strong key", k.Name)
        }
        if k.BudgetLimit < 0 {
            return fmt.Errorf("auth key %q budget_limit must be non-negative, got %f", k.Name, k.BudgetLimit)
        }
        if k.DailyBudgetLimit < 0 {
            return fmt.Errorf("auth key %q daily_budget_limit must be non-negative, got %f", k.Name, k.DailyBudgetLimit)
        }
    }

    // R7/C2 (audit P1): encryption.master_key protects OAuth2 connector tokens
    // at rest (data/connections.json). Without it, tokens persist PLAINTEXT to
    // disk. Audit C2 grades this a release-blocking MUST ("不满足 C1 或 C2 不得
    // 对外发布"), so a deployment that activates OAuth2 flows (OIDC enabled OR a
    // connector persistence_path configured) WITHOUT a master_key is
    // fail-closed at Load rather than warned. A local-only no-OIDC/no-connector
    // deployment needs no master_key (nothing to encrypt) and is unaffected.
    // Signals: cfg.OIDC.Enabled = OAuth2 flows active; cfg.Connector != nil =
    // connector framework configured (enablement is runtime via stored keys,
    // but presence of the block means the operator intends to use it).
    oauth2Active := cfg.OIDC.Enabled || cfg.Connector != nil
    if oauth2Active {
        if cfg.Encryption == nil || cfg.Encryption.MasterKey == "" {
            return fmt.Errorf("encryption.master_key is required when OIDC or connector is enabled — without it OAuth2/connector tokens persist PLAINTEXT to disk; set a >=32-char random master_key (audit C2, release-blocking)")
        }
        if isKnownInsecureSecret(cfg.Encryption.MasterKey) {
            return fmt.Errorf("encryption.master_key must not be a known placeholder when OIDC or connector is enabled — OAuth2/connector tokens would be encrypted with a publicly-known key; set a unique >=32-char random master_key (audit C2, release-blocking)")
        }
        if len(cfg.Encryption.MasterKey) < 32 {
            return fmt.Errorf("encryption.master_key must be at least 32 characters when OIDC or connector is enabled, got %d (audit C2)", len(cfg.Encryption.MasterKey))
        }
    }

    // Outbound UDS: a backend with socket_path dials a unix socket instead of
    // TCP. base_url is a dummy host then. Warn (not fail) so callers can opt in
    // without hard requirements; the convention is http://unix/.
    for name, backend := range cfg.Backends {
        if backend.Enabled && backend.SocketPath != "" {
            slog.Warn("backend configured for outbound UDS",
                "backend", name,
                "socket_path", backend.SocketPath,
                "base_url", backend.BaseURL,
                "note", "base_url is a dummy host; transport dials the unix socket",
            )
        }
    }

    // #118: MCP dedicated-listener + auth gate validation. When MCP is enabled
    // with a dedicated listener, Host/Port must be set (the listener must bind
    // somewhere). When MCP is enabled at all, an MCP credential must exist —
    // either mcp.token or auth.master_key — so the MCP auth gate can enforce
    // #119: cluster master mode makes the fusion-multi-node master the single
    // source of inference node membership + strategy. A master-mode config with
    // no master address would silently fall back to an empty node set (every
    // request cloud-degrades). Fail at load so the misconfig is loud, not a
    // silent cloud-only gateway masquerading as clustered.
    if cfg.Cluster.Enabled && cfg.Cluster.Mode == ClusterModeMaster && cfg.Cluster.Master.Address == "" {
        return fmt.Errorf("cluster.mode=master requires cluster.master.address (the fusion-multi-node master API URL); got empty — set it or use mode=standalone")
    }
    // R3: seed a sane max_stale_age default when omitted (0 =永久-sticky, the
    // pre-R3 bug). 2m bounds how long a cached master strategy is trusted
    // across failed syncs before resolveStrategy falls back to local. A
    // negative value is rejected; 0 intentionally left as the explicit legacy
    // opt-out for operators who want the old永久-sticky behavior.
    if cfg.Cluster.Enabled && cfg.Cluster.Mode == ClusterModeMaster {
        if cfg.Cluster.Master.MaxStaleAge < 0 {
            return fmt.Errorf("cluster.master.max_stale_age must be >= 0 (0=disable staleness bound, legacy永久-sticky), got %v", cfg.Cluster.Master.MaxStaleAge)
        }
        if cfg.Cluster.Master.MaxStaleAge == 0 {
            cfg.Cluster.Master.MaxStaleAge = 2 * time.Minute
            slog.Info("cluster.master.max_stale_age defaulted", "max_stale_age", cfg.Cluster.Master.MaxStaleAge)
        }
    }

    // E5 (audit P2): validate the reverse-proxy body cap. 0 is allowed (the
    // handlers fall back to the maxProxyBodySize const default at read time);
    // negative is rejected. A positive cap is what actually bounds the body —
    // the const alone was never enforced because the proxy paths wrapped
    // r.Body with no MaxBytesReader at all.
    if cfg.Server.ProxyMaxBodySize < 0 {
        return fmt.Errorf("server.proxy_max_body_size must be >= 0 (0=default %d bytes), got %d", int64(256<<20), cfg.Server.ProxyMaxBodySize)
    }

    // access independent of the main auth chain (auth.enabled=false must NOT
    // open MCP). Fail-closed: an enabled MCP with no credential is rejected at
    // config load, not silently exposed.
    if cfg.MCP.Enabled {
        if cfg.MCP.ListenEnabled {
            if cfg.MCP.Host == "" {
                return fmt.Errorf("mcp.host must be set when mcp.listen_enabled is true (dedicated MCP listener bind address)")
            }
            if cfg.MCP.Port <= 0 || cfg.MCP.Port > 65535 {
                return fmt.Errorf("mcp.port must be in (0,65535] when mcp.listen_enabled is true, got %d", cfg.MCP.Port)
            }
        }
        if cfg.MCP.Token == "" && cfg.Auth.MasterKey == "" {
            return fmt.Errorf("mcp is enabled but has no credential: set mcp.token or auth.master_key so the MCP auth gate can enforce access (auth.enabled=false must not open MCP)")
        }
    }

    return nil
}

func ExpandPath(p string) string {
    if p == "" {
        return ""
    }
    if strings.HasPrefix(p, "~" + string(filepath.Separator)) {
        home, err := os.UserHomeDir()
        if err != nil {
            slog.Warn("expandPath: cannot resolve home dir, returning path as-is", "path", p, "error", err)
            return p
        }
        return filepath.Join(home, p[2:])
    }
    if p == "~" {
        home, err := os.UserHomeDir()
        if err != nil {
            return p
        }
        return home
    }
    return p
}

// defaultJWTSecret is the placeholder secret baked into DefaultConfig so a
// from-scratch config parses. RR1 (audit P0): Validate rejects this value when
// admin is enabled — operators MUST override it before enabling the admin
// dashboard. Keeping a named constant (not a literal at both sites) prevents
// the default and the validator from silently diverging.
const defaultJWTSecret = "default-dev-secret-change-in-production-32ch"

// knownInsecureJWTSecrets is the denylist of JWT secrets that Validate rejects
// when admin is enabled. RR1 (audit P0): besides the built-in default, the
// shipped config.yaml carries obvious placeholder strings ("change-me-*") that
// a careless operator may leave in place. A publicly-known or self-describing
// placeholder is equivalent to no secret at all — anyone reading the repo can
// forge admin JWTs. Fail-closed at startup forces a real secret.
var knownInsecureJWTSecrets = map[string]bool{
    defaultJWTSecret:                          true,
    "change-me-at-least-32-chars-long-random-secret": true,
    "change-me-to-a-random-secret-32-chars-min":      true,
}

func isKnownInsecureJWTSecret(s string) bool {
    t := strings.ToLower(strings.TrimSpace(s))
    if t == "" {
        return false
    }
    if knownInsecureJWTSecrets[t] {
        return true
    }
    // C1 (audit P1-ops): the shipped config.yaml carries
    // "fg-local-dev-jwt-secret-...-DO-NOT-SHIP" which evades the exact table.
    // Reuse the generic placeholder pattern layer (fg- prefix, do-not-ship /
    // change-me substrings) so self-describing JWT placeholders are rejected.
    return looksLikePlaceholder(t)
}

// knownInsecureSecrets is the R7 (audit P2) denylist for generic secrets —
// auth.master_key, api_keys[*].key, and admin passwords. These are the
// placeholder/default literals a shipped config.yaml carries that a careless
// operator may leave in place; a publicly-known secret is equivalent to no
// secret at all. Fail-closed at startup forces rotation before going live.
// Covers the ops-only P1 items C1 (credential rotation) + C2
// (encryption.master_key) at the config layer: the shipped config cannot Load
// until the placeholders are replaced.
var knownInsecureSecrets = map[string]bool{
    "change-me":                true,
    "changeme":                 true,
    "change-me-now":            true,
    "placeholder":              true,
    "DO-NOT-SHIP":              true,
    "do-not-ship":              true,
    "default":                  true,
    "secret":                   true,
    "password":                 true,
    "master-key":               true,
    "master_key":               true,
    "your-master-key-here":     true,
    "your-secret-key-here":     true,
    "replace-me":               true,
    "todo":                     true,
}

// isKnownInsecureSecret reports whether s is a known placeholder/default
// secret. R7 audit fix; C1 (audit P1-ops) extends it from exact-match-only to
// pattern detection: the shipped config.example.yaml carries literals like
// "fg-master-key-change-me", "fg-admin-key", "your-baichuan-key",
// "change-me-secure-password" that all EVADE the exact-match table (no entry
// for those exact strings), so a careless operator could Load unchanged and
// ship publicly-known credentials. The pattern layer catches the families:
//   - "fg-" prefix: repo-baked marker (fg-master-key-*, fg-admin-key, ...)
//   - "your-" prefix + "-key" suffix: template stub (your-baichuan-key, ...)
//   - "change-me" / "do-not-ship" / "replace-me" / "placeholder" /
//     "set-me" / "example" / "todo" / "changeme" substrings
// Case-insensitive, trims surrounding whitespace. Exact-match table kept for
// short bare words ("default", "secret", "password") that substring matching
// would over-flag (a real key containing "secret" is plausible; a real key
// that IS "secret" is not).
func isKnownInsecureSecret(s string) bool {
    t := strings.ToLower(strings.TrimSpace(s))
    if t == "" {
        return false
    }
    if knownInsecureSecrets[t] {
        return true
    }
    return looksLikePlaceholder(t)
}

// looksLikePlaceholder applies the C1 pattern layer: prefix/suffix/substring
// families that the shipped config templates use. Kept conservative to avoid
// false positives on real credentials — only well-known stub markers.
func looksLikePlaceholder(t string) bool {
    // Prefix families.
    if strings.HasPrefix(t, "fg-") {
        return true
    }
    if strings.HasPrefix(t, "your-") && strings.HasSuffix(t, "-key") {
        return true
    }
    if strings.HasPrefix(t, "sk-your-") || strings.HasPrefix(t, "sk-ant-your-") {
        return true
    }
    // Substring families (case already lowered).
    substringMarkers := []string{
        "change-me", "changeme",
        "do-not-ship", "do_not_ship",
        "replace-me", "replace_me",
        "placeholder",
        "set-me", "set_me",
        "your-key", "your_key",
        "your-master", "your-secret",
        "example-key", "example_key",
        "todo", "xxxxx",
    }
    for _, m := range substringMarkers {
        if strings.Contains(t, m) {
            return true
        }
    }
    return false
}

func DefaultConfig() Config {
    return Config{
        Server: ServerConfig{
            Host:                   "0.0.0.0",
            Port:                   11432,
            LogLevel:               "info",
            GracefulShutdownTimeout: 15,
            MaxRequestBodySize:     5242880,
            ProxyMaxBodySize:       256 << 20,
        },
        Routing: RoutingConfig{
            Mode:                      "hybrid",
            DefaultModel:              "",
            TokenThreshold:            8000,
            OutputInputRatioThreshold: 0.6,
            OutputInputRatioMinInputTokens: 32,
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
                QueueEnabled:          false,
                QueueTimeout:          5 * time.Second,
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
            HeuristicClassifier: HeuristicClassifierConfig{
                Enabled:       false,
                CodeAdapter:   "lora-code",
                CacheSize:     4096,
                CacheTTL:      5 * time.Minute,
                MinConfidence: 0.6,
                TextScanBytes: 4096,
            },
            Webhooks: WebhooksConfig{
                ModelHub: ModelHubWebhookConfig{
                    Enabled: false,
                },
            },
            Stream: StreamConfig{
                KeepaliveInterval:    15 * time.Second,
                IdleTimeout:          180 * time.Second,
                ResumeEnabled:        false,
                ResumeMaxEvents:      256,
                ResumeMaxBytes:       1 << 20,
                ResumeTTL:            10 * time.Minute,
                ResumeMaxEntries:     1024,
                MaxRequestDuration:   600 * time.Second,
                MaxConcurrentStreams: 256,
            },
            AgentTasks: AgentTaskConfig{
                TTL:            30 * time.Minute,
                MaxEntries:     10000,
                ReaperInterval: 5 * time.Minute,
            },
            // N5 (audit): per-IP cap for anonymous (no-key) requests. Default
            // 60 RPM — engaged once rate_limit.enabled. 0 disables (back-compat).
            RateLimit: RateLimitConfig{
                AnonymousRPM: 60,
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
            // RR1 (audit P0): admin dashboard disabled by default. Enabling
            // requires a unique jwt_secret (>=32 chars, not the built-in default),
            // enforced by Validate. Defaulting off is fail-closed: an out-of-box
            // deployment exposes no admin surface until an operator consciously
            // turns it on with a real secret.
            Enabled:   false,
            JWTSecret: defaultJWTSecret,
            LogMaxLen: 10000,
        },
    }
}
