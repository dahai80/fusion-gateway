package admin

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/http"
    "strconv"
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

// handleInsightsTimeline — GET /admin/api/insights/timeline (§15.2)
// Four-track overlay: request volume + errors (fg_request_logs), breaker
// events (fg_gateway_events), hardware memory ratios (fg_hw_samples).
// Params: from,to (RFC3339, default 24h), granularity (minute|hour|day).
func (h *Handler) handleInsightsTimeline(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    resp, err := h.insights.Timeline(r.Context(),
        q.Get("from"), q.Get("to"), q.Get("granularity"))
    if err != nil {
        slog.Warn("insights timeline query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, resp)
}

// handleInsightsOverview — GET /admin/api/insights/overview (#421)
// Persistent Dashboard rollup: KPI totals, latency percentiles, gateway
// cache hit rates, top models/backends, trend series.
// Params: from,to (RFC3339, default 24h), granularity (minute|hour|day).
func (h *Handler) handleInsightsOverview(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    resp, err := h.insights.Overview(r.Context(),
        q.Get("from"), q.Get("to"), q.Get("granularity"))
    if err != nil {
        slog.Warn("insights overview query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, resp)
}

// handleInsightsCompare — GET /admin/api/insights/compare
// Params: from,to,project_id, input_min,input_max (prompt-length bucket,
// <=0 unbounded), overlap=true (only hourly buckets where every group has
// traffic — AC5 shared-load fairness).
func (h *Handler) handleInsightsCompare(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    inputMin, _ := strconv.Atoi(q.Get("input_min"))
    inputMax, _ := strconv.Atoi(q.Get("input_max"))
    overlap := q.Get("overlap") == "true" || q.Get("overlap") == "1"
    resp, err := h.insights.Compare(r.Context(),
        q.Get("from"), q.Get("to"), q.Get("project_id"), inputMin, inputMax, overlap)
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
// Single-row fetch; list queries never join payloads (plan §8 guard). Each
// successful fetch lands a payload_queried audit row — sensitive-body access
// must leave an operator trail (plan §15.5).
func (h *Handler) handleInsightsLogPayload(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    rest := strings.TrimPrefix(r.URL.Path, "/admin/api/insights/logs/")
    if strings.HasSuffix(rest, "/replay") {
        id := strings.TrimSuffix(rest, "/replay")
        if id == "" {
            writeError(w, http.StatusBadRequest, "missing log id")
            return
        }
        h.handleInsightsLogReplay(w, r, id)
        return
    }
    if strings.HasSuffix(rest, "/chunks") {
        id := strings.TrimSuffix(rest, "/chunks")
        if id == "" {
            writeError(w, http.StatusBadRequest, "missing log id")
            return
        }
        h.handleInsightsLogChunks(w, r, id)
        return
    }
    id := strings.TrimSuffix(rest, "/payload")
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
    actor := "admin"
    if claims := GetAdminClaims(r.Context()); claims != nil && claims.Username != "" {
        actor = claims.Username
    }
    h.auditEvent("info", "payload_queried", map[string]any{
        "id": id, "by": actor,
    })
    writeJSON(w, http.StatusOK, resp)
}

// handleInsightsLogChunks — GET /admin/api/insights/logs/{id}/chunks (G10).
// Per-chunk stream timings from fg_stream_chunks; an empty list means chunk
// capture was off for that request (Explorer renders the tab gray), not an
// error.
func (h *Handler) handleInsightsLogChunks(w http.ResponseWriter, r *http.Request, id string) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    rows, err := h.insights.StreamChunks(r.Context(), id)
    if err != nil {
        slog.Warn("insights stream chunks fetch failed", "id", id, "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"id": id, "chunks": rows})
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

// handleInsightsEvents — GET /admin/api/events (plan §15.5)
// Structured gateway lifecycle stream. Params: level, event, limit.
func (h *Handler) handleInsightsEvents(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    limit := 0
    if v, err := strconv.Atoi(q.Get("limit")); err == nil {
        limit = v
    }
    events, err := h.insights.Events(r.Context(), q.Get("level"), q.Get("event"), limit)
    if err != nil {
        slog.Warn("insights events query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": len(events)})
}

// handleInsightsHWSamples — GET /admin/api/insights/hw-samples
// Hardware snapshot series for the dashboard HW overlay (plan §14).
// Params: from, to, limit.
func (h *Handler) handleInsightsHWSamples(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    q := r.URL.Query()
    limit := 0
    if v, err := strconv.Atoi(q.Get("limit")); err == nil {
        limit = v
    }
    samples, err := h.insights.HWSamples(r.Context(), q.Get("from"), q.Get("to"), limit)
    if err != nil {
        slog.Warn("insights hw samples query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"samples": samples, "total": len(samples)})
}

// SetEventRecorder wires the fg_gateway_events audit sink for anomaly
// lifecycle mutations (ack/mute must leave an operator trail, plan §15.5).
// *pg.Sink satisfies insights.EventRecorder; nil (default) skips auditing.
func (h *Handler) SetEventRecorder(rec insights.EventRecorder) {
    h.eventRecorder = rec
}

// auditEvent records an admin mutation. Best-effort: a broken event sink must
// not fail the mutation itself.
func (h *Handler) auditEvent(level, event string, detail map[string]any) {
    if h.eventRecorder == nil {
        return
    }
    h.eventRecorder.RecordEvent(level, event, detail)
}

// handleAnomaliesList — GET /admin/api/anomalies (plan §15.5)
// Params: status (open|acked|muted), kind, backend, limit (default 100, cap 500).
func (h *Handler) handleAnomaliesList(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    q := r.URL.Query()
    limit := 0
    if v, err := strconv.Atoi(q.Get("limit")); err == nil {
        limit = v
    }
    anomalies, err := h.insights.Anomalies(r.Context(),
        q.Get("status"), q.Get("kind"), q.Get("backend"), limit)
    if err != nil {
        slog.Warn("anomalies list query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"anomalies": anomalies, "total": len(anomalies)})
}

// handleAnomalyByID — POST /admin/api/anomalies/{id}/ack | /{id}/mute
// ack body {"by": "..."}; mute body {"days": 7} (default 7, cap 90).
// Both land an fg_gateway_events audit row.
func (h *Handler) handleAnomalyByID(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    rest := strings.TrimPrefix(r.URL.Path, "/admin/api/anomalies/")
    parts := strings.Split(rest, "/")
    if len(parts) != 2 || parts[0] == "" {
        writeError(w, http.StatusBadRequest, "expected /admin/api/anomalies/{id}/ack or /mute")
        return
    }
    id, err := strconv.ParseInt(parts[0], 10, 64)
    if err != nil || id <= 0 {
        writeError(w, http.StatusBadRequest, "invalid anomaly id")
        return
    }
    actor := "admin"
    if claims := GetAdminClaims(r.Context()); claims != nil && claims.Username != "" {
        actor = claims.Username
    }
    switch parts[1] {
    case "ack":
        var req struct {
            By string `json:"by"`
        }
        if r.Body != nil {
            if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err.Error() != "EOF" {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
            }
        }
        if req.By != "" {
            actor = req.By
        }
        a, err := h.insights.AckAnomaly(r.Context(), id, actor)
        if err != nil {
            slog.Warn("anomaly ack failed", "id", id, "error", err)
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        h.auditEvent("info", "anomaly_acked", map[string]any{
            "id": id, "kind": a.Kind, "backend": a.Backend, "by": actor,
        })
        writeJSON(w, http.StatusOK, a)
    case "mute":
        var req struct {
            Days int `json:"days"`
        }
        if r.Body != nil {
            if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err.Error() != "EOF" {
                writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
                return
            }
        }
        days := req.Days
        if days <= 0 {
            days = 7
        }
        if days > 90 {
            days = 90
        }
        until := time.Now().UTC().AddDate(0, 0, days)
        a, err := h.insights.MuteAnomaly(r.Context(), id, until)
        if err != nil {
            slog.Warn("anomaly mute failed", "id", id, "error", err)
            writeError(w, http.StatusNotFound, err.Error())
            return
        }
        h.auditEvent("info", "anomaly_muted", map[string]any{
            "id": id, "kind": a.Kind, "backend": a.Backend,
            "days": days, "muted_until": until.Format(time.RFC3339),
        })
        writeJSON(w, http.StatusOK, a)
    default:
        writeError(w, http.StatusBadRequest, "unknown action: "+parts[1])
    }
}

// handleOptimizationFindingsList — GET /admin/api/optimization/findings
// (plan §15.5). Params: status (new|applied|dismissed), rule, limit.
func (h *Handler) handleOptimizationFindingsList(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    q := r.URL.Query()
    limit := 0
    if v, err := strconv.Atoi(q.Get("limit")); err == nil {
        limit = v
    }
    findings, err := h.insights.Findings(r.Context(),
        q.Get("status"), q.Get("rule"), limit)
    if err != nil {
        slog.Warn("optimization findings query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"findings": findings, "total": len(findings)})
}

// handleOptimizationFindingByID — POST /admin/api/optimization/findings/{id}/apply
// or /{id}/dismiss (plan §15.5). Apply is only implemented for
// MIGRATE_TO_LOCAL: it prepends fusion-mlx to models.supply.<model> (existing
// entries survive as fallback), reloads the live snapshot, and only then
// stamps the finding applied. Other rules are advisory — the operator acts
// outside the gateway or dismisses.
func (h *Handler) handleOptimizationFindingByID(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    rest := strings.TrimPrefix(r.URL.Path, "/admin/api/optimization/findings/")
    parts := strings.Split(rest, "/")
    if len(parts) != 2 || parts[0] == "" {
        writeError(w, http.StatusBadRequest,
            "expected /admin/api/optimization/findings/{id}/apply or /dismiss")
        return
    }
    id, err := strconv.ParseInt(parts[0], 10, 64)
    if err != nil || id <= 0 {
        writeError(w, http.StatusBadRequest, "invalid finding id")
        return
    }
    f, err := h.insights.FindingByID(r.Context(), id)
    if err != nil {
        writeError(w, http.StatusNotFound, err.Error())
        return
    }
    actor := "admin"
    if claims := GetAdminClaims(r.Context()); claims != nil && claims.Username != "" {
        actor = claims.Username
    }
    switch parts[1] {
    case "apply":
        h.applyFinding(w, r, f, id, actor)
    case "dismiss":
        dismissed, err := h.insights.DismissFinding(r.Context(), id)
        if err != nil {
            slog.Warn("finding dismiss failed", "id", id, "error", err)
            writeError(w, http.StatusConflict, err.Error())
            return
        }
        h.auditEvent("info", "finding_dismissed", map[string]any{
            "id": id, "rule": f.Rule, "scope": json.RawMessage(f.Scope), "by": actor,
        })
        writeJSON(w, http.StatusOK, dismissed)
    default:
        writeError(w, http.StatusBadRequest, "unknown action: "+parts[1])
    }
}

// applyFinding executes the MIGRATE_TO_LOCAL config mutation. Config write
// first, DB stamp second — a failed reload leaves the finding at 'new' so
// the operator can retry instead of a phantom applied row.
func (h *Handler) applyFinding(w http.ResponseWriter, r *http.Request, f *insights.Finding, id int64, actor string) {
    if f.Rule != insights.RuleMigrateToLocal {
        writeError(w, http.StatusBadRequest,
            "rule "+f.Rule+" is advisory: no machine-applicable config mutation, use dismiss")
        return
    }
    if f.Status != "new" {
        writeError(w, http.StatusConflict, "finding already "+f.Status)
        return
    }
    var scope struct {
        Model string `json:"model"`
    }
    if err := json.Unmarshal(f.Scope, &scope); err != nil || scope.Model == "" {
        writeError(w, http.StatusBadRequest, "finding scope has no model")
        return
    }
    model := scope.Model
    var supply []string
    if _, err := h.updateYAMLSection(func(doc map[string]interface{}) error {
        models := getOrCreateSection(doc, "models")
        supplySection := getOrCreateSection(models, "supply")
        supply = mergeSupply(supplySection[model], model)
        supplySection[model] = toIfaceSlice(supply)
        return nil
    }); err != nil {
        slog.Warn("finding apply config write failed", "id", id, "model", model, "error", err)
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    appliedCfg, _ := json.Marshal(map[string]any{
        "models.supply." + model: supply,
    })
    applied, err := h.insights.ApplyFinding(r.Context(), id, string(appliedCfg))
    if err != nil {
        // Config is live but the stamp failed — log loudly, do not roll back
        // (the supply entry is what the operator asked for; the row stays
        // 'new' and a retry converges).
        slog.Error("finding applied in config but DB stamp failed",
            "id", id, "model", model, "error", err)
        writeError(w, http.StatusInternalServerError,
            "config applied but finding stamp failed, retry apply to converge")
        return
    }
    h.auditEvent("info", "finding_applied", map[string]any{
        "id": id, "rule": f.Rule, "model": model,
        "supply": supply, "by": actor,
    })
    writeJSON(w, http.StatusOK, applied)
}

// handleReportsList — GET /admin/api/reports (plan §15.5)
// Params: period (daily|weekly), limit (default 50, cap 200). List rows omit
// content_md; the detail endpoint serves it.
func (h *Handler) handleReportsList(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    q := r.URL.Query()
    limit := 0
    if v, err := strconv.Atoi(q.Get("limit")); err == nil {
        limit = v
    }
    reports, err := h.insights.Reports(r.Context(), q.Get("period"), limit)
    if err != nil {
        slog.Warn("reports list query failed", "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"reports": reports, "total": len(reports)})
}

// handleReportByID — GET /admin/api/reports/{id} (plan §15.5). Serves the
// full row including content_md.
func (h *Handler) handleReportByID(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    rest := strings.TrimPrefix(r.URL.Path, "/admin/api/reports/")
    if rest == "" {
        writeError(w, http.StatusBadRequest, "missing report id")
        return
    }
    id, err := strconv.ParseInt(rest, 10, 64)
    if err != nil || id <= 0 {
        writeError(w, http.StatusBadRequest, "invalid report id")
        return
    }
    rep, err := h.insights.ReportByID(r.Context(), id)
    if err != nil {
        slog.Warn("report fetch failed", "id", id, "error", err)
        writeError(w, http.StatusNotFound, err.Error())
        return
    }
    writeJSON(w, http.StatusOK, rep)
}

// handleReportGenerate — POST /admin/api/reports/generate
// Body {"period": "daily"|"weekly"} (default daily). Manual trigger for the
// operator: generates the last complete window now instead of waiting for
// the 08:00 worker tick. Idempotent — an existing report is returned as-is.
func (h *Handler) handleReportGenerate(w http.ResponseWriter, r *http.Request) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    var req struct {
        Period string `json:"period"`
    }
    if r.Body != nil {
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err.Error() != "EOF" {
            writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
            return
        }
    }
    if req.Period == "" {
        req.Period = "daily"
    }
    rep, err := h.insights.GenerateReport(r.Context(), req.Period)
    if err != nil {
        slog.Warn("report generation failed", "period", req.Period, "error", err)
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }
    // Same file copy the worker pass makes, so a forced report lands on disk
    // immediately (no-op when the reports worker never started).
    h.insights.WriteReportFile(rep)
    h.auditEvent("info", "report_generated", map[string]any{
        "period": rep.Period, "id": rep.ID, "by": "admin",
    })
    writeJSON(w, http.StatusOK, rep)
}

// toIfaceSlice converts []string to []interface{} for the YAML doc tree.
func toIfaceSlice(in []string) []interface{} {
    out := make([]interface{}, len(in))
    for i, s := range in {
        out[i] = s
    }
    return out
}

// mergeSupply builds the new models.supply list for a MIGRATE_TO_LOCAL apply:
// fusion-mlx first, then the model's existing entries (minus fusion-mlx) as
// fallback. A missing or non-list existing entry yields just fusion-mlx.
func mergeSupply(existing any, model string) []string {
    supply := []string{"fusion-mlx"}
    if list, ok := existing.([]interface{}); ok {
        for _, e := range list {
            if es, _ := e.(string); es != "" && es != "fusion-mlx" {
                supply = append(supply, es)
            }
        }
    }
    return supply
}
