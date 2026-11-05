package server

// #514: admin endpoints for the try-quota meter. Registered on the gateway
// mux inside the Admin.Enabled block (before the /admin/ SPA catch-all) with
// withAdminOnly — same protection tier as the /admin/orgs routes.

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "time"
)

// handleTryQuotaUsage GET /admin/api/try-quota/usage — per-sub usage rows
// (day rollup, budget, override, recent calls). Empty (no meter) → [].
func (s *Server) handleTryQuotaUsage(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    if s.tryQuotaMeter == nil {
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode([]any{})
        return
    }
    rows := s.tryQuotaMeter.Usage(time.Now(), func(sub string) int {
        _, budget, _, _ := s.tryQuotaConfigSource()
        if b, ok := s.tryQuotaMeter.BudgetOverride(sub); ok {
            return b
        }
        return budget
    })
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(rows)
}

// handleTryQuotaBudget POST /admin/api/try-quota/budget — set or clear a
// per-user daily budget override. Body: {"sub": "...", "daily_budget": N}
// (N <= 0 clears the override, falling back to try_quota.daily_token_budget).
func (s *Server) handleTryQuotaBudget(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    if s.tryQuotaMeter == nil {
        http.Error(w, "try-quota meter not enabled", http.StatusServiceUnavailable)
        return
    }
    var req struct {
        Sub         string `json:"sub"`
        DailyBudget int    `json:"daily_budget"`
    }
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
        http.Error(w, "invalid request body", http.StatusBadRequest)
        return
    }
    if req.Sub == "" {
        http.Error(w, "sub is required", http.StatusBadRequest)
        return
    }
    s.tryQuotaMeter.SetBudgetOverride(req.Sub, req.DailyBudget)
    slog.Info("try-quota: budget override set", "sub", req.Sub, "daily_budget", req.DailyBudget)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(map[string]any{
        "sub":          req.Sub,
        "daily_budget": req.DailyBudget,
        "cleared":      req.DailyBudget <= 0,
    })
}
