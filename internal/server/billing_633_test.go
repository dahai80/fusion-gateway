package server

// #633: billing rate-plan wiring — tracker billable amounts, hot-path gate
// (429 at exhaustion), finalize deduction, and the admin summary surface.

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/billing"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func Test633_TrackerBillableDistinctFromCost(t *testing.T) {
    tr := cost.NewTracker(10)
    tr.SyncConfigBilling(true, 2.0) // $2 per 1K tokens
    tr.Record("key-a", "local", "m1", 500, 500)
    tr.SyncConfigBilling(false, 2.0) // disabled mid-stream
    tr.Record("key-a", "local", "m1", 1000, 1000)

    s := tr.Summary()
    if s.TotalBillableUSD != 2.0 {
        t.Fatalf("total billable = %v, want 2.0 (1K tokens at $2/1K)", s.TotalBillableUSD)
    }
    if s.ByKeyBillable["key-a"] != 2.0 {
        t.Fatalf("by-key billable = %v", s.ByKeyBillable["key-a"])
    }
    kb := tr.SummaryByKey("key-a")
    if kb.TotalBillableUSD != 2.0 {
        t.Fatalf("key summary billable = %v", kb.TotalBillableUSD)
    }
}

func Test633_BillingGateBlocksExhaustedKey(t *testing.T) {
    s := newTestServer()
    s.billingLedger = billing.NewLedger(t.TempDir())
    _, _ = s.billingLedger.TopUp("empty-key", 10, "")
    s.billingLedger.Deduct("empty-key", 10)
    s.cfg.Config.Billing.Enabled = true

    h := s.billingGate()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), &middleware.Principal{
        AuthMethod: "api-key",
        KeyConfig:  &config.AuthKeyConfig{Name: "empty-key"},
    }))
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("exhausted key = %d, want 429", rec.Code)
    }
    if !strings.Contains(rec.Body.String(), "BILLING_EXHAUSTED") {
        t.Fatalf("body missing code: %s", rec.Body.String())
    }
}

func Test633_BillingGatePassesFundedAndNonPrepaid(t *testing.T) {
    s := newTestServer()
    s.billingLedger = billing.NewLedger(t.TempDir())
    _, _ = s.billingLedger.TopUp("funded", 500, "")
    s.cfg.Config.Billing.Enabled = true

    h := s.billingGate()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    mk := func(name string) *http.Request {
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        return req.WithContext(middleware.ContextWithPrincipal(req.Context(), &middleware.Principal{
            AuthMethod: "api-key",
            KeyConfig:  &config.AuthKeyConfig{Name: name},
        }))
    }
    run := func(name string) *httptest.ResponseRecorder {
        rec := httptest.NewRecorder()
        h.ServeHTTP(rec, mk(name))
        return rec
    }
    if rec := run("funded"); rec.Code != 200 {
        t.Fatalf("funded key blocked: %d", rec.Code)
    }
    if rec := run("no-bucket"); rec.Code != 200 {
        t.Fatalf("non-prepaid key blocked: %d", rec.Code)
    }
    // Disabled billing = no enforcement even for empty balances.
    s.cfg.Config.Billing.Enabled = false
    _, _ = s.billingLedger.TopUp("zeroed", 1, "")
    s.billingLedger.Deduct("zeroed", 1)
    if rec := run("zeroed"); rec.Code != 200 {
        t.Fatalf("disabled billing still enforced: %d", rec.Code)
    }
}

func Test633_DeductBillingUsesEntryTokens(t *testing.T) {
    s := newTestServer()
    s.billingLedger = billing.NewLedger(t.TempDir())
    _, _ = s.billingLedger.TopUp("k1", 1000, "")
    s.deductBilling(&store.RequestLog{APIKeyName: "k1", InputTokens: 300, OutputTokens: 200})
    if s.billingLedger.Balance("k1") != 500 {
        t.Fatalf("balance = %v, want 500", s.billingLedger.Balance("k1"))
    }
    s.deductBilling(&store.RequestLog{APIKeyName: "k1"}) // zero tokens: no-op
    if s.billingLedger.Balance("k1") != 500 {
        t.Fatalf("zero-token deduct changed balance: %v", s.billingLedger.Balance("k1"))
    }
}

func Test633_AdminTopUpAndSummary(t *testing.T) {
    s := newTestServer()
    s.billingLedger = billing.NewLedger(t.TempDir())
    s.costTracker = cost.NewTracker(10)
    s.cfg.Config.Billing.Enabled = true
    s.cfg.Config.Billing.ListPricePer1KTokensUSD = 2.5
    s.cfg.Config.Auth.APIKeys = append(s.cfg.Config.Auth.APIKeys, config.AuthKeyConfig{Name: "bucket-key"})

    // Top-up rejects unknown keys (fail-closed: no invisible credit).
    rec := httptest.NewRecorder()
    s.handleBillingTopUp(rec, adminJSONRequest633(t, map[string]any{"key": "typo-key", "tokens": 100}))
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("unknown-key topup = %d, want 400", rec.Code)
    }

    rec = httptest.NewRecorder()
    s.handleBillingTopUp(rec, adminJSONRequest633(t, map[string]any{"key": "bucket-key", "tokens": 1000, "order_ref": "ord-7"}))
    if rec.Code != http.StatusOK {
        t.Fatalf("topup = %d body=%s", rec.Code, rec.Body.String())
    }

    rec = httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/admin/api/billing/summary", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    s.handleBillingSummary(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("summary = %d", rec.Code)
    }
    var resp struct {
        Enabled               bool             `json:"enabled"`
        ListPricePer1KUSD     float64          `json:"list_price_per_1k_usd"`
        Balances              map[string]any   `json:"balances"`
        BillableVsCost        map[string]any   `json:"billable_vs_cost"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatal(err)
    }
    if !resp.Enabled || resp.ListPricePer1KUSD != 2.5 {
        t.Fatalf("rate not surfaced: %v %v", resp.Enabled, resp.ListPricePer1KUSD)
    }
    if _, ok := resp.Balances["bucket-key"]; !ok {
        t.Fatalf("balance missing from summary: %v", resp.Balances)
    }
    if resp.BillableVsCost == nil {
        t.Fatal("billable_vs_cost missing")
    }
}

func adminJSONRequest633(t *testing.T, body map[string]any) *http.Request {
    t.Helper()
    b, _ := json.Marshal(body)
    return httptest.NewRequest(http.MethodPost, "/admin/api/billing/topup", strings.NewReader(string(b)))
}
