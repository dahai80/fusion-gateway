package admin

import (
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
    "gopkg.in/yaml.v3"
)

type Handler struct {
    store       store.Store
    auth        *AdminAuth
    configPath  string
    configMutex sync.Mutex
}

func NewHandler(st store.Store, auth *AdminAuth, configPath string) *Handler {
    return &Handler{store: st, auth: auth, configPath: configPath}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
    mux.HandleFunc("/admin/api/keys", h.withAuth(h.handleKeys))
    mux.HandleFunc("/admin/api/keys/", h.withAuth(h.handleKeyByID))
    mux.HandleFunc("/admin/api/channels", h.withAuth(h.handleChannels))
    mux.HandleFunc("/admin/api/channels/", h.withAuth(h.handleChannelByID))
    mux.HandleFunc("/admin/api/logs", h.withAuth(h.handleLogs))
    mux.HandleFunc("/admin/api/logs/export", h.withAuth(h.handleLogsExport))
    mux.HandleFunc("/admin/api/analytics", h.withAuth(h.handleAnalyticsOverview))
    mux.HandleFunc("/admin/api/analytics/tokens", h.withAuth(h.handleTokenStats))
    mux.HandleFunc("/admin/api/analytics/cost", h.withAuth(h.handleCostStats))
    mux.HandleFunc("/admin/api/analytics/models", h.withAuth(h.handleModelStats))
    mux.HandleFunc("/admin/api/analytics/latency", h.withAuth(h.handleLatencyStats))
    mux.HandleFunc("/admin/api/analytics/errors", h.withAuth(h.handleErrorStats))
    mux.HandleFunc("/admin/api/analytics/profit", h.withAuth(h.handleProfitStats))
    mux.HandleFunc("/admin/api/dashboard", h.withAuth(h.handleDashboard))
    mux.HandleFunc("/admin/api/quota/", h.withAuth(h.handleQuota))
    mux.HandleFunc("/admin/api/config/routing", h.requireAdminRole(h.handleRoutingConfig))
    mux.HandleFunc("/admin/api/config/backends", h.requireAdminRole(h.handleBackendsConfig))
    mux.HandleFunc("/admin/api/config/backends/", h.requireAdminRole(h.handleBackendByName))
    mux.HandleFunc("/admin/api/config/full", h.requireAdminRole(h.handleFullConfig))
    mux.HandleFunc("/admin/api/config/server", h.requireAdminRole(h.handleServerConfig))
    mux.HandleFunc("/admin/api/config/auth", h.requireAdminRole(h.handleAuthConfig))
    mux.HandleFunc("/admin/api/config/rate-limit", h.requireAdminRole(h.handleRateLimitConfig))
    mux.HandleFunc("/admin/api/config/retry", h.requireAdminRole(h.handleRetryConfig))
    mux.HandleFunc("/admin/api/config/negotiation", h.requireAdminRole(h.handleNegotiationConfig))
    mux.HandleFunc("/admin/api/config/cache", h.requireAdminRole(h.handleCacheConfig))
    mux.HandleFunc("/admin/api/config/cost", h.requireAdminRole(h.handleCostConfig))
    mux.HandleFunc("/admin/api/config/cost-markup", h.requireAdminRole(h.handleCostMarkupConfig))
    mux.HandleFunc("/admin/api/config/pii", h.requireAdminRole(h.handlePIIConfig))
    mux.HandleFunc("/admin/api/config/cloud-routing", h.requireAdminRole(h.handleCloudRoutingConfig))
    mux.HandleFunc("/admin/api/config/hardware", h.requireAdminRole(h.handleHardwareConfig))
    mux.HandleFunc("/admin/api/config/tokenizer", h.requireAdminRole(h.handleTokenizerConfig))
    mux.HandleFunc("/admin/api/config/observability", h.requireAdminRole(h.handleObservabilityConfig))
    mux.HandleFunc("/admin/api/config/cors", h.requireAdminRole(h.handleCORSConfig))
    mux.HandleFunc("/admin/api/config/hot-reload", h.requireAdminRole(h.handleHotReloadConfig))
    mux.HandleFunc("/admin/api/config/cluster", h.requireAdminRole(h.handleClusterConfig))
    mux.HandleFunc("/admin/api/config/realtime", h.requireAdminRole(h.handleRealtimeConfig))
    mux.HandleFunc("/admin/api/config/admin", h.requireAdminRole(h.handleAdminConfig))
    mux.HandleFunc("/admin/api/config/oidc", h.requireAdminRole(h.handleOIDCConfig))
    mux.HandleFunc("/admin/api/config/rbac", h.requireAdminRole(h.handleRBACConfig))
    mux.HandleFunc("/admin/api/config/semantic-cache", h.requireAdminRole(h.handleSemanticCacheConfig))
    mux.HandleFunc("/admin/api/config/prompt-injection", h.requireAdminRole(h.handlePromptInjectionConfig))
    mux.HandleFunc("/admin/api/config/batch", h.requireAdminRole(h.handleBatchConfig))
    mux.HandleFunc("/admin/api/config/store", h.requireAdminRole(h.handleStoreConfig))
    mux.HandleFunc("/admin/api/config/validation", h.requireAdminRole(h.handleValidationConfig))
    slog.Info("admin API routes registered")
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        token := extractBearerToken(r)
        if token == "" {
            writeError(w, http.StatusUnauthorized, "missing authorization token")
            return
        }
        claims, err := h.auth.ValidateToken(token)
        if err != nil {
            slog.Warn("admin API auth failed", "error", err, "path", r.URL.Path)
            writeError(w, http.StatusUnauthorized, "invalid token")
            return
        }
        ctx := WithAdminContext(r.Context(), claims)
        next.ServeHTTP(w, r.WithContext(ctx))
    }
}

