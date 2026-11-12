package server

// #633: billing rate-plan admin surface. GET /admin/api/billing/summary
// returns the configured list rate, the prepaid bucket catalog, per-key
// balances, and the billable-vs-cost rollup (margin view) from the cost
// tracker ring. POST /admin/api/billing/topup funds a key's prepaid balance —
// the manual executor until fusion-identity ships an order plane (upstream
// issue filed); order_ref is a free-form reference recorded on the balance.

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func (s *Server) handleBillingSummary(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    snap := config.SnapshotFromContext(r.Context())
    if snap == nil {
        snap = s.cfg
    }
    billingCfg := snap.Config.Billing
    resp := map[string]any{
        "enabled":                   billingCfg.Enabled,
        "list_price_per_1k_usd":     billingCfg.ListPricePer1KTokensUSD,
        "prepaid_buckets":           billingCfg.PrepaidBuckets,
        "balances":                  s.billingLedger.Snapshot(),
    }
    if s.costTracker != nil {
        sum := s.costTracker.Summary()
        resp["billable_vs_cost"] = map[string]any{
            "total_cost_usd":     sum.TotalCostUSD,
            "total_billable_usd": sum.TotalBillableUSD,
            "by_key_cost":        sum.ByKey,
            "by_key_billable":    sum.ByKeyBillable,
            "total_tokens":       sum.TotalTokens,
            "total_requests":     sum.TotalRequests,
        }
    }
    writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBillingTopUp(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodySize)
    var req struct {
        Key     string  `json:"key"`
        Tokens  float64 `json:"tokens"`
        OrderRef string `json:"order_ref"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    req.Key = strings.TrimSpace(req.Key)
    if req.Key == "" || req.Tokens <= 0 {
        http.Error(w, `{"error":{"message":"key and positive tokens required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    // Fail-closed on unknown keys: topping up a typo'd key name would mint
    // invisible credit. The key must already exist (store or config keys).
    if !s.keyNameExists(req.Key) {
        slog.Warn("billing topup rejected: unknown key", "key", req.Key)
        http.Error(w, `{"error":{"message":"unknown key","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    balance, err := s.billingLedger.TopUp(req.Key, req.Tokens, req.OrderRef)
    if err != nil {
        http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    slog.Info("billing topup applied", "key", req.Key, "tokens", req.Tokens, "order_ref", req.OrderRef, "balance", balance)
    writeJSON(w, http.StatusOK, map[string]any{
        "key":            req.Key,
        "balance_tokens": balance,
    })
}

// keyNameExists checks the in-memory key store for an ACTIVE key with this
// display name (config keys live in s.cfg, store keys in s.store).
func (s *Server) keyNameExists(name string) bool {
    if keys, err := s.store.ListKeys(); err == nil {
        for _, k := range keys {
            if k.Name == name && k.Status == "active" {
                return true
            }
        }
    }
    for _, k := range s.cfg.Config.Auth.APIKeys {
        if k.Name == name {
            return true
        }
    }
    return false
}
