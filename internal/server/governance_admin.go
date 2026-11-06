package server

// Governance report endpoint (LiteLLM-gap dashboard wave): one admin surface
// answering "who is spending, what got blocked, what fired". Budget rows come
// from the try-quota meter (in-memory), PII daily counts / 429-by-key counts
// from the PG request logs, and the recent governance events (budget
// soft-limit, cloud cooldown arm/clear, cloud overflow) from the structured
// event stream. Registered next to the try-quota admin routes with
// withAdminOnly. Every PG-backed section degrades to an empty array when the
// insights read layer is absent (persistence off) — the report must never
// fail because a data source is disabled.

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/persist/pg"
)

// handleGovernanceReport GET /admin/api/reports/governance — spend and
// governance rollup for the admin dashboard.
func (s *Server) handleGovernanceReport(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    resp := map[string]any{
        "generated_at": time.Now().UTC().Format(time.RFC3339),
        "budgets":      []any{},
        "pii_daily":    []any{},
        "rejects_by_key": []any{},
        "events":       []any{},
    }

    if s.tryQuotaMeter != nil {
        resp["budgets"] = s.tryQuotaMeter.Usage(time.Now(), func(sub string) int {
            budget := s.tryQuotaConfigSource().Budget
            if b, ok := s.tryQuotaMeter.BudgetOverride(sub); ok {
                return b
            }
            return budget
        })
    }

    if s.insightsSvc != nil {
        ctx := r.Context()
        if rows, err := s.insightsSvc.PIIDaily(ctx, 14); err != nil {
            slog.Warn("governance report: pii daily query failed", "error", err)
        } else {
            resp["pii_daily"] = rows
        }
        if rows, err := s.insightsSvc.RejectsByKey(ctx, 7); err != nil {
            slog.Warn("governance report: rejects by key query failed", "error", err)
        } else {
            resp["rejects_by_key"] = rows
        }
        if events, err := s.insightsSvc.Events(ctx, "", "", 50); err != nil {
            slog.Warn("governance report: events query failed", "error", err)
        } else {
            resp["events"] = events
        }
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    if err := json.NewEncoder(w).Encode(resp); err != nil {
        slog.Warn("governance report: encode failed", "error", err)
    }
}

// recordGovernanceEvent lands a governance event (budget soft-limit, cloud
// cooldown arm/clear) in fg_gateway_events via the persistence sink. No-op
// (log only) when persistence is off. Same contract as
// recordCloudOverflowEvent — this is the queryable "why did this fire"
// surface for the dashboard.
func (s *Server) recordGovernanceEvent(level, event string, detail map[string]any) {
    if s.persistSink != nil {
        if rec, ok := s.persistSink.(pg.EventRecorder); ok {
            rec.RecordEvent(level, event, detail)
        }
    }
    slog.Info("governance event", "level", level, "event", event, "detail", detail)
}