// requireAdminRole wraps withAuth and rejects non-admin roles for write operations.
func (h *Handler) requireAdminRole(next http.HandlerFunc) http.HandlerFunc {
    return h.withAuth(func(w http.ResponseWriter, r *http.Request) {
        if r.Method == http.MethodGet {
            next.ServeHTTP(w, r)
            return
        }
        claims := GetAdminClaims(r.Context())
        if claims == nil || claims.Role != "admin" {
            slog.Warn("config write rejected for non-admin role", "role", func() string { if claims != nil { return claims.Role }; return "" }(), "path", r.URL.Path, "method", r.Method)
            writeError(w, http.StatusForbidden, "admin role required for configuration changes")
            return
        }
        next.ServeHTTP(w, r)
    })
}

func extractBearerToken(r *http.Request) string {
    auth := r.Header.Get("Authorization")
    if strings.HasPrefix(auth, "Bearer ") {
        return strings.TrimPrefix(auth, "Bearer ")
    }
    if c, err := r.Cookie("admin_token"); err == nil && c.Value != "" {
        return c.Value
    }
    return ""
}

type createKeyRequest struct {
    Name    string        `json:"name"`
    Status  interface{}   `json:"status"`
    Models  []string      `json:"models"`
    Modules []string      `json:"modules"`
    Quota   float64       `json:"quota"`
    RPM     int           `json:"rpm"`
    TPM     int           `json:"tpm"`
    Budget  float64       `json:"budget"`
}

type createChannelRequest struct {
    Name      string      `json:"name"`
    Type      string      `json:"type"`
    BaseURL   string      `json:"base_url"`
    Key       string      `json:"key"`
    Models    []string    `json:"models"`
    Status    interface{} `json:"status"`
    Priority  int         `json:"priority"`
    Weight    int         `json:"weight"`
    MaxTokens int         `json:"max_tokens"`
}

func normalizeStatus(s interface{}) string {
    switch v := s.(type) {
    case float64:
        if v == 1 {
            return "active"
        }
        return "disabled"
    case string:
        return v
    default:
        return "active"
    }
}

func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        keys, err := h.store.ListKeys()
        if err != nil {
            writeError(w, http.StatusInternalServerError, "failed to list keys")
            return
        }
        result := make([]keyResponse, 0, len(keys))
        for i, k := range keys {
            result = append(result, keyResponse{
                ID:        i + 1,
                Key:       k.KeyPrefix,
                Name:      k.Name,
                Status:    statusToNumber(k.Status),
                Models:    k.AllowedModels,
                Modules:   k.ModelModules,
                Quota:     k.QuotaLimit,
                UsedQuota: k.QuotaUsed,
                CreatedAt: k.CreatedAt.Format(time.RFC3339),
            })
        }
        writeJSON(w, http.StatusOK, result)

    case http.MethodPost:
        var req createKeyRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        key := store.APIKeyEntry{
            Name:            req.Name,
            Status:          normalizeStatus(req.Status),
            AllowedModels:   req.Models,
            ModelModules:    req.Modules,
            QuotaLimit:      req.Quota,
            RPM:             req.RPM,
            TPM:             req.TPM,
            BudgetLimit:     req.Budget,
        }
        // V1 fix: validate key fields
        if key.Name == "" {
            writeError(w, http.StatusBadRequest, "name is required")
            return
        }
        if key.QuotaLimit < 0 {
            writeError(w, http.StatusBadRequest, "quota_limit must be non-negative")
            return
        }
        if key.RPM < 0 {
            writeError(w, http.StatusBadRequest, "rpm must be non-negative")
            return
        }
        if key.TPM < 0 {
            writeError(w, http.StatusBadRequest, "tpm must be non-negative")
            return
        }
        if key.BudgetLimit < 0 {
            writeError(w, http.StatusBadRequest, "budget_limit must be non-negative")
            return
        }
        rawKey, err := generateAPIKey()
        if err != nil {
            slog.Error("failed to generate api key", "error", err)
            writeError(w, http.StatusInternalServerError, "key generation failed")
            return
        }
        key.KeyPrefix = "sk-" + rawKey[:8]
        if err := h.store.CreateKey(&key); err != nil {
            slog.Error("failed to create key", "error", err)
            writeError(w, http.StatusConflict, "key creation failed")
            return
        }
        writeJSON(w, http.StatusCreated, map[string]interface{}{
            "name":       key.Name,
            "key_prefix": key.KeyPrefix,
            "raw_key":    "sk-" + rawKey,
            "status":     key.Status,
            "models":     key.AllowedModels,
            "modules":    key.ModelModules,
            "quota":      key.QuotaLimit,
            "created_at": key.CreatedAt.Format(time.RFC3339),
        })

    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

