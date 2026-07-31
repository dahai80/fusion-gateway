package admin

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type Handler struct {
    store store.Store
}

func NewHandler(st store.Store) *Handler {
    return &Handler{store: st}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
    mux.HandleFunc("/admin/api/keys", h.withAuth(h.handleKeys))
    mux.HandleFunc("/admin/api/keys/", h.withAuth(h.handleKeyByID))
    mux.HandleFunc("/admin/api/channels", h.withAuth(h.handleChannels))
    mux.HandleFunc("/admin/api/channels/", h.withAuth(h.handleChannelByID))
    mux.HandleFunc("/admin/api/logs", h.withAuth(h.handleLogs))
    mux.HandleFunc("/admin/api/logs/export", h.withAuth(h.handleLogsExport))
    mux.HandleFunc("/admin/api/analytics/tokens", h.withAuth(h.handleTokenStats))
    mux.HandleFunc("/admin/api/analytics/cost", h.withAuth(h.handleCostStats))
    mux.HandleFunc("/admin/api/analytics/models", h.withAuth(h.handleModelStats))
    mux.HandleFunc("/admin/api/analytics/latency", h.withAuth(h.handleLatencyStats))
    mux.HandleFunc("/admin/api/analytics/errors", h.withAuth(h.handleErrorStats))
    mux.HandleFunc("/admin/api/dashboard", h.withAuth(h.handleDashboard))
    mux.HandleFunc("/admin/api/quota/", h.withAuth(h.handleQuota))
    slog.Info("admin API routes registered")
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        token := extractBearerToken(r)
        if token == "" {
            writeError(w, http.StatusUnauthorized, "missing authorization token")
            return
        }
        claims, err := ValidateToken(token)
        if err != nil {
            slog.Warn("admin API auth failed", "error", err, "path", r.URL.Path)
            writeError(w, http.StatusUnauthorized, "invalid token")
            return
        }
        ctx := WithAdminContext(r.Context(), claims)
        next.ServeHTTP(w, r.WithContext(ctx))
    }
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

func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        keys, err := h.store.ListKeys()
        if err != nil {
            writeError(w, http.StatusInternalServerError, "failed to list keys")
            return
        }
        writeJSON(w, http.StatusOK, keys)

    case http.MethodPost:
        var key store.APIKeyEntry
        if err := json.NewDecoder(r.Body).Decode(&key); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        if key.Name == "" {
            writeError(w, http.StatusBadRequest, "name is required")
            return
        }
        if err := h.store.CreateKey(&key); err != nil {
            writeError(w, http.StatusConflict, err.Error())
            return
        }
        writeJSON(w, http.StatusCreated, key)

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
        var key store.APIKeyEntry
        if err := json.NewDecoder(r.Body).Decode(&key); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        key.Name = name
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
        writeJSON(w, http.StatusOK, channels)

    case http.MethodPost:
        var ch store.ChannelEntry
        if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
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
        var ch store.ChannelEntry
        if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
            writeError(w, http.StatusBadRequest, "invalid JSON")
            return
        }
        ch.Name = name
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

func (h *Handler) handleTokenStats(w http.ResponseWriter, r *http.Request) {
    from, to := parseTimeRange(r)
    groupBy := r.URL.Query().Get("group_by")
    if groupBy == "" {
        groupBy = "day"
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
