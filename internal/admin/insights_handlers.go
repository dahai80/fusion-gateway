package admin

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/insights"
)

// SetInsightsService wires the PG-backed insights query service (P3). Called
// after NewHandler when persistence is enabled; nil (default) means the
// insights endpoints answer 503 with a clear reason.
func (h *Handler) SetInsightsService(svc *insights.Service) {
    h.insights = svc
    if svc != nil {
        slog.Info("insights service attached to admin handler")
    }
}

// InsightsAvailable reports whether the PG read layer is attached. The admin
// handler is built inside Server.Start, so a service wired between New and
// Start has no handler yet — Server re-applies it in Start and this predicate
// is what the wiring regression test asserts on.
func (h *Handler) InsightsAvailable() bool {
    return h.insights != nil
}

func (h *Handler) insightsUnavailable(w http.ResponseWriter) {
    writeError(w, http.StatusServiceUnavailable,
        "insights unavailable: persistence disabled or PostgreSQL not configured")
}

// handleInsightsLatency — GET /admin/api/insights/latency
// Params: from,to (RFC3339, default 24h), granularity (minute|hour|day),
// backend, model, project_id.
func (h *Handler) handleInsightsLatency(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    resp, err := h.insights.Latency(r.Context(),
        q.Get("from"), q.Get("to"), q.Get("granularity"),
        q.Get("backend"), q.Get("model"), q.Get("project_id"))
    if err != nil {
        slog.Warn("insights latency query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, resp)
}

// handleInsightsCompare — GET /admin/api/insights/compare
// Params: from,to,project_id.
func (h *Handler) handleInsightsCompare(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    resp, err := h.insights.Compare(r.Context(), q.Get("from"), q.Get("to"), q.Get("project_id"))
    if err != nil {
        slog.Warn("insights compare query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, resp)
}

// handleInsightsLogSearch — POST /admin/api/insights/logs/search
// Explorer DSL (plan §8): POST because the filter set is too rich for query
// params.
func (h *Handler) handleInsightsLogSearch(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    var sq insights.SearchQuery
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&sq); err != nil {
        writeError(w, http.StatusBadRequest, "invalid search body: "+err.Error())
        return
    }
    resp, err := h.insights.Search(r.Context(), sq)
    if err != nil {
        slog.Warn("insights log search failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, resp)
}

// handleInsightsLogPayload — GET /admin/api/insights/logs/{id}/payload
// Single-row fetch; list queries never join payloads (plan §8 guard).
func (h *Handler) handleInsightsLogPayload(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    id := strings.TrimPrefix(r.URL.Path, "/admin/api/insights/logs/")
    id = strings.TrimSuffix(id, "/payload")
    if id == "" {
        writeError(w, http.StatusBadRequest, "missing log id")
        return
    }
    resp, err := h.insights.Payload(r.Context(), id)
    if err != nil {
        slog.Warn("insights payload fetch failed", "id", id, "error", err)
        writeError(w, http.StatusNotFound, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, resp)
}

// handlePersistenceTest — POST /admin/api/persistence/test
// Verifies the read pool is live (admin Settings page, plan §8).
func (h *Handler) handlePersistenceTest(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
    defer cancel()
    if err := h.insights.Ping(ctx); err != nil {
        slog.Warn("insights persistence test failed", "error", err)
        writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