func (h *Handler) handleKeyByID(w http.ResponseWriter, r *http.Request) {
    name := strings.TrimPrefix(r.URL.Path, "/admin/api/keys/")
    if name == "" {
        writeError(w, http.StatusBadRequest, "key name required")
        return
    }

    switch r.Method {
    case http.MethodGet:
        key, err := h.store.GetKey(name)
        if err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusOK, key)

    case http.MethodPut:
        var req createKeyRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        key := store.APIKeyEntry{
            Name:          name,
            Status:        normalizeStatus(req.Status),
            AllowedModels: req.Models,
            ModelModules:  req.Modules,
            QuotaLimit:    req.Quota,
            RPM:           req.RPM,
            TPM:           req.TPM,
            BudgetLimit:   req.Budget,
        }
        if err := h.store.UpdateKey(&key); err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusOK, key)

    case http.MethodDelete:
        if err := h.store.DeleteKey(name); err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusNoContent, nil)

    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

func (h *Handler) handleChannels(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        channels, err := h.store.ListChannels()
        if err != nil {
            writeError(w, http.StatusInternalServerError, "failed to list channels")
            return
        }
        result := make([]channelResponse, 0, len(channels))
        for i, c := range channels {
            result = append(result, channelResponse{
                ID:        i + 1,
                Name:      c.Name,
                Type:      c.Type,
                BaseURL:   c.BaseURL,
                Models:    c.Models,
                Status:    statusToNumber(c.Status),
                Priority:  c.Priority,
                Weight:    c.Weight,
                MaxTokens: 4096,
                CreatedAt: c.CreatedAt.Format(time.RFC3339),
            })
        }
        writeJSON(w, http.StatusOK, result)

    case http.MethodPost:
        var req createChannelRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        ch := store.ChannelEntry{
            Name:     req.Name,
            Type:     req.Type,
            BaseURL:  req.BaseURL,
            Models:   req.Models,
            Status:   normalizeStatus(req.Status),
            Priority: req.Priority,
            Weight:   req.Weight,
        }
        if ch.Name == "" {
            writeError(w, http.StatusBadRequest, "name is required")
            return
        }
        if err := h.store.CreateChannel(&ch); err != nil {
            writeError(w, http.StatusConflict, err.Error())
            return
        }
        writeJSON(w, http.StatusCreated, ch)

    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

func (h *Handler) handleChannelByID(w http.ResponseWriter, r *http.Request) {
    name := strings.TrimPrefix(r.URL.Path, "/admin/api/channels/")
    if name == "" {
        writeError(w, http.StatusBadRequest, "channel name required")
        return
    }

    switch r.Method {
    case http.MethodGet:
        ch, err := h.store.GetChannel(name)
        if err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusOK, ch)

    case http.MethodPut:
        var req createChannelRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        ch := store.ChannelEntry{
            Name:     name,
            Type:     req.Type,
            BaseURL:  req.BaseURL,
            Models:   req.Models,
            Status:   normalizeStatus(req.Status),
            Priority: req.Priority,
            Weight:   req.Weight,
        }
        if err := h.store.UpdateChannel(&ch); err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusOK, ch)

    case http.MethodDelete:
        if err := h.store.DeleteChannel(name); err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusNoContent, nil)

    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    filter := parseLogFilter(r)
    logs, total, err := h.store.QueryLogs(filter)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "query failed")
        return
    }

    writeJSON(w, http.StatusOK, map[string]interface{}{
        "logs":  logs,
        "total": total,
        "page":  filter.Page,
        "limit": filter.PageSize,
    })
}

func (h *Handler) handleLogsExport(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    filter := parseLogFilter(r)
    format := r.URL.Query().Get("format")
    if format == "" {
        format = "json"
    }
    if format != "json" && format != "csv" {
        writeError(w, http.StatusBadRequest, "format must be json or csv")
        return
    }

    data, err := h.store.ExportLogs(filter, format)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "export failed")
        return
    }

    if format == "csv" {
        w.Header().Set("Content-Type", "text/csv")
        w.Header().Set("Content-Disposition", "attachment; filename=logs.csv")
    } else {
        w.Header().Set("Content-Type", "application/json")
        w.Header().Set("Content-Disposition", "attachment; filename=logs.json")
    }
    _, _ = w.Write(data)
}

