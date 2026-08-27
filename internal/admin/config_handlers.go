package admin

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// ─── Server Config ────────────────────────────────────────────

type serverConfigResponse struct {
    Host                    string `json:"host"`
    Port                    int    `json:"port"`
    LogLevel                string `json:"log_level"`
    GracefulShutdownTimeout int    `json:"graceful_shutdown_timeout"`
    MaxRequestBodySize      int64  `json:"max_request_body_size"`
    EnablePProf             bool   `json:"enable_pprof"`
}

type serverConfigUpdate struct {
    Host                    *string `json:"host,omitempty"`
    Port                    *int    `json:"port,omitempty"`
    LogLevel                *string `json:"log_level,omitempty"`
    GracefulShutdownTimeout *int    `json:"graceful_shutdown_timeout,omitempty"`
    MaxRequestBodySize      *int64  `json:"max_request_body_size,omitempty"`
    EnablePProf             *bool   `json:"enable_pprof,omitempty"`
}

func (h *Handler) handleServerConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        cfg := &config.GetSnapshot().Config
        writeJSON(w, http.StatusOK, serverConfigResponse{
            Host:                    cfg.Server.Host,
            Port:                    cfg.Server.Port,
            LogLevel:                cfg.Server.LogLevel,
            GracefulShutdownTimeout: cfg.Server.GracefulShutdownTimeout,
            MaxRequestBodySize:      cfg.Server.MaxRequestBodySize,
            EnablePProf:             cfg.Server.EnablePProf,
        })
    case http.MethodPut:
        var req serverConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "server")
            applyString(sec, "host", req.Host)
            applyInt(sec, "port", req.Port)
            applyString(sec, "log_level", req.LogLevel)
            applyInt(sec, "graceful_shutdown_timeout", req.GracefulShutdownTimeout)
            if req.MaxRequestBodySize != nil {
                sec["max_request_body_size"] = *req.MaxRequestBodySize
            }
            applyBool(sec, "enable_pprof", req.EnablePProf)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("server config updated via admin API")
        sec := getOrCreateSection(doc, "server")
        writeJSON(w, http.StatusOK, serverConfigResponse{
            Host:                    getString(sec, "host"),
            Port:                    getInt(sec, "port"),
            LogLevel:                getString(sec, "log_level"),
            GracefulShutdownTimeout: getInt(sec, "graceful_shutdown_timeout"),
            MaxRequestBodySize:      int64(getFloat64(sec, "max_request_body_size")),
            EnablePProf:             getBool(sec, "enable_pprof"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Rate Limit Config ────────────────────────────────────────

type rateLimitConfigResponse struct {
    Enabled        bool `json:"enabled"`
    GlobalRPM      int  `json:"global_rpm"`
    GlobalTPM      int  `json:"global_tpm"`
    KeyEnforcement bool `json:"key_enforcement"`
}

type rateLimitConfigUpdate struct {
    Enabled        *bool `json:"enabled,omitempty"`
    GlobalRPM      *int  `json:"global_rpm,omitempty"`
    GlobalTPM      *int  `json:"global_tpm,omitempty"`
    KeyEnforcement *bool `json:"key_enforcement,omitempty"`
}

func (h *Handler) handleRateLimitConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        rl := config.GetSnapshot().Config.Routing.RateLimit
        writeJSON(w, http.StatusOK, rateLimitConfigResponse{
            Enabled: rl.Enabled, GlobalRPM: rl.GlobalRPM,
            GlobalTPM: rl.GlobalTPM, KeyEnforcement: rl.KeyEnforcement,
        })
    case http.MethodPut:
        var req rateLimitConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "routing", "rate_limit")
            applyBool(sec, "enabled", req.Enabled)
            applyInt(sec, "global_rpm", req.GlobalRPM)
            applyInt(sec, "global_tpm", req.GlobalTPM)
            applyBool(sec, "key_enforcement", req.KeyEnforcement)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("rate-limit config updated via admin API")
        sec := getOrCreateSection(doc, "routing", "rate_limit")
        writeJSON(w, http.StatusOK, rateLimitConfigResponse{
            Enabled: getBool(sec, "enabled"), GlobalRPM: getInt(sec, "global_rpm"),
            GlobalTPM: getInt(sec, "global_tpm"), KeyEnforcement: getBool(sec, "key_enforcement"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Retry Config ─────────────────────────────────────────────

type retryConfigResponse struct {
    MaxRetries           int    `json:"max_retries"`
    InitialBackoff       string `json:"initial_backoff"`
    MaxBackoff           string `json:"max_backoff"`
    RetryableStatusCodes []int  `json:"retryable_status_codes"`
}

type retryConfigUpdate struct {
    MaxRetries           *int    `json:"max_retries,omitempty"`
    InitialBackoff       *string `json:"initial_backoff,omitempty"`
    MaxBackoff           *string `json:"max_backoff,omitempty"`
    RetryableStatusCodes *[]int  `json:"retryable_status_codes,omitempty"`
}

func (h *Handler) handleRetryConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        rt := config.GetSnapshot().Config.Routing.Retry
        writeJSON(w, http.StatusOK, retryConfigResponse{
            MaxRetries: rt.MaxRetries, InitialBackoff: rt.InitialBackoff.String(),
            MaxBackoff: rt.MaxBackoff.String(), RetryableStatusCodes: rt.RetryableStatusCodes,
        })
    case http.MethodPut:
        var req retryConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "routing", "retry")
            applyInt(sec, "max_retries", req.MaxRetries)
            if req.InitialBackoff != nil {
                sec["initial_backoff"] = *req.InitialBackoff
            }
            if req.MaxBackoff != nil {
                sec["max_backoff"] = *req.MaxBackoff
            }
            if req.RetryableStatusCodes != nil {
                codes := make([]interface{}, len(*req.RetryableStatusCodes))
                for i, c := range *req.RetryableStatusCodes {
                    codes[i] = c
                }
                sec["retryable_status_codes"] = codes
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("retry config updated via admin API")
        sec := getOrCreateSection(doc, "routing", "retry")
        retryableCodes := []int{}
        if raw, ok := sec["retryable_status_codes"].([]interface{}); ok {
            for _, v := range raw {
                switch iv := v.(type) {
                case int:
                    retryableCodes = append(retryableCodes, iv)
                case float64:
                    retryableCodes = append(retryableCodes, int(iv))
                }
            }
        }
        writeJSON(w, http.StatusOK, retryConfigResponse{
            MaxRetries: getInt(sec, "max_retries"), InitialBackoff: getString(sec, "initial_backoff"),
            MaxBackoff: getString(sec, "max_backoff"), RetryableStatusCodes: retryableCodes,
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Negotiation Config ───────────────────────────────────────

type negotiationConfigResponse struct {
    DisableFusionMLXRouting bool   `json:"disable_fusion_mlx_routing"`
    RouteHeader             string `json:"route_header"`
    RouteHeaderValue        string `json:"route_header_value"`
}

type negotiationConfigUpdate struct {
    DisableFusionMLXRouting *bool   `json:"disable_fusion_mlx_routing,omitempty"`
    RouteHeader             *string `json:"route_header,omitempty"`
    RouteHeaderValue        *string `json:"route_header_value,omitempty"`
}

func (h *Handler) handleNegotiationConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        n := config.GetSnapshot().Config.Routing.Negotiation
        writeJSON(w, http.StatusOK, negotiationConfigResponse{
            DisableFusionMLXRouting: n.DisableFusionMLXRouting,
            RouteHeader:             n.RouteHeader, RouteHeaderValue: n.RouteHeaderValue,
        })
    case http.MethodPut:
        var req negotiationConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "routing", "negotiation")
            applyBool(sec, "disable_fusion_mlx_routing", req.DisableFusionMLXRouting)
            applyString(sec, "route_header", req.RouteHeader)
            applyString(sec, "route_header_value", req.RouteHeaderValue)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("negotiation config updated via admin API")
        sec := getOrCreateSection(doc, "routing", "negotiation")
        writeJSON(w, http.StatusOK, negotiationConfigResponse{
            DisableFusionMLXRouting: getBool(sec, "disable_fusion_mlx_routing"),
            RouteHeader:             getString(sec, "route_header"), RouteHeaderValue: getString(sec, "route_header_value"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Cache Config ─────────────────────────────────────────────

type cacheConfigResponse struct {
    Enabled     bool               `json:"enabled"`
    MaxEntries  int                `json:"max_entries"`
    TTL         string             `json:"ttl"`
    MaxMemoryMB int                `json:"max_memory_mb"`
    Backend     string             `json:"backend"`
    Redis       cacheRedisResponse `json:"redis"`
    WarmupFile  string             `json:"warmup_file"`
}

type cacheRedisResponse struct {
    Addr     string `json:"addr"`
    Password string `json:"password"`
    DB       int    `json:"db"`
    PoolSize int    `json:"pool_size"`
}

type cacheConfigUpdate struct {
    Enabled     *bool             `json:"enabled,omitempty"`
    MaxEntries  *int              `json:"max_entries,omitempty"`
    TTL         *string           `json:"ttl,omitempty"`
    MaxMemoryMB *int              `json:"max_memory_mb,omitempty"`
    Backend     *string           `json:"backend,omitempty"`
    Redis       *cacheRedisUpdate `json:"redis,omitempty"`
    WarmupFile  *string           `json:"warmup_file,omitempty"`
}

type cacheRedisUpdate struct {
    Addr     *string `json:"addr,omitempty"`
    Password *string `json:"password,omitempty"`
    DB       *int    `json:"db,omitempty"`
    PoolSize *int    `json:"pool_size,omitempty"`
}

func (h *Handler) handleCacheConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        c := config.GetSnapshot().Config.Cache
        writeJSON(w, http.StatusOK, cacheConfigResponse{
            Enabled: c.Enabled, MaxEntries: c.MaxEntries, TTL: c.TTL.String(),
            MaxMemoryMB: c.MaxMemoryMB, Backend: c.Backend, WarmupFile: c.WarmupFile,
            Redis: cacheRedisResponse{
                Addr: c.Redis.Addr, Password: maskAPIKey(c.Redis.Password),
                DB: c.Redis.DB, PoolSize: c.Redis.PoolSize,
            },
        })
    case http.MethodPut:
        var req cacheConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "cache")
            applyBool(sec, "enabled", req.Enabled)
            applyInt(sec, "max_entries", req.MaxEntries)
            if req.TTL != nil {
                sec["ttl"] = *req.TTL
            }
            applyInt(sec, "max_memory_mb", req.MaxMemoryMB)
            applyString(sec, "backend", req.Backend)
            applyString(sec, "warmup_file", req.WarmupFile)
            if req.Redis != nil {
                rSec := getOrCreateSection(doc, "cache", "redis")
                applyString(rSec, "addr", req.Redis.Addr)
                applyMaskedString(rSec, "password", req.Redis.Password)
                applyInt(rSec, "db", req.Redis.DB)
                applyInt(rSec, "pool_size", req.Redis.PoolSize)
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("cache config updated via admin API")
        auditSensitiveChanges("cache", getOrCreateSection(doc, "cache"), adminUsername(r))
        auditSensitiveChanges("cache.redis", getOrCreateSection(doc, "cache", "redis"), adminUsername(r))
        sec := getOrCreateSection(doc, "cache")
        rSec := getOrCreateSection(doc, "cache", "redis")
        writeJSON(w, http.StatusOK, cacheConfigResponse{
            Enabled: getBool(sec, "enabled"), MaxEntries: getInt(sec, "max_entries"), TTL: getString(sec, "ttl"),
            MaxMemoryMB: getInt(sec, "max_memory_mb"), Backend: getString(sec, "backend"), WarmupFile: getString(sec, "warmup_file"),
            Redis: cacheRedisResponse{
                Addr: getString(rSec, "addr"), Password: maskAPIKey(getString(rSec, "password")),
                DB: getInt(rSec, "db"), PoolSize: getInt(rSec, "pool_size"),
            },
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Cost Config ──────────────────────────────────────────────

type costConfigResponse struct {
    Enabled              bool    `json:"enabled"`
    PricingFile          string  `json:"pricing_file"`
    BudgetAlertThreshold float64 `json:"budget_alert_threshold"`
}

type costConfigUpdate struct {
    Enabled              *bool    `json:"enabled,omitempty"`
    PricingFile          *string  `json:"pricing_file,omitempty"`
    BudgetAlertThreshold *float64 `json:"budget_alert_threshold,omitempty"`
}

func (h *Handler) handleCostConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        c := config.GetSnapshot().Config.Cost
        writeJSON(w, http.StatusOK, costConfigResponse{
            Enabled: c.Enabled, PricingFile: c.PricingFile,
            BudgetAlertThreshold: c.BudgetAlertThreshold,
        })
    case http.MethodPut:
        var req costConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "cost")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "pricing_file", req.PricingFile)
            if req.BudgetAlertThreshold != nil {
                sec["budget_alert_threshold"] = *req.BudgetAlertThreshold
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("cost config updated via admin API")
        sec := getOrCreateSection(doc, "cost")
        writeJSON(w, http.StatusOK, costConfigResponse{
            Enabled: getBool(sec, "enabled"), PricingFile: getString(sec, "pricing_file"),
            BudgetAlertThreshold: getFloat64(sec, "budget_alert_threshold"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── CostMarkup Config ────────────────────────────────────────

type costMarkupConfigResponse struct {
    Enabled      bool    `json:"enabled"`
    GlobalMarkup float64 `json:"global_markup"`
}

type costMarkupConfigUpdate struct {
    Enabled      *bool    `json:"enabled,omitempty"`
    GlobalMarkup *float64 `json:"global_markup,omitempty"`
}

func (h *Handler) handleCostMarkupConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        cm := config.GetSnapshot().Config.CostMarkup
        writeJSON(w, http.StatusOK, costMarkupConfigResponse{
            Enabled: cm.Enabled, GlobalMarkup: cm.GlobalMarkup,
        })
    case http.MethodPut:
        var req costMarkupConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "cost_markup")
            applyBool(sec, "enabled", req.Enabled)
            if req.GlobalMarkup != nil {
                sec["global_markup"] = *req.GlobalMarkup
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("cost_markup config updated via admin API")
        sec := getOrCreateSection(doc, "cost_markup")
        writeJSON(w, http.StatusOK, costMarkupConfigResponse{
            Enabled: getBool(sec, "enabled"), GlobalMarkup: getFloat64(sec, "global_markup"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── PII Config ───────────────────────────────────────────────

type piiPatternResponse struct {
    Name  string `json:"name"`
    Regex string `json:"regex"`
}

type piiConfigResponse struct {
    Enabled  bool                 `json:"enabled"`
    Action   string               `json:"action"`
    Patterns []piiPatternResponse `json:"patterns"`
}

type piiConfigUpdate struct {
    Enabled  *bool                  `json:"enabled,omitempty"`
    Action   *string                `json:"action,omitempty"`
    Patterns *[]piiPatternResponse  `json:"patterns,omitempty"`
}

func (h *Handler) handlePIIConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        p := config.GetSnapshot().Config.PII
        patterns := make([]piiPatternResponse, 0, len(p.Patterns))
        for _, pat := range p.Patterns {
            patterns = append(patterns, piiPatternResponse{Name: pat.Name, Regex: pat.Regex})
        }
        writeJSON(w, http.StatusOK, piiConfigResponse{
            Enabled: p.Enabled, Action: p.Action, Patterns: patterns,
        })
    case http.MethodPut:
        var req piiConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "pii")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "action", req.Action)
            if req.Patterns != nil {
                var yamlPats []interface{}
                for _, p := range *req.Patterns {
                    yamlPats = append(yamlPats, map[string]interface{}{
                        "name": p.Name, "regex": p.Regex,
                    })
                }
                sec["patterns"] = yamlPats
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("pii config updated via admin API")
        sec := getOrCreateSection(doc, "pii")
        piiPatterns := []piiPatternResponse{}
        if raw, ok := sec["patterns"].([]interface{}); ok {
            for _, v := range raw {
                if m, ok := v.(map[string]interface{}); ok {
                    piiPatterns = append(piiPatterns, piiPatternResponse{
                        Name: getString(m, "name"), Regex: getString(m, "regex"),
                    })
                }
            }
        }
        writeJSON(w, http.StatusOK, piiConfigResponse{
            Enabled: getBool(sec, "enabled"), Action: getString(sec, "action"), Patterns: piiPatterns,
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Cloud Routing Config ─────────────────────────────────────

type cloudRoutingConfigResponse struct {
    Strategy     string         `json:"strategy"`
    CloudWeights map[string]int `json:"cloud_weights"`
}

type cloudRoutingConfigUpdate struct {
    Strategy     *string        `json:"strategy,omitempty"`
    CloudWeights *map[string]int `json:"cloud_weights,omitempty"`
}

func (h *Handler) handleCloudRoutingConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        cr := config.GetSnapshot().Config.CloudRouting
        writeJSON(w, http.StatusOK, cloudRoutingConfigResponse{
            Strategy: cr.Strategy, CloudWeights: cr.CloudWeights,
        })
    case http.MethodPut:
        var req cloudRoutingConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "cloud_routing")
            applyString(sec, "strategy", req.Strategy)
            if req.CloudWeights != nil {
                weights := make(map[string]interface{})
                for k, v := range *req.CloudWeights {
                    weights[k] = v
                }
                sec["cloud_weights"] = weights
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("cloud_routing config updated via admin API")
        sec := getOrCreateSection(doc, "cloud_routing")
        cloudWeights := map[string]int{}
        if raw, ok := sec["cloud_weights"].(map[string]interface{}); ok {
            for k, v := range raw {
                switch iv := v.(type) {
                case int:
                    cloudWeights[k] = iv
                case float64:
                    cloudWeights[k] = int(iv)
                }
            }
        }
        writeJSON(w, http.StatusOK, cloudRoutingConfigResponse{
            Strategy: getString(sec, "strategy"), CloudWeights: cloudWeights,
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Hardware Config ──────────────────────────────────────────

type hardwareConfigResponse struct {
    Enabled                   bool   `json:"enabled"`
    CollectInterval           string `json:"collect_interval"`
    IOKitEnabled              bool   `json:"iokit_enabled"`
    GopsutilEnabled           bool   `json:"gopsutil_enabled"`
    MLXMetricsEnabled         bool   `json:"mlx_metrics_enabled"`
    MLXMetricsInterval        string `json:"mlx_metrics_interval"`
    SwapPageRateSampling      bool   `json:"swap_page_rate_sampling"`
    SwapPageRateThreshold     uint64 `json:"swap_page_rate_threshold"`
    CollectionErrorProtection bool   `json:"collection_error_protection"`
}

type hardwareConfigUpdate struct {
    Enabled                   *bool   `json:"enabled,omitempty"`
    CollectInterval           *string `json:"collect_interval,omitempty"`
    IOKitEnabled              *bool   `json:"iokit_enabled,omitempty"`
    GopsutilEnabled           *bool   `json:"gopsutil_enabled,omitempty"`
    MLXMetricsEnabled         *bool   `json:"mlx_metrics_enabled,omitempty"`
    MLXMetricsInterval        *string `json:"mlx_metrics_interval,omitempty"`
    SwapPageRateSampling      *bool   `json:"swap_page_rate_sampling,omitempty"`
    SwapPageRateThreshold     *uint64 `json:"swap_page_rate_threshold,omitempty"`
    CollectionErrorProtection *bool   `json:"collection_error_protection,omitempty"`
}

func (h *Handler) handleHardwareConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        hw := config.GetSnapshot().Config.Hardware
        writeJSON(w, http.StatusOK, hardwareConfigResponse{
            Enabled: hw.Enabled, CollectInterval: hw.CollectInterval.String(),
            IOKitEnabled: hw.IOKit.Enabled, GopsutilEnabled: hw.Gopsutil.Enabled,
            MLXMetricsEnabled: hw.MLXMetrics.Enabled, MLXMetricsInterval: hw.MLXMetrics.Interval.String(),
            SwapPageRateSampling: hw.Swap.PageRateSampling, SwapPageRateThreshold: hw.Swap.PageRateThreshold,
            CollectionErrorProtection: hw.CollectionErrorProtection,
        })
    case http.MethodPut:
        var req hardwareConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "hardware")
            applyBool(sec, "enabled", req.Enabled)
            if req.CollectInterval != nil {
                sec["collect_interval"] = *req.CollectInterval
            }
            applyBool(sec, "collection_error_protection", req.CollectionErrorProtection)
            if req.IOKitEnabled != nil {
                iokit := getOrCreateSection(doc, "hardware", "iokit")
                iokit["enabled"] = *req.IOKitEnabled
            }
            if req.GopsutilEnabled != nil {
                gops := getOrCreateSection(doc, "hardware", "gopsutil")
                gops["enabled"] = *req.GopsutilEnabled
            }
            if req.MLXMetricsEnabled != nil || req.MLXMetricsInterval != nil {
                mlx := getOrCreateSection(doc, "hardware", "mlx_metrics")
                applyBool(mlx, "enabled", req.MLXMetricsEnabled)
                if req.MLXMetricsInterval != nil {
                    mlx["interval"] = *req.MLXMetricsInterval
                }
            }
            if req.SwapPageRateSampling != nil || req.SwapPageRateThreshold != nil {
                swap := getOrCreateSection(doc, "hardware", "swap")
                applyBool(swap, "page_rate_sampling", req.SwapPageRateSampling)
                if req.SwapPageRateThreshold != nil {
                    swap["page_rate_threshold"] = *req.SwapPageRateThreshold
                }
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("hardware config updated via admin API")
        sec := getOrCreateSection(doc, "hardware")
        iokit := getOrCreateSection(doc, "hardware", "iokit")
        gops := getOrCreateSection(doc, "hardware", "gopsutil")
        mlx := getOrCreateSection(doc, "hardware", "mlx_metrics")
        swap := getOrCreateSection(doc, "hardware", "swap")
        writeJSON(w, http.StatusOK, hardwareConfigResponse{
            Enabled: getBool(sec, "enabled"), CollectInterval: getString(sec, "collect_interval"),
            IOKitEnabled: getBool(iokit, "enabled"), GopsutilEnabled: getBool(gops, "enabled"),
            MLXMetricsEnabled: getBool(mlx, "enabled"), MLXMetricsInterval: getString(mlx, "interval"),
            SwapPageRateSampling: getBool(swap, "page_rate_sampling"), SwapPageRateThreshold: uint64(getFloat64(swap, "page_rate_threshold")),
            CollectionErrorProtection: getBool(sec, "collection_error_protection"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Tokenizer Config ─────────────────────────────────────────

type tokenizerConfigResponse struct {
    Provider                      string  `json:"provider"`
    DefaultMaxTokensStrategy      string  `json:"default_max_tokens_strategy"`
    ContextWindowRatio            float64 `json:"context_window_ratio"`
    MinMaxTokens                  int     `json:"min_max_tokens"`
    VisionTokenEstimate           int     `json:"vision_token_estimate"`
    ScenePresetsChat              int     `json:"scene_presets_chat"`
    ScenePresetsCode              int     `json:"scene_presets_code"`
    ScenePresetsToolCall          int     `json:"scene_presets_tool_call"`
    CalibrationEnabled            bool    `json:"calibration_enabled"`
    CalibrationSampleInterval     int     `json:"calibration_sample_interval"`
    CalibrationSampleSize         int     `json:"calibration_sample_size"`
    CalibrationDeviationThreshold float64 `json:"calibration_deviation_threshold"`
    CalibrationAutoSwitchThreshold float64 `json:"calibration_auto_switch_threshold"`
}

type tokenizerConfigUpdate struct {
    Provider                      *string  `json:"provider,omitempty"`
    DefaultMaxTokensStrategy      *string  `json:"default_max_tokens_strategy,omitempty"`
    ContextWindowRatio            *float64 `json:"context_window_ratio,omitempty"`
    MinMaxTokens                  *int     `json:"min_max_tokens,omitempty"`
    VisionTokenEstimate           *int     `json:"vision_token_estimate,omitempty"`
    ScenePresetsChat              *int     `json:"scene_presets_chat,omitempty"`
    ScenePresetsCode              *int     `json:"scene_presets_code,omitempty"`
    ScenePresetsToolCall          *int     `json:"scene_presets_tool_call,omitempty"`
    CalibrationEnabled            *bool    `json:"calibration_enabled,omitempty"`
    CalibrationSampleInterval     *int     `json:"calibration_sample_interval,omitempty"`
    CalibrationSampleSize         *int     `json:"calibration_sample_size,omitempty"`
    CalibrationDeviationThreshold *float64 `json:"calibration_deviation_threshold,omitempty"`
    CalibrationAutoSwitchThreshold *float64 `json:"calibration_auto_switch_threshold,omitempty"`
}

func (h *Handler) handleTokenizerConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        t := config.GetSnapshot().Config.Tokenizer
        writeJSON(w, http.StatusOK, tokenizerConfigResponse{
            Provider: t.Provider, DefaultMaxTokensStrategy: t.DefaultMaxTokensStrategy,
            ContextWindowRatio: t.ContextWindowRatio, MinMaxTokens: t.MinMaxTokens,
            VisionTokenEstimate: t.VisionTokenEstimate,
            ScenePresetsChat: t.ScenePresets.Chat, ScenePresetsCode: t.ScenePresets.Code,
            ScenePresetsToolCall: t.ScenePresets.ToolCall,
            CalibrationEnabled: t.Calibration.Enabled, CalibrationSampleInterval: t.Calibration.SampleInterval,
            CalibrationSampleSize: t.Calibration.SampleSize,
            CalibrationDeviationThreshold: t.Calibration.DeviationThreshold,
            CalibrationAutoSwitchThreshold: t.Calibration.AutoSwitchThreshold,
        })
    case http.MethodPut:
        var req tokenizerConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "tokenizer")
            applyString(sec, "provider", req.Provider)
            applyString(sec, "default_max_tokens_strategy", req.DefaultMaxTokensStrategy)
            if req.ContextWindowRatio != nil {
                sec["context_window_ratio"] = *req.ContextWindowRatio
            }
            applyInt(sec, "min_max_tokens", req.MinMaxTokens)
            applyInt(sec, "vision_token_estimate", req.VisionTokenEstimate)
            if req.ScenePresetsChat != nil || req.ScenePresetsCode != nil || req.ScenePresetsToolCall != nil {
                sp := getOrCreateSection(doc, "tokenizer", "scene_presets")
                applyInt(sp, "chat", req.ScenePresetsChat)
                applyInt(sp, "code", req.ScenePresetsCode)
                applyInt(sp, "tool_call", req.ScenePresetsToolCall)
            }
            if req.CalibrationEnabled != nil || req.CalibrationSampleInterval != nil ||
                req.CalibrationSampleSize != nil || req.CalibrationDeviationThreshold != nil ||
                req.CalibrationAutoSwitchThreshold != nil {
                cal := getOrCreateSection(doc, "tokenizer", "calibration")
                applyBool(cal, "enabled", req.CalibrationEnabled)
                applyInt(cal, "sample_interval", req.CalibrationSampleInterval)
                applyInt(cal, "sample_size", req.CalibrationSampleSize)
                if req.CalibrationDeviationThreshold != nil {
                    cal["deviation_threshold"] = *req.CalibrationDeviationThreshold
                }
                if req.CalibrationAutoSwitchThreshold != nil {
                    cal["auto_switch_threshold"] = *req.CalibrationAutoSwitchThreshold
                }
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("tokenizer config updated via admin API")
        sec := getOrCreateSection(doc, "tokenizer")
        sp := getOrCreateSection(doc, "tokenizer", "scene_presets")
        cal := getOrCreateSection(doc, "tokenizer", "calibration")
        writeJSON(w, http.StatusOK, tokenizerConfigResponse{
            Provider: getString(sec, "provider"), DefaultMaxTokensStrategy: getString(sec, "default_max_tokens_strategy"),
            ContextWindowRatio: getFloat64(sec, "context_window_ratio"), MinMaxTokens: getInt(sec, "min_max_tokens"),
            VisionTokenEstimate: getInt(sec, "vision_token_estimate"),
            ScenePresetsChat: getInt(sp, "chat"), ScenePresetsCode: getInt(sp, "code"),
            ScenePresetsToolCall: getInt(sp, "tool_call"),
            CalibrationEnabled: getBool(cal, "enabled"), CalibrationSampleInterval: getInt(cal, "sample_interval"),
            CalibrationSampleSize: getInt(cal, "sample_size"),
            CalibrationDeviationThreshold: getFloat64(cal, "deviation_threshold"),
            CalibrationAutoSwitchThreshold: getFloat64(cal, "auto_switch_threshold"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Observability Config ─────────────────────────────────────

type observabilityConfigResponse struct {
    LogFormat             string `json:"log_format"`
    LogFile               string `json:"log_file"`
    LogRotationMaxSize    int    `json:"log_rotation_max_size"`
    LogRotationMaxBackups int    `json:"log_rotation_max_backups"`
    MetricsEnabled        bool   `json:"metrics_enabled"`
    MetricsPath           string `json:"metrics_path"`
    AuditLogEnabled       bool   `json:"audit_log_enabled"`
    ConfigAuditLog        bool   `json:"config_audit_log"`
    ConfigAuditFile       string `json:"config_audit_file"`
    OtelEnabled           bool   `json:"otel_enabled"`
    OtelEndpoint          string `json:"otel_endpoint"`
    OtelProtocol          string `json:"otel_protocol"`
    OtelServiceName       string `json:"otel_service_name"`
}

type observabilityConfigUpdate struct {
    LogFormat             *string `json:"log_format,omitempty"`
    LogFile               *string `json:"log_file,omitempty"`
    LogRotationMaxSize    *int    `json:"log_rotation_max_size,omitempty"`
    LogRotationMaxBackups *int    `json:"log_rotation_max_backups,omitempty"`
    MetricsEnabled        *bool   `json:"metrics_enabled,omitempty"`
    MetricsPath           *string `json:"metrics_path,omitempty"`
    AuditLogEnabled       *bool   `json:"audit_log_enabled,omitempty"`
    ConfigAuditLog        *bool   `json:"config_audit_log,omitempty"`
    ConfigAuditFile       *string `json:"config_audit_file,omitempty"`
    OtelEnabled           *bool   `json:"otel_enabled,omitempty"`
    OtelEndpoint          *string `json:"otel_endpoint,omitempty"`
    OtelProtocol          *string `json:"otel_protocol,omitempty"`
    OtelServiceName       *string `json:"otel_service_name,omitempty"`
}

func (h *Handler) handleObservabilityConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        o := config.GetSnapshot().Config.Observability
        writeJSON(w, http.StatusOK, observabilityConfigResponse{
            LogFormat: o.LogFormat, LogFile: o.LogFile,
            LogRotationMaxSize: o.LogRotationMaxSize, LogRotationMaxBackups: o.LogRotationMaxBackups,
            MetricsEnabled: o.MetricsEnabled, MetricsPath: o.MetricsPath,
            AuditLogEnabled: o.AuditLogEnabled, ConfigAuditLog: o.ConfigAuditLog,
            ConfigAuditFile: o.ConfigAuditFile,
            OtelEnabled: o.OtelEnabled, OtelEndpoint: o.OtelEndpoint,
            OtelProtocol: o.OtelProtocol, OtelServiceName: o.OtelServiceName,
        })
    case http.MethodPut:
        var req observabilityConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "observability")
            applyString(sec, "log_format", req.LogFormat)
            applyString(sec, "log_file", req.LogFile)
            applyInt(sec, "log_rotation_max_size", req.LogRotationMaxSize)
            applyInt(sec, "log_rotation_max_backups", req.LogRotationMaxBackups)
            applyBool(sec, "metrics_enabled", req.MetricsEnabled)
            applyString(sec, "metrics_path", req.MetricsPath)
            applyBool(sec, "audit_log_enabled", req.AuditLogEnabled)
            applyBool(sec, "config_audit_log", req.ConfigAuditLog)
            applyString(sec, "config_audit_file", req.ConfigAuditFile)
            applyBool(sec, "otel_enabled", req.OtelEnabled)
            applyString(sec, "otel_endpoint", req.OtelEndpoint)
            applyString(sec, "otel_protocol", req.OtelProtocol)
            applyString(sec, "otel_service_name", req.OtelServiceName)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("observability config updated via admin API")
        sec := getOrCreateSection(doc, "observability")
        writeJSON(w, http.StatusOK, observabilityConfigResponse{
            LogFormat: getString(sec, "log_format"), LogFile: getString(sec, "log_file"),
            LogRotationMaxSize: getInt(sec, "log_rotation_max_size"), LogRotationMaxBackups: getInt(sec, "log_rotation_max_backups"),
            MetricsEnabled: getBool(sec, "metrics_enabled"), MetricsPath: getString(sec, "metrics_path"),
            AuditLogEnabled: getBool(sec, "audit_log_enabled"), ConfigAuditLog: getBool(sec, "config_audit_log"),
            ConfigAuditFile: getString(sec, "config_audit_file"),
            OtelEnabled: getBool(sec, "otel_enabled"), OtelEndpoint: getString(sec, "otel_endpoint"),
            OtelProtocol: getString(sec, "otel_protocol"), OtelServiceName: getString(sec, "otel_service_name"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── CORS Config ──────────────────────────────────────────────

type corsConfigResponse struct {
    AllowedOrigins []string `json:"allowed_origins"`
    AllowedMethods []string `json:"allowed_methods"`
    AllowedHeaders []string `json:"allowed_headers"`
}

type corsConfigUpdate struct {
    AllowedOrigins *[]string `json:"allowed_origins,omitempty"`
    AllowedMethods *[]string `json:"allowed_methods,omitempty"`
    AllowedHeaders *[]string `json:"allowed_headers,omitempty"`
}

func (h *Handler) handleCORSConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        c := config.GetSnapshot().Config.CORS
        writeJSON(w, http.StatusOK, corsConfigResponse{
            AllowedOrigins: c.AllowedOrigins, AllowedMethods: c.AllowedMethods,
            AllowedHeaders: c.AllowedHeaders,
        })
    case http.MethodPut:
        var req corsConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "cors")
            if req.AllowedOrigins != nil {
                sec["allowed_origins"] = stringSliceToInterface(*req.AllowedOrigins)
            }
            if req.AllowedMethods != nil {
                sec["allowed_methods"] = stringSliceToInterface(*req.AllowedMethods)
            }
            if req.AllowedHeaders != nil {
                sec["allowed_headers"] = stringSliceToInterface(*req.AllowedHeaders)
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("cors config updated via admin API")
        sec := getOrCreateSection(doc, "cors")
        writeJSON(w, http.StatusOK, corsConfigResponse{
            AllowedOrigins: interfaceToStringSlice(sec["allowed_origins"]),
            AllowedMethods: interfaceToStringSlice(sec["allowed_methods"]),
            AllowedHeaders: interfaceToStringSlice(sec["allowed_headers"]),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Hot Reload Config ────────────────────────────────────────

type hotReloadConfigResponse struct {
    Enabled              bool   `json:"enabled"`
    WatchPath            string `json:"watch_path"`
    Debounce             string `json:"debounce"`
    Versioning           bool   `json:"versioning"`
    BreakerDrainTimeout  string `json:"breaker_drain_timeout"`
    BreakerWarmupSuccess int    `json:"breaker_warmup_success"`
}

type hotReloadConfigUpdate struct {
    Enabled              *bool   `json:"enabled,omitempty"`
    WatchPath            *string `json:"watch_path,omitempty"`
    Debounce             *string `json:"debounce,omitempty"`
    Versioning           *bool   `json:"versioning,omitempty"`
    BreakerDrainTimeout  *string `json:"breaker_drain_timeout,omitempty"`
    BreakerWarmupSuccess *int    `json:"breaker_warmup_success,omitempty"`
}

func (h *Handler) handleHotReloadConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        hr := config.GetSnapshot().Config.HotReload
        writeJSON(w, http.StatusOK, hotReloadConfigResponse{
            Enabled: hr.Enabled, WatchPath: hr.WatchPath, Debounce: hr.Debounce.String(),
            Versioning: hr.Versioning, BreakerDrainTimeout: hr.BreakerDrainTimeout.String(),
            BreakerWarmupSuccess: hr.BreakerWarmupSuccess,
        })
    case http.MethodPut:
        var req hotReloadConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "hot_reload")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "watch_path", req.WatchPath)
            if req.Debounce != nil {
                sec["debounce"] = *req.Debounce
            }
            applyBool(sec, "versioning", req.Versioning)
            if req.BreakerDrainTimeout != nil {
                sec["breaker_drain_timeout"] = *req.BreakerDrainTimeout
            }
            applyInt(sec, "breaker_warmup_success", req.BreakerWarmupSuccess)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("hot_reload config updated via admin API")
        sec := getOrCreateSection(doc, "hot_reload")
        writeJSON(w, http.StatusOK, hotReloadConfigResponse{
            Enabled: getBool(sec, "enabled"), WatchPath: getString(sec, "watch_path"), Debounce: getString(sec, "debounce"),
            Versioning: getBool(sec, "versioning"), BreakerDrainTimeout: getString(sec, "breaker_drain_timeout"),
            BreakerWarmupSuccess: getInt(sec, "breaker_warmup_success"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Cluster Config ───────────────────────────────────────────

type clusterNodeResponse struct {
    ID       string `json:"id"`
    Address  string `json:"address"`
    GPU      string `json:"gpu"`
    MemoryGB int    `json:"memory_gb"`
}

type clusterConfigResponse struct {
    Enabled             bool                  `json:"enabled"`
    Mode                string                `json:"mode"`
    Nodes               []clusterNodeResponse `json:"nodes"`
    MasterAddress       string                `json:"master_address"`
    MasterSharedToken   string                `json:"master_shared_token"`
    LoadBalancer        string                `json:"load_balancer"`
    HealthCheckInterval string                `json:"health_check_interval"`
    FailureThreshold    int                   `json:"failure_threshold"`
    RecoveryInterval    string                `json:"recovery_interval"`
}

type clusterConfigUpdate struct {
    Enabled             *bool                  `json:"enabled,omitempty"`
    Mode                *string                `json:"mode,omitempty"`
    Nodes               *[]clusterNodeResponse `json:"nodes,omitempty"`
    MasterAddress       *string                `json:"master_address,omitempty"`
    MasterSharedToken   *string                `json:"master_shared_token,omitempty"`
    LoadBalancer        *string                `json:"load_balancer,omitempty"`
    HealthCheckInterval *string                `json:"health_check_interval,omitempty"`
    FailureThreshold    *int                   `json:"failure_threshold,omitempty"`
    RecoveryInterval    *string                `json:"recovery_interval,omitempty"`
}

func (h *Handler) handleClusterConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        cl := config.GetSnapshot().Config.Cluster
        nodes := make([]clusterNodeResponse, 0, len(cl.Nodes))
        for _, n := range cl.Nodes {
            nodes = append(nodes, clusterNodeResponse{
                ID: n.ID, Address: n.Address, GPU: n.GPU, MemoryGB: n.MemoryGB,
            })
        }
        writeJSON(w, http.StatusOK, clusterConfigResponse{
            Enabled: cl.Enabled, Mode: string(cl.Mode), Nodes: nodes,
            MasterAddress: cl.Master.Address, MasterSharedToken: maskAPIKey(cl.Master.SharedToken),
            LoadBalancer: cl.LoadBalancer, HealthCheckInterval: cl.HealthCheckInterval.String(),
            FailureThreshold: cl.FailureThreshold, RecoveryInterval: cl.RecoveryInterval.String(),
        })
    case http.MethodPut:
        var req clusterConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "cluster")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "mode", req.Mode)
            applyString(sec, "load_balancer", req.LoadBalancer)
            if req.HealthCheckInterval != nil {
                sec["health_check_interval"] = *req.HealthCheckInterval
            }
            applyInt(sec, "failure_threshold", req.FailureThreshold)
            if req.RecoveryInterval != nil {
                sec["recovery_interval"] = *req.RecoveryInterval
            }
            if req.Nodes != nil {
                var yamlNodes []interface{}
                for _, n := range *req.Nodes {
                    yamlNodes = append(yamlNodes, map[string]interface{}{
                        "id": n.ID, "address": n.Address, "gpu": n.GPU, "memory_gb": n.MemoryGB,
                    })
                }
                sec["nodes"] = yamlNodes
            }
            if req.MasterAddress != nil || req.MasterSharedToken != nil {
                master := getOrCreateSection(doc, "cluster", "master")
                applyString(master, "address", req.MasterAddress)
                applyMaskedString(master, "shared_token", req.MasterSharedToken)
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("cluster config updated via admin API")
        auditSensitiveChanges("cluster", getOrCreateSection(doc, "cluster"), adminUsername(r))
        auditSensitiveChanges("cluster.master", getOrCreateSection(doc, "cluster", "master"), adminUsername(r))
        sec := getOrCreateSection(doc, "cluster")
        master := getOrCreateSection(doc, "cluster", "master")
        clNodes := []clusterNodeResponse{}
        if raw, ok := sec["nodes"].([]interface{}); ok {
            for _, v := range raw {
                if m, ok := v.(map[string]interface{}); ok {
                    clNodes = append(clNodes, clusterNodeResponse{
                        ID: getString(m, "id"), Address: getString(m, "address"),
                        GPU: getString(m, "gpu"), MemoryGB: getInt(m, "memory_gb"),
                    })
                }
            }
        }
        writeJSON(w, http.StatusOK, clusterConfigResponse{
            Enabled: getBool(sec, "enabled"), Mode: getString(sec, "mode"), Nodes: clNodes,
            MasterAddress: getString(master, "address"), MasterSharedToken: maskAPIKey(getString(master, "shared_token")),
            LoadBalancer: getString(sec, "load_balancer"), HealthCheckInterval: getString(sec, "health_check_interval"),
            FailureThreshold: getInt(sec, "failure_threshold"), RecoveryInterval: getString(sec, "recovery_interval"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Realtime Config ──────────────────────────────────────────

type realtimeConfigResponse struct {
    Enabled      bool   `json:"enabled"`
    BackendURL   string `json:"backend_url"`
    APIKey       string `json:"api_key"`
    MaxMessageMB int    `json:"max_message_mb"`
}

type realtimeConfigUpdate struct {
    Enabled      *bool   `json:"enabled,omitempty"`
    BackendURL   *string `json:"backend_url,omitempty"`
    APIKey       *string `json:"api_key,omitempty"`
    MaxMessageMB *int    `json:"max_message_mb,omitempty"`
}

func (h *Handler) handleRealtimeConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        rt := config.GetSnapshot().Config.Realtime
        writeJSON(w, http.StatusOK, realtimeConfigResponse{
            Enabled: rt.Enabled, BackendURL: rt.BackendURL,
            APIKey: maskAPIKey(rt.APIKey), MaxMessageMB: rt.MaxMessageMB,
        })
    case http.MethodPut:
        var req realtimeConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "realtime")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "backend_url", req.BackendURL)
            applyMaskedString(sec, "api_key", req.APIKey)
            applyInt(sec, "max_message_mb", req.MaxMessageMB)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("realtime config updated via admin API")
        auditSensitiveChanges("realtime", getOrCreateSection(doc, "realtime"), adminUsername(r))
        sec := getOrCreateSection(doc, "realtime")
        writeJSON(w, http.StatusOK, realtimeConfigResponse{
            Enabled: getBool(sec, "enabled"), BackendURL: getString(sec, "backend_url"),
            APIKey: maskAPIKey(getString(sec, "api_key")), MaxMessageMB: getInt(sec, "max_message_mb"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Admin Config ─────────────────────────────────────────────

type adminConfigResponse struct {
    Enabled   bool              `json:"enabled"`
    Listen    string            `json:"listen"`
    LogMaxLen int               `json:"log_max_len"`
    JWTSecret string            `json:"jwt_secret"`
    Users     map[string]string `json:"users"`
}

type adminConfigUpdate struct {
    Enabled   *bool              `json:"enabled,omitempty"`
    Listen    *string            `json:"listen,omitempty"`
    LogMaxLen *int               `json:"log_max_len,omitempty"`
    JWTSecret *string            `json:"jwt_secret,omitempty"`
    Users     *map[string]string `json:"users,omitempty"`
}

func (h *Handler) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        a := config.GetSnapshot().Config.Admin
        if a == nil {
            writeJSON(w, http.StatusOK, adminConfigResponse{})
            return
        }
        maskedUsers := make(map[string]string)
        for k := range a.Users {
            maskedUsers[k] = "********"
        }
        writeJSON(w, http.StatusOK, adminConfigResponse{
            Enabled: a.Enabled, Listen: a.Listen, LogMaxLen: a.LogMaxLen,
            JWTSecret: maskAPIKey(a.JWTSecret), Users: maskedUsers,
        })
    case http.MethodPut:
        var req adminConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "admin")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "listen", req.Listen)
            applyInt(sec, "log_max_len", req.LogMaxLen)
            applyMaskedString(sec, "jwt_secret", req.JWTSecret)
            if req.Users != nil {
                // F9: the prior check `v != "" && len(v) < 8` let an empty
                // password through and wrote it — a passwordless admin login.
                // Empty/masked now means "keep existing" (skip that user);
                // any real password must be >= 8 chars. This mirrors
                // applyMaskedString's no-change semantics for secrets.
                for k, v := range *req.Users {
                    if v == "" || isMaskedValue(v) {
                        continue
                    }
                    if len(v) < 8 {
                        return fmt.Errorf("password for user %q must be at least 8 characters", k)
                    }
                }
                users := make(map[string]interface{})
                for k, v := range *req.Users {
                    if v == "" || isMaskedValue(v) {
                        continue
                    }
                    // H8: hash the password BEFORE persisting so config.yaml
                    // never stores plaintext. Previously the raw password was
                    // written and only bcrypt'd at next startup, leaving it
                    // plaintext on disk (and in any crash/replay) until restart.
                    hashed, err := HashAdminPasswordIfPlaintext(v)
                    if err != nil {
                        return fmt.Errorf("hash password for user %q: %w", k, err)
                    }
                    users[k] = hashed
                }
                sec["users"] = users
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("admin config updated via admin API")
        auditSensitiveChanges("admin", getOrCreateSection(doc, "admin"), adminUsername(r))
        sec := getOrCreateSection(doc, "admin")
        maskedUsers := make(map[string]string)
        if raw, ok := sec["users"].(map[string]interface{}); ok {
            for k := range raw {
                maskedUsers[k] = "********"
            }
            // H8: apply the rotated admin credentials to the live AdminAuth so
            // the new password/jwt_secret take effect immediately — not only at
            // the next process restart. Without this, a security rotation via
            // the admin panel is silently ineffective until restart.
            reloadedUsers := make(map[string]string, len(raw))
            for k, v := range raw {
                if hv, ok := v.(string); ok {
                    reloadedUsers[k] = hv
                }
            }
            if h.auth != nil && len(reloadedUsers) > 0 {
                h.auth.ReloadUsers(reloadedUsers)
            }
        }
        if h.auth != nil {
            if newSecret := getString(sec, "jwt_secret"); newSecret != "" && !isMaskedValue(newSecret) {
                h.auth.ReloadSecret(newSecret)
            }
        }
        writeJSON(w, http.StatusOK, adminConfigResponse{
            Enabled: getBool(sec, "enabled"), Listen: getString(sec, "listen"), LogMaxLen: getInt(sec, "log_max_len"),
            JWTSecret: maskAPIKey(getString(sec, "jwt_secret")), Users: maskedUsers,
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── OIDC Config ──────────────────────────────────────────────

type oidcConfigResponse struct {
    Enabled       bool   `json:"enabled"`
    Issuer        string `json:"issuer"`
    ClientID      string `json:"client_id"`
    Audiences     string `json:"audiences"`
    Scopes        string `json:"scopes"`
    ClaimMappings string `json:"claim_mappings"`
}

type oidcConfigUpdate struct {
    Enabled       *bool   `json:"enabled,omitempty"`
    Issuer        *string `json:"issuer,omitempty"`
    ClientID      *string `json:"client_id,omitempty"`
    Audiences     *string `json:"audiences,omitempty"`
    Scopes        *string `json:"scopes,omitempty"`
    ClaimMappings *string `json:"claim_mappings,omitempty"`
}

func (h *Handler) handleOIDCConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        o := config.GetSnapshot().Config.OIDC
        writeJSON(w, http.StatusOK, oidcConfigResponse{
            Enabled: o.Enabled, Issuer: o.Issuer, ClientID: o.ClientID,
            Audiences: o.Audiences, Scopes: o.Scopes, ClaimMappings: o.ClaimMappings,
        })
    case http.MethodPut:
        var req oidcConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "oidc")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "issuer", req.Issuer)
            applyString(sec, "client_id", req.ClientID)
            applyString(sec, "audiences", req.Audiences)
            applyString(sec, "scopes", req.Scopes)
            applyString(sec, "claim_mappings", req.ClaimMappings)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("oidc config updated via admin API")
        sec := getOrCreateSection(doc, "oidc")
        writeJSON(w, http.StatusOK, oidcConfigResponse{
            Enabled: getBool(sec, "enabled"), Issuer: getString(sec, "issuer"), ClientID: getString(sec, "client_id"),
            Audiences: getString(sec, "audiences"), Scopes: getString(sec, "scopes"), ClaimMappings: getString(sec, "claim_mappings"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── RBAC Config ──────────────────────────────────────────────

type rbacConfigResponse struct {
    Enabled     bool   `json:"enabled"`
    DefaultRole string `json:"default_role"`
    TeamEnabled bool   `json:"team_enabled"`
    DefaultTeam string `json:"default_team"`
}

type rbacConfigUpdate struct {
    Enabled     *bool   `json:"enabled,omitempty"`
    DefaultRole *string `json:"default_role,omitempty"`
    TeamEnabled *bool   `json:"team_enabled,omitempty"`
    DefaultTeam *string `json:"default_team,omitempty"`
}

func (h *Handler) handleRBACConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        rb := config.GetSnapshot().Config.RBAC
        tm := config.GetSnapshot().Config.Team
        writeJSON(w, http.StatusOK, rbacConfigResponse{
            Enabled: rb.Enabled, DefaultRole: rb.DefaultRole,
            TeamEnabled: tm.Enabled, DefaultTeam: tm.DefaultTeam,
        })
    case http.MethodPut:
        var req rbacConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            if req.Enabled != nil || req.DefaultRole != nil {
                sec := getOrCreateSection(doc, "rbac")
                applyBool(sec, "enabled", req.Enabled)
                applyString(sec, "default_role", req.DefaultRole)
            }
            if req.TeamEnabled != nil || req.DefaultTeam != nil {
                sec := getOrCreateSection(doc, "team")
                applyBool(sec, "enabled", req.TeamEnabled)
                applyString(sec, "default_team", req.DefaultTeam)
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("rbac/team config updated via admin API")
        rbacSec := getOrCreateSection(doc, "rbac")
        teamSec := getOrCreateSection(doc, "team")
        writeJSON(w, http.StatusOK, rbacConfigResponse{
            Enabled: getBool(rbacSec, "enabled"), DefaultRole: getString(rbacSec, "default_role"),
            TeamEnabled: getBool(teamSec, "enabled"), DefaultTeam: getString(teamSec, "default_team"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Semantic Cache Config ────────────────────────────────────

type semanticCacheConfigResponse struct {
    Enabled             bool    `json:"enabled"`
    SimilarityThreshold float64 `json:"similarity_threshold"`
    MaxEntries          int     `json:"max_entries"`
    Provider            string  `json:"provider"`
    Endpoint            string  `json:"endpoint"`
}

type semanticCacheConfigUpdate struct {
    Enabled             *bool    `json:"enabled,omitempty"`
    SimilarityThreshold *float64 `json:"similarity_threshold,omitempty"`
    MaxEntries          *int     `json:"max_entries,omitempty"`
    Provider            *string  `json:"provider,omitempty"`
    Endpoint            *string  `json:"endpoint,omitempty"`
}

func (h *Handler) handleSemanticCacheConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        sc := config.GetSnapshot().Config.SemanticCache
        writeJSON(w, http.StatusOK, semanticCacheConfigResponse{
            Enabled: sc.Enabled, SimilarityThreshold: sc.SimilarityThreshold,
            MaxEntries: sc.MaxEntries, Provider: sc.Provider, Endpoint: sc.Endpoint,
        })
    case http.MethodPut:
        var req semanticCacheConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "semantic_cache")
            applyBool(sec, "enabled", req.Enabled)
            if req.SimilarityThreshold != nil {
                sec["similarity_threshold"] = *req.SimilarityThreshold
            }
            applyInt(sec, "max_entries", req.MaxEntries)
            applyString(sec, "provider", req.Provider)
            applyString(sec, "endpoint", req.Endpoint)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("semantic_cache config updated via admin API")
        sec := getOrCreateSection(doc, "semantic_cache")
        writeJSON(w, http.StatusOK, semanticCacheConfigResponse{
            Enabled: getBool(sec, "enabled"), SimilarityThreshold: getFloat64(sec, "similarity_threshold"),
            MaxEntries: getInt(sec, "max_entries"), Provider: getString(sec, "provider"), Endpoint: getString(sec, "endpoint"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Prompt Injection Config ──────────────────────────────────

type promptInjectionConfigResponse struct {
    Enabled   bool    `json:"enabled"`
    Action    string  `json:"action"`
    Provider  string  `json:"provider"`
    APIKey    string  `json:"api_key"`
    Threshold float64 `json:"threshold"`
}

type promptInjectionConfigUpdate struct {
    Enabled   *bool    `json:"enabled,omitempty"`
    Action    *string  `json:"action,omitempty"`
    Provider  *string  `json:"provider,omitempty"`
    APIKey    *string  `json:"api_key,omitempty"`
    Threshold *float64 `json:"threshold,omitempty"`
}

func (h *Handler) handlePromptInjectionConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        pi := config.GetSnapshot().Config.PromptInjection
        writeJSON(w, http.StatusOK, promptInjectionConfigResponse{
            Enabled: pi.Enabled, Action: pi.Action, Provider: pi.Provider,
            APIKey: maskAPIKey(pi.APIKey), Threshold: pi.Threshold,
        })
    case http.MethodPut:
        var req promptInjectionConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "prompt_injection")
            applyBool(sec, "enabled", req.Enabled)
            applyString(sec, "action", req.Action)
            applyString(sec, "provider", req.Provider)
            applyMaskedString(sec, "api_key", req.APIKey)
            if req.Threshold != nil {
                sec["threshold"] = *req.Threshold
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("prompt_injection config updated via admin API")
        auditSensitiveChanges("prompt_injection", getOrCreateSection(doc, "prompt_injection"), adminUsername(r))
        sec := getOrCreateSection(doc, "prompt_injection")
        writeJSON(w, http.StatusOK, promptInjectionConfigResponse{
            Enabled: getBool(sec, "enabled"), Action: getString(sec, "action"), Provider: getString(sec, "provider"),
            APIKey: maskAPIKey(getString(sec, "api_key")), Threshold: getFloat64(sec, "threshold"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Batch Config ─────────────────────────────────────────────

type batchConfigResponse struct {
    Enabled      bool   `json:"enabled"`
    MaxBatchSize int    `json:"max_batch_size"`
    PollInterval string `json:"poll_interval"`
    Timeout      string `json:"timeout"`
}

type batchConfigUpdate struct {
    Enabled      *bool   `json:"enabled,omitempty"`
    MaxBatchSize *int    `json:"max_batch_size,omitempty"`
    PollInterval *string `json:"poll_interval,omitempty"`
    Timeout      *string `json:"timeout,omitempty"`
}

func (h *Handler) handleBatchConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        b := config.GetSnapshot().Config.Batch
        writeJSON(w, http.StatusOK, batchConfigResponse{
            Enabled: b.Enabled, MaxBatchSize: b.MaxBatchSize,
            PollInterval: b.PollInterval.String(), Timeout: b.Timeout.String(),
        })
    case http.MethodPut:
        var req batchConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "batch")
            applyBool(sec, "enabled", req.Enabled)
            applyInt(sec, "max_batch_size", req.MaxBatchSize)
            if req.PollInterval != nil {
                sec["poll_interval"] = *req.PollInterval
            }
            if req.Timeout != nil {
                sec["timeout"] = *req.Timeout
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("batch config updated via admin API")
        sec := getOrCreateSection(doc, "batch")
        writeJSON(w, http.StatusOK, batchConfigResponse{
            Enabled: getBool(sec, "enabled"), MaxBatchSize: getInt(sec, "max_batch_size"),
            PollInterval: getString(sec, "poll_interval"), Timeout: getString(sec, "timeout"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Store Config ─────────────────────────────────────────────

type storeConfigResponse struct {
    Backend       string `json:"backend"`
    RedisAddr     string `json:"redis_addr"`
    RedisPassword string `json:"redis_password"`
    RedisDB       int    `json:"redis_db"`
    RedisPoolSize int    `json:"redis_pool_size"`
}

type storeConfigUpdate struct {
    Backend       *string `json:"backend,omitempty"`
    RedisAddr     *string `json:"redis_addr,omitempty"`
    RedisPassword *string `json:"redis_password,omitempty"`
    RedisDB       *int    `json:"redis_db,omitempty"`
    RedisPoolSize *int    `json:"redis_pool_size,omitempty"`
}

func (h *Handler) handleStoreConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        s := config.GetSnapshot().Config.Store
        writeJSON(w, http.StatusOK, storeConfigResponse{
            Backend: s.Backend, RedisAddr: s.Redis.Addr,
            RedisPassword: maskAPIKey(s.Redis.Password),
            RedisDB: s.Redis.DB, RedisPoolSize: s.Redis.PoolSize,
        })
    case http.MethodPut:
        var req storeConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "store")
            applyString(sec, "backend", req.Backend)
            if req.RedisAddr != nil || req.RedisPassword != nil ||
                req.RedisDB != nil || req.RedisPoolSize != nil {
                rSec := getOrCreateSection(doc, "store", "redis")
                applyString(rSec, "addr", req.RedisAddr)
                applyMaskedString(rSec, "password", req.RedisPassword)
                applyInt(rSec, "db", req.RedisDB)
                applyInt(rSec, "pool_size", req.RedisPoolSize)
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("store config updated via admin API")
        auditSensitiveChanges("store", getOrCreateSection(doc, "store"), adminUsername(r))
        auditSensitiveChanges("store.redis", getOrCreateSection(doc, "store", "redis"), adminUsername(r))
        sec := getOrCreateSection(doc, "store")
        rSec := getOrCreateSection(doc, "store", "redis")
        writeJSON(w, http.StatusOK, storeConfigResponse{
            Backend: getString(sec, "backend"), RedisAddr: getString(rSec, "addr"),
            RedisPassword: maskAPIKey(getString(rSec, "password")),
            RedisDB: getInt(rSec, "db"), RedisPoolSize: getInt(rSec, "pool_size"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Validation Config ────────────────────────────────────────

type validationConfigResponse struct {
    BaseURLConflictCheck bool `json:"base_url_conflict_check"`
}

type validationConfigUpdate struct {
    BaseURLConflictCheck *bool `json:"base_url_conflict_check,omitempty"`
}

func (h *Handler) handleValidationConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        v := config.GetSnapshot().Config.Validation
        writeJSON(w, http.StatusOK, validationConfigResponse{
            BaseURLConflictCheck: v.BaseURLConflictCheck,
        })
    case http.MethodPut:
        var req validationConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "validation")
            applyBool(sec, "base_url_conflict_check", req.BaseURLConflictCheck)
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("validation config updated via admin API")
        sec := getOrCreateSection(doc, "validation")
        writeJSON(w, http.StatusOK, validationConfigResponse{
            BaseURLConflictCheck: getBool(sec, "base_url_conflict_check"),
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Auth Config ──────────────────────────────────────────────

type authKeyResponse struct {
    Key              string            `json:"key"`
    Name             string            `json:"name"`
    RPM              int               `json:"rpm"`
    TPM              int               `json:"tpm"`
    AllowedModels    []string          `json:"allowed_models"`
    AllowedBackends  []string          `json:"allowed_backends"`
    ModelModules     []string          `json:"model_modules"`
    ExpiresAt        string            `json:"expires_at"`
    BudgetLimit      float64           `json:"budget_limit"`
    DailyBudgetLimit float64           `json:"daily_budget_limit"`
    Metadata         map[string]string `json:"metadata"`
}

type authConfigResponse struct {
    Enabled     bool              `json:"enabled"`
    MasterKey   string            `json:"master_key"`
    Passthrough bool              `json:"passthrough"`
    APIKeys     []authKeyResponse `json:"api_keys"`
}

type authConfigUpdate struct {
    Enabled     *bool              `json:"enabled,omitempty"`
    MasterKey   *string            `json:"master_key,omitempty"`
    Passthrough *bool              `json:"passthrough,omitempty"`
    APIKeys     *[]authKeyResponse `json:"api_keys,omitempty"`
}

func (h *Handler) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        a := config.GetSnapshot().Config.Auth
        keys := make([]authKeyResponse, 0, len(a.APIKeys))
        for _, k := range a.APIKeys {
            keys = append(keys, authKeyResponse{
                Key: maskAPIKey(k.Key), Name: k.Name,
                RPM: k.RPM, TPM: k.TPM,
                AllowedModels: k.AllowedModels, AllowedBackends: k.AllowedBackends,
                ModelModules: k.ModelModules,
                ExpiresAt: k.ExpiresAt, BudgetLimit: k.BudgetLimit,
                DailyBudgetLimit: k.DailyBudgetLimit,
                Metadata: k.Metadata,
            })
        }
        writeJSON(w, http.StatusOK, authConfigResponse{
            Enabled: a.Enabled, MasterKey: maskAPIKey(a.MasterKey),
            Passthrough: a.Passthrough, APIKeys: keys,
        })
    case http.MethodPut:
        var req authConfigUpdate
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }
        doc, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
            sec := getOrCreateSection(doc, "auth")
            applyBool(sec, "enabled", req.Enabled)
            applyMaskedString(sec, "master_key", req.MasterKey)
            applyBool(sec, "passthrough", req.Passthrough)
            if req.APIKeys != nil {
                // F9: a GET masks every key (maskAPIKey). A PUT that round-trips
                // those masked values would write entries with no "key" field
                // at all, and sec["api_keys"]=yamlKeys replaces the whole list
                // — so every existing real key is dropped on next reload.
                // Preserve the existing real key per name when the submitted
                // key is masked or empty (no-change), only honor a freshly
                // typed key.
                existing := make(map[string]string, len(config.GetSnapshot().Config.Auth.APIKeys))
                for _, ek := range config.GetSnapshot().Config.Auth.APIKeys {
                    existing[ek.Name] = ek.Key
                }
                var yamlKeys []interface{}
                for _, k := range *req.APIKeys {
                    entry := map[string]interface{}{
                        "name": k.Name, "rpm": k.RPM, "tpm": k.TPM,
                        "allowed_models": stringSliceToInterface(k.AllowedModels),
                        "allowed_backends": stringSliceToInterface(k.AllowedBackends),
                        "model_modules": stringSliceToInterface(k.ModelModules),
                        "expires_at": k.ExpiresAt, "budget_limit": k.BudgetLimit,
                        "daily_budget_limit": k.DailyBudgetLimit,
                    }
                    if k.Key != "" && !isMaskedValue(k.Key) {
                        entry["key"] = k.Key
                    } else if prev, ok := existing[k.Name]; ok && prev != "" {
                        // submitted key masked/empty -> carry the existing real key
                        entry["key"] = prev
                    }
                    yamlKeys = append(yamlKeys, entry)
                }
                sec["api_keys"] = yamlKeys
            }
            return nil
        }); if err != nil {
            writeYAMLError(w, err)
            return
        }
        slog.Info("auth config updated via admin API")
        auditSensitiveChanges("auth", getOrCreateSection(doc, "auth"), adminUsername(r))
        sec := getOrCreateSection(doc, "auth")
        authKeys := []authKeyResponse{}
        if raw, ok := sec["api_keys"].([]interface{}); ok {
            for _, v := range raw {
                if m, ok := v.(map[string]interface{}); ok {
                    authKeys = append(authKeys, authKeyResponse{
                        Key: maskAPIKey(getString(m, "key")), Name: getString(m, "name"),
                        RPM: getInt(m, "rpm"), TPM: getInt(m, "tpm"),
                        AllowedModels: interfaceToStringSlice(m["allowed_models"]),
                        AllowedBackends: interfaceToStringSlice(m["allowed_backends"]),
                        ModelModules: interfaceToStringSlice(m["model_modules"]),
                        ExpiresAt: getString(m, "expires_at"), BudgetLimit: getFloat64(m, "budget_limit"),
                        DailyBudgetLimit: getFloat64(m, "daily_budget_limit"),
                        Metadata: interfaceToStringMap(m["metadata"]),
                    })
                }
            }
        }
        writeJSON(w, http.StatusOK, authConfigResponse{
            Enabled: getBool(sec, "enabled"), MasterKey: maskAPIKey(getString(sec, "master_key")),
            Passthrough: getBool(sec, "passthrough"), APIKeys: authKeys,
        })
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

// ─── Full Config (read-only) ──────────────────────────────────

func (h *Handler) handleFullConfig(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    snap := config.GetSnapshot()
    cfg := &snap.Config
    result := map[string]interface{}{
        "server": map[string]interface{}{
            "host": cfg.Server.Host, "port": cfg.Server.Port,
            "log_level": cfg.Server.LogLevel, "enable_pprof": cfg.Server.EnablePProf,
        },
        "auth": map[string]interface{}{
            "enabled": cfg.Auth.Enabled, "master_key": maskAPIKey(cfg.Auth.MasterKey),
            "passthrough": cfg.Auth.Passthrough,
        },
        "cache": map[string]interface{}{
            "enabled": cfg.Cache.Enabled, "max_entries": cfg.Cache.MaxEntries,
            "ttl": cfg.Cache.TTL.String(), "max_memory_mb": cfg.Cache.MaxMemoryMB,
            "backend": cfg.Cache.Backend,
        },
        "cost": map[string]interface{}{
            "enabled": cfg.Cost.Enabled, "pricing_file": cfg.Cost.PricingFile,
            "budget_alert_threshold": cfg.Cost.BudgetAlertThreshold,
        },
        "cluster": map[string]interface{}{
            "enabled": cfg.Cluster.Enabled, "mode": string(cfg.Cluster.Mode),
        },
        "observability": map[string]interface{}{
            "metrics_enabled": cfg.Observability.MetricsEnabled,
            "otel_enabled": cfg.Observability.OtelEnabled,
        },
    }
    writeJSON(w, http.StatusOK, result)
}

// ─── Shared Helpers ───────────────────────────────────────────

func applyString(sec map[string]interface{}, key string, val *string) {
    if val != nil {
        sec[key] = *val
    }
}

func applyInt(sec map[string]interface{}, key string, val *int) {
    if val != nil {
        sec[key] = *val
    }
}

func applyBool(sec map[string]interface{}, key string, val *bool) {
    if val != nil {
        sec[key] = *val
    }
}

func stringSliceToInterface(s []string) []interface{} {
    result := make([]interface{}, len(s))
    for i, v := range s {
        result[i] = v
    }
    return result
}

func interfaceToStringSlice(v interface{}) []string {
    raw, ok := v.([]interface{})
    if !ok {
        return nil
    }
    result := make([]string, 0, len(raw))
    for _, item := range raw {
        if s, ok := item.(string); ok {
            result = append(result, s)
        }
    }
    return result
}

func interfaceToStringMap(v interface{}) map[string]string {
    raw, ok := v.(map[string]interface{})
    if !ok {
        return nil
    }
    result := make(map[string]string, len(raw))
    for k, val := range raw {
        if s, ok := val.(string); ok {
            result[k] = s
        }
    }
    return result
}

func writeYAMLError(w http.ResponseWriter, err error) {
    if err == errNoConfigPath {
        writeError(w, http.StatusInternalServerError, "config file path not configured")
        return
    }
    errMsg := err.Error()
    slog.Error("config update failed", "error", errMsg)
    status := http.StatusInternalServerError
    if strings.Contains(errMsg, "must be at least") ||
        strings.Contains(errMsg, "invalid") ||
        strings.Contains(errMsg, "required") {
        status = http.StatusBadRequest
    }
    writeError(w, status, errMsg)
}

var _ = time.Second