func (h *Handler) handleAnalyticsOverview(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    filter := store.LogFilter{
        Page:      1,
        PageSize:  10000,
        StartTime: &from,
        EndTime:   &to,
    }
    logs, _, err := h.store.QueryLogs(filter)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to query logs")
        return
    }

    var totalPrompt, totalCompletion, totalTokens int64
    var totalCost, localCost, cloudCost float64
    var totalLatencyMs float64
    var errorCount int
    modelCounts := make(map[string]int)
    errCodes := make(map[int]int)

    for _, l := range logs {
        totalPrompt += int64(l.InputTokens)
        totalCompletion += int64(l.OutputTokens)
        totalTokens += int64(l.TotalTokens)
        totalCost += l.Cost
        if l.ChannelType == "local" {
            localCost += l.Cost
        } else {
            cloudCost += l.Cost
        }
        if l.Model != "" {
            modelCounts[l.Model]++
        }
        totalLatencyMs += l.Latency
        if l.StatusCode >= 400 {
            errorCount++
            errCodes[l.StatusCode]++
        }
    }

    var avgTokens float64
    if len(logs) > 0 {
        avgTokens = float64(totalTokens) / float64(len(logs))
    }
    var avgCost float64
    if len(logs) > 0 {
        avgCost = totalCost / float64(len(logs))
    }
    var avgLatency float64
    if len(logs) > 0 {
        avgLatency = totalLatencyMs / float64(len(logs))
    }
    var errorRate float64
    if len(logs) > 0 {
        errorRate = float64(errorCount) / float64(len(logs)) * 100
    }

    modelDistribution := make([]map[string]interface{}, 0)
    for m, c := range modelCounts {
        modelDistribution = append(modelDistribution, map[string]interface{}{
            "name":  m,
            "value": c,
        })
    }

    topCodes := make([]map[string]interface{}, 0)
    for code, cnt := range errCodes {
        topCodes = append(topCodes, map[string]interface{}{
            "code":  code,
            "count": cnt,
        })
    }

    result := map[string]interface{}{
        "token": map[string]interface{}{
            "summary": map[string]interface{}{
                "total_prompt":     totalPrompt,
                "total_completion": totalCompletion,
                "total_tokens":     totalTokens,
                "avg_per_request":  int64(avgTokens),
            },
            "trend": []interface{}{},
        },
        "cost": map[string]interface{}{
            "summary": map[string]interface{}{
                "total_cost":      totalCost,
                "avg_per_request": avgCost,
                "local_cost":      localCost,
                "cloud_cost":      cloudCost,
            },
            "trend": []interface{}{},
        },
        "model": map[string]interface{}{
            "distribution": modelDistribution,
            "trend":        []interface{}{},
        },
        "latency": map[string]interface{}{
            "summary": map[string]interface{}{
                "avg_ms": avgLatency,
                "p50_ms": avgLatency,
                "p95_ms": avgLatency * 1.5,
                "p99_ms": avgLatency * 2,
            },
            "trend": []interface{}{},
        },
        "error": map[string]interface{}{
            "summary": map[string]interface{}{
                "total_errors": errorCount,
                "error_rate":   errorRate,
                "top_codes":    topCodes,
            },
            "trend": []interface{}{},
        },
    }
    writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleTokenStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    groupBy := r.URL.Query().Get("group_by")
    if groupBy == "" {
        groupBy = "day"
    }
    // #6-8 fix: whitelist group_by values
    if !isValidGroupBy(groupBy) {
        writeError(w, http.StatusBadRequest, "group_by must be one of: hour, day, week, month, model, backend, key")
        return
    }
    stats, err := h.store.GetTokenStats(from, to, groupBy)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get token stats")
        return
    }
    writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleCostStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    groupBy := r.URL.Query().Get("group_by")
    if groupBy == "" {
        groupBy = "day"
    }
    if !isValidGroupBy(groupBy) {
        writeError(w, http.StatusBadRequest, "group_by must be one of: hour, day, week, month, model, backend, key")
        return
    }
    stats, err := h.store.GetCostStats(from, to, groupBy)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get cost stats")
        return
    }
    writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleModelStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    stats, err := h.store.GetModelStats(from, to)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get model stats")
        return
    }
    writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleLatencyStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    stats, err := h.store.GetLatencyStats(from, to)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get latency stats")
        return
    }
    writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleErrorStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    stats, err := h.store.GetErrorStats(from, to)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get error stats")
        return
    }
    writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    overview, err := h.store.GetDashboardOverview()
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get dashboard data")
        return
    }
    writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) handleQuota(w http.ResponseWriter, r *http.Request) {
    keyName := strings.TrimPrefix(r.URL.Path, "/admin/api/quota/")
    if keyName == "" {
        writeError(w, http.StatusBadRequest, "key name required")
        return
    }

    if r.Method == http.MethodGet {
        used, limit, exceeded, err := h.store.CheckQuota(keyName)
        if err != nil {
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        writeJSON(w, http.StatusOK, map[string]interface{}{
            "key_name": keyName,
            "used":     used,
            "limit":    limit,
            "exceeded": exceeded,
        })
        return
    }

    if r.Method == http.MethodPost {
        var req struct {
            Amount float64 `json:"amount"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        if err := h.store.DeductQuota(keyName, req.Amount); err != nil {
            writeError(w, http.StatusInternalServerError, "deduction failed")
            return
        }
        writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
        return
    }

    writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func parseLogFilter(r *http.Request) store.LogFilter {
    q := r.URL.Query()
    filter := store.LogFilter{
        KeyName:  q.Get("key_name"),
        Model:    q.Get("model"),
        Channel:  q.Get("channel"),
        Status:   q.Get("status"),
        Page:     1,
        PageSize: 50,
    }

    if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
        filter.Page = p
    }
    if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= 500 {
        filter.PageSize = l
    }
    if mt, err := strconv.Atoi(q.Get("min_tokens")); err == nil {
        filter.MinTokens = mt
    }
    if mc, err := strconv.ParseFloat(q.Get("min_cost"), 64); err == nil {
        filter.MinCost = mc
    }

    if from := q.Get("from"); from != "" {
        if t, err := time.Parse(time.RFC3339, from); err == nil {
            filter.StartTime = &t
        }
    }
    if to := q.Get("to"); to != "" {
        if t, err := time.Parse(time.RFC3339, to); err == nil {
            filter.EndTime = &t
        }
    }

    return filter
}

func parseTimeRange(r *http.Request) (time.Time, time.Time) {
    now := time.Now()
    from := now.AddDate(0, 0, -7)
    to := now

    if f := r.URL.Query().Get("from"); f != "" {
        if t, err := time.Parse(time.RFC3339, f); err == nil {
            from = t
        }
    }
    if t := r.URL.Query().Get("to"); t != "" {
        if parsed, err := time.Parse(time.RFC3339, t); err == nil {
            to = parsed
        }
    }

    return from, to
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if data != nil {
        _ = json.NewEncoder(w).Encode(data)
    }
}

func writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "error": map[string]string{
            "message": msg,
            "type":    fmt.Sprintf("http_%d", status),
        },
    })
}

// isValidGroupBy checks that group_by parameter only contains allowed values
func isValidGroupBy(val string) bool {
    switch val {
    case "hour", "day", "week", "month", "model", "backend", "key":
        return true
    default:
        return false
    }
}

func (h *Handler) handleProfitStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    stats, err := h.store.GetKeyProfitStats(from, to)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to get profit stats")
        return
    }

    var totalInput, totalOutput int64
    var totalCost float64
    var totalRatio float64
    var ratioCount int
    for _, s := range stats {
        totalInput += s.TotalInput
        totalOutput += s.TotalOutput
        totalCost += s.TotalCost
        if s.Ratio > 0 {
            totalRatio += s.Ratio
            ratioCount++
        }
    }
    var avgRatio float64
    if ratioCount > 0 {
        avgRatio = totalRatio / float64(ratioCount)
    }

    writeJSON(w, http.StatusOK, map[string]interface{}{
        "keys": stats,
        "summary": map[string]interface{}{
            "total_keys":      len(stats),
            "avg_ratio":       avgRatio,
            "total_input":     totalInput,
            "total_output":    totalOutput,
            "total_cost":      totalCost,
        },
    })
}

// Frontend response adapters — map backend fields to frontend-expected names

type keyResponse struct {
    ID        int      `json:"id"`
    Key       string   `json:"key"`
    Name      string   `json:"name"`
    Status    int      `json:"status"`
    Models    []string `json:"models"`
    Modules   []string `json:"modules"`
    Quota     float64  `json:"quota"`
    UsedQuota float64  `json:"used_quota"`
    CreatedAt string   `json:"created_at"`
}

type channelResponse struct {
    ID        int      `json:"id"`
    Name      string   `json:"name"`
    Type      string   `json:"type"`
    BaseURL   string   `json:"base_url"`
    Models    []string `json:"models"`
    Status    int      `json:"status"`
    Priority  int      `json:"priority"`
    Weight    int      `json:"weight"`
    MaxTokens int      `json:"max_tokens"`
    CreatedAt string   `json:"created_at"`
}

func statusToNumber(s string) int {
    if s == "active" || s == "enabled" {
        return 1
    }
    return 0
}

type ratioTierRuleResponse struct {
    MaxRatio float64 `json:"max_ratio"`
    Backend  string  `json:"backend"`
}

type tokenTierRuleResponse struct {
    MaxTokens int    `json:"max_tokens"`
    Backend   string `json:"backend"`
}

type routingConfigResponse struct {
    Mode                      string                   `json:"mode"`
    TokenThreshold            int                      `json:"token_threshold"`
    OutputInputRatioThreshold float64                  `json:"output_input_ratio_threshold"`
    RatioTiersEnabled         bool                     `json:"ratio_tiers_enabled"`
    RatioTiersRules           []ratioTierRuleResponse  `json:"ratio_tiers_rules"`
    TokenTiersEnabled         bool                     `json:"token_tiers_enabled"`
    TokenTiersMetric          string                   `json:"token_tiers_metric"`
    TokenTiersRules           []tokenTierRuleResponse  `json:"token_tiers_rules"`
    LocalPriorityEnabled      bool                     `json:"local_priority_enabled"`
    MaxSystemMemoryRatio      float64                  `json:"max_system_memory_ratio"`
    MaxMLXMemoryRatio         float64                  `json:"max_mlx_memory_ratio"`
    MaxConcurrent             int                      `json:"max_concurrent"`
    CircuitBreakerEnabled     bool                     `json:"circuit_breaker_enabled"`
    FallbackEnabled           bool                     `json:"fallback_enabled"`
    FallbackCloudDefault      string                   `json:"fallback_cloud_default"`
}

type routingConfigUpdate struct {
    Mode                      *string                   `json:"mode,omitempty"`
    TokenThreshold            *int                      `json:"token_threshold,omitempty"`
    OutputInputRatioThreshold *float64                  `json:"output_input_ratio_threshold,omitempty"`
    RatioTiersEnabled         *bool                     `json:"ratio_tiers_enabled,omitempty"`
    RatioTiersRules           *[]ratioTierRuleResponse  `json:"ratio_tiers_rules,omitempty"`
    TokenTiersEnabled         *bool                     `json:"token_tiers_enabled,omitempty"`
    TokenTiersMetric          *string                   `json:"token_tiers_metric,omitempty"`
    TokenTiersRules           *[]tokenTierRuleResponse  `json:"token_tiers_rules,omitempty"`
    LocalPriorityEnabled      *bool                     `json:"local_priority_enabled,omitempty"`
    MaxSystemMemoryRatio      *float64                  `json:"max_system_memory_ratio,omitempty"`
    MaxMLXMemoryRatio         *float64                  `json:"max_mlx_memory_ratio,omitempty"`
    MaxConcurrent             *int                      `json:"max_concurrent,omitempty"`
    CircuitBreakerEnabled     *bool                     `json:"circuit_breaker_enabled,omitempty"`
    FallbackEnabled           *bool                     `json:"fallback_enabled,omitempty"`
    FallbackCloudDefault      *string                   `json:"fallback_cloud_default,omitempty"`
}

func (h *Handler) handleRoutingConfig(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        h.handleGetRoutingConfig(w, r)
    case http.MethodPut:
        h.handleUpdateRoutingConfig(w, r)
    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

func (h *Handler) handleGetRoutingConfig(w http.ResponseWriter, r *http.Request) {
    snap := config.GetSnapshot()
    cfg := &snap.Config
    ratioRules := make([]ratioTierRuleResponse, 0, len(cfg.Routing.RatioTiers.Rules))
    for _, r := range cfg.Routing.RatioTiers.Rules {
        ratioRules = append(ratioRules, ratioTierRuleResponse{MaxRatio: r.MaxRatio, Backend: r.Backend})
    }
    tokenRules := make([]tokenTierRuleResponse, 0, len(cfg.Routing.TokenTiers.Rules))
    for _, r := range cfg.Routing.TokenTiers.Rules {
        tokenRules = append(tokenRules, tokenTierRuleResponse{MaxTokens: r.MaxTokens, Backend: r.Backend})
    }
    resp := routingConfigResponse{
        Mode:                      cfg.Routing.Mode,
        TokenThreshold:            cfg.Routing.TokenThreshold,
        OutputInputRatioThreshold: cfg.Routing.OutputInputRatioThreshold,
        RatioTiersEnabled:         cfg.Routing.RatioTiers.Enabled,
        RatioTiersRules:           ratioRules,
        TokenTiersEnabled:         cfg.Routing.TokenTiers.Enabled,
        TokenTiersMetric:          cfg.Routing.TokenTiers.Metric,
        TokenTiersRules:           tokenRules,
        LocalPriorityEnabled:      cfg.Routing.LocalPriority.Enabled,
        MaxSystemMemoryRatio:      cfg.Routing.LocalPriority.MaxSystemMemoryRatio,
        MaxMLXMemoryRatio:         cfg.Routing.LocalPriority.MaxMLXMemoryRatio,
        MaxConcurrent:             cfg.Routing.LocalPriority.MaxConcurrent,
        CircuitBreakerEnabled:     cfg.Routing.CircuitBreaker.FailureThreshold > 0,
        FallbackEnabled:           cfg.Routing.Fallback.Enabled,
        FallbackCloudDefault:      cfg.Routing.Fallback.CloudDefault,
    }
    slog.Info("routing config read", "token_threshold", resp.TokenThreshold, "ratio_threshold", resp.OutputInputRatioThreshold,
        "ratio_tiers_enabled", resp.RatioTiersEnabled, "ratio_tiers_rules", len(resp.RatioTiersRules))
    writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleUpdateRoutingConfig(w http.ResponseWriter, r *http.Request) {
    var req routingConfigUpdate
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request body")
        return
    }

    if h.configPath == "" {
        writeError(w, http.StatusInternalServerError, "config file path not configured")
        return
    }

    h.configMutex.Lock()
    defer h.configMutex.Unlock()

    raw, err := os.ReadFile(h.configPath)
    if err != nil {
        slog.Error("failed to read config file", "path", h.configPath, "error", err)
        writeError(w, http.StatusInternalServerError, "failed to read config file")
        return
    }

    var doc map[string]interface{}
    if err := yaml.Unmarshal(raw, &doc); err != nil {
        slog.Error("failed to parse config yaml", "error", err)
        writeError(w, http.StatusInternalServerError, "failed to parse config file")
        return
    }

    routing, _ := doc["routing"].(map[string]interface{})
    if routing == nil {
        routing = make(map[string]interface{})
        doc["routing"] = routing
    }

    if req.TokenThreshold != nil {
        routing["token_threshold"] = *req.TokenThreshold
    }
    if req.Mode != nil {
        if *req.Mode != "local" && *req.Mode != "cloud" && *req.Mode != "hybrid" {
            writeError(w, http.StatusBadRequest, "mode must be local, cloud, or hybrid")
            return
        }
        routing["mode"] = *req.Mode
    }
    if req.OutputInputRatioThreshold != nil {
        routing["output_input_ratio_threshold"] = *req.OutputInputRatioThreshold
    }
    if req.LocalPriorityEnabled != nil || req.MaxSystemMemoryRatio != nil ||
        req.MaxMLXMemoryRatio != nil || req.MaxConcurrent != nil {
        lp, _ := routing["local_priority"].(map[string]interface{})
        if lp == nil {
            lp = make(map[string]interface{})
            routing["local_priority"] = lp
        }
        if req.LocalPriorityEnabled != nil {
            lp["enabled"] = *req.LocalPriorityEnabled
        }
        if req.MaxSystemMemoryRatio != nil {
            lp["max_system_memory_ratio"] = *req.MaxSystemMemoryRatio
        }
        if req.MaxMLXMemoryRatio != nil {
            lp["max_mlx_memory_ratio"] = *req.MaxMLXMemoryRatio
        }
        if req.MaxConcurrent != nil {
            lp["max_concurrent"] = *req.MaxConcurrent
        }
    }
    if req.CircuitBreakerEnabled != nil {
        cb, _ := routing["circuit_breaker"].(map[string]interface{})
        if cb == nil {
            cb = make(map[string]interface{})
            routing["circuit_breaker"] = cb
        }
        snap := config.GetSnapshot()
        if *req.CircuitBreakerEnabled {
            cb["failure_threshold"] = snap.Config.Routing.CircuitBreaker.FailureThreshold
            if snap.Config.Routing.CircuitBreaker.FailureThreshold == 0 {
                cb["failure_threshold"] = 5
            }
        } else {
            cb["failure_threshold"] = 0
        }
    }
    if req.FallbackEnabled != nil || req.FallbackCloudDefault != nil {
        fb, _ := routing["fallback"].(map[string]interface{})
        if fb == nil {
            fb = make(map[string]interface{})
            routing["fallback"] = fb
        }
        if req.FallbackEnabled != nil {
            fb["enabled"] = *req.FallbackEnabled
        }
        if req.FallbackCloudDefault != nil {
            fb["cloud_default"] = *req.FallbackCloudDefault
        }
    }
    if req.RatioTiersEnabled != nil || req.RatioTiersRules != nil {
        rt, _ := routing["ratio_tiers"].(map[string]interface{})
        if rt == nil {
            rt = make(map[string]interface{})
            routing["ratio_tiers"] = rt
        }
        if req.RatioTiersEnabled != nil {
            rt["enabled"] = *req.RatioTiersEnabled
        }
        if req.RatioTiersRules != nil {
            var yamlRules []interface{}
            for _, rule := range *req.RatioTiersRules {
                yamlRules = append(yamlRules, map[string]interface{}{
                    "max_ratio": rule.MaxRatio,
                    "backend":   rule.Backend,
                })
            }
            rt["rules"] = yamlRules
        }
    }
    if req.TokenTiersEnabled != nil || req.TokenTiersMetric != nil || req.TokenTiersRules != nil {
        tt, _ := routing["token_tiers"].(map[string]interface{})
        if tt == nil {
            tt = make(map[string]interface{})
            routing["token_tiers"] = tt
        }
        if req.TokenTiersEnabled != nil {
            tt["enabled"] = *req.TokenTiersEnabled
        }
        if req.TokenTiersMetric != nil {
            tt["metric"] = *req.TokenTiersMetric
        }
        if req.TokenTiersRules != nil {
            var yamlRules []interface{}
            for _, rule := range *req.TokenTiersRules {
                yamlRules = append(yamlRules, map[string]interface{}{
                    "max_tokens": rule.MaxTokens,
                    "backend":    rule.Backend,
                })
            }
            tt["rules"] = yamlRules
        }
    }

    out, err := yaml.Marshal(doc)
    if err != nil {
        slog.Error("failed to marshal config yaml", "error", err)
        writeError(w, http.StatusInternalServerError, "failed to marshal config")
        return
    }

    if err := os.WriteFile(h.configPath, out, 0600); err != nil {
        slog.Error("failed to write config file", "path", h.configPath, "error", err)
        writeError(w, http.StatusInternalServerError, "failed to write config file")
        return
    }

    slog.Info("routing config updated via admin API", "path", h.configPath)
    h.handleGetRoutingConfig(w, r)
}

type backendResponse struct {
    Name    string `json:"name"`
    Type    string `json:"type"`
    BaseURL string `json:"base_url"`
    APIKey  string `json:"api_key"`
    Timeout string `json:"timeout"`
    Enabled bool   `json:"enabled"`
}

type backendUpdateRequest struct {
    BaseURL *string `json:"base_url,omitempty"`
    APIKey  *string `json:"api_key,omitempty"`
    Enabled *bool   `json:"enabled,omitempty"`
    Timeout *string `json:"timeout,omitempty"`
}

func maskAPIKey(key string) string {
    if key == "" {
        return ""
    }
    if len(key) <= 4 {
        return "****"
    }
    return "****" + key[len(key)-4:]
}

func (h *Handler) handleBackendsConfig(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    snap := config.GetSnapshot()
    result := make([]backendResponse, 0)
    for name, b := range snap.Config.Backends {
        result = append(result, backendResponse{
            Name:    name,
            Type:    b.Type,
            BaseURL: b.BaseURL,
            APIKey:  maskAPIKey(b.APIKey),
            Timeout: b.Timeout.String(),
            Enabled: b.Enabled,
        })
    }
    slog.Info("backends config read", "count", len(result))
    writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleBackendByName(w http.ResponseWriter, r *http.Request) {
    name := strings.TrimPrefix(r.URL.Path, "/admin/api/config/backends/")
    if name == "" {
        writeError(w, http.StatusBadRequest, "backend name required")
        return
    }

    switch r.Method {
    case http.MethodGet:
        snap := config.GetSnapshot()
        b, ok := snap.Config.Backends[name]
        if !ok {
            writeError(w, http.StatusNotFound, "backend not found")
            return
        }
        writeJSON(w, http.StatusOK, backendResponse{
            Name:    name,
            Type:    b.Type,
            BaseURL: b.BaseURL,
            APIKey:  maskAPIKey(b.APIKey),
            Timeout: b.Timeout.String(),
            Enabled: b.Enabled,
        })

    case http.MethodPut:
        var req backendUpdateRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            writeError(w, http.StatusBadRequest, "invalid request body")
            return
        }

        if h.configPath == "" {
            writeError(w, http.StatusInternalServerError, "config file path not configured")
            return
        }

        snap := config.GetSnapshot()
        if _, ok := snap.Config.Backends[name]; !ok {
            writeError(w, http.StatusNotFound, "backend not found")
            return
        }

        h.configMutex.Lock()
        defer h.configMutex.Unlock()

        raw, err := os.ReadFile(h.configPath)
        if err != nil {
            slog.Error("failed to read config file", "path", h.configPath, "error", err)
            writeError(w, http.StatusInternalServerError, "failed to read config file")
            return
        }

        var doc map[string]interface{}
        if err := yaml.Unmarshal(raw, &doc); err != nil {
            slog.Error("failed to parse config yaml", "error", err)
            writeError(w, http.StatusInternalServerError, "failed to parse config file")
            return
        }

        backends, _ := doc["backends"].(map[string]interface{})
        if backends == nil {
            writeError(w, http.StatusInternalServerError, "backends section not found in config")
            return
        }

        backend, _ := backends[name].(map[string]interface{})
        if backend == nil {
            writeError(w, http.StatusNotFound, "backend not found in config file")
            return
        }

        if req.BaseURL != nil {
            backend["base_url"] = *req.BaseURL
        }
        if req.APIKey != nil {
            backend["api_key"] = *req.APIKey
        }
        if req.Enabled != nil {
            backend["enabled"] = *req.Enabled
        }
        if req.Timeout != nil {
            backend["timeout"] = *req.Timeout
        }

        out, err := yaml.Marshal(doc)
        if err != nil {
            slog.Error("failed to marshal config yaml", "error", err)
            writeError(w, http.StatusInternalServerError, "failed to marshal config")
            return
        }

        if err := os.WriteFile(h.configPath, out, 0600); err != nil {
            slog.Error("failed to write config file", "path", h.configPath, "error", err)
            writeError(w, http.StatusInternalServerError, "failed to write config file")
            return
        }

        slog.Info("backend config updated via admin API", "backend", name, "path", h.configPath)

        newSnap := config.GetSnapshot()
        b := newSnap.Config.Backends[name]
        writeJSON(w, http.StatusOK, backendResponse{
            Name:    name,
            Type:    b.Type,
            BaseURL: b.BaseURL,
            APIKey:  maskAPIKey(b.APIKey),
            Timeout: b.Timeout.String(),
            Enabled: b.Enabled,
        })

    default:
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}

func generateAPIKey() (string, error) {
    b := make([]byte, 24)
    if _, err := rand.Read(b); err != nil {
        return "", err
    }
    return hex.EncodeToString(b), nil
}
