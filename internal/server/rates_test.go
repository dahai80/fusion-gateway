package server

// #659 regression: GET /v1/rates publishes the billing rate plan (#633) as
// stable versioned JSON for first-party surfaces (fusion-site top-up panel).
// Public route — no auth middleware; 404 while billing is disabled so
// consumers fall back to placeholder copy.

import (
    "encoding/json"
    "net/http/httptest"
    "os"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func Test659_Rates_NotPublishedWhenBillingDisabled(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest("GET", "/v1/rates", nil)
    rec := httptest.NewRecorder()
    s.handleRates(rec, req)
    if rec.Code != 404 {
        t.Fatalf("billing disabled: want 404, got %d body=%s", rec.Code, rec.Body.String())
    }
}

func Test659_Rates_PublishedPlanShape(t *testing.T) {
    s := newTestServer()
    // GetSnapshot returns a fresh DefaultConfig until a Load/Reload seeds the
    // global — publish the plan through the real reload path instead of
    // mutating a throwaway copy.
    dir := t.TempDir()
    cfgPath := dir + "/config.yaml"
    planYAML := `
server:
  port: 11432
billing:
  enabled: true
  list_price_per_1k_tokens_usd: 0.05
  prepaid_buckets:
    - name: starter
      tokens: 100000
      price_usd: 4.9
    - name: pro
      tokens: 500000
      price_usd: 19.9
`
    if err := os.WriteFile(cfgPath, []byte(planYAML), 0600); err != nil {
        t.Fatal(err)
    }
    snap, err := config.Reload(cfgPath)
    if err != nil {
        t.Fatalf("reload with billing plan failed: %v", err)
    }
    t.Cleanup(func() { snap.Config.Billing.Enabled = false })

    req := httptest.NewRequest("GET", "/v1/rates", nil)
    rec := httptest.NewRecorder()
    s.handleRates(rec, req)
    if rec.Code != 200 {
        t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    var plan map[string]any
    if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
        t.Fatalf("response not json: %v", err)
    }
    if plan["schema"] != "fusion.rates.v1" {
        t.Fatalf("missing versioned schema field: %v", plan["schema"])
    }
    if plan["list_price_per_1k_tokens_usd"] != 0.05 {
        t.Fatalf("list price wrong: %v", plan["list_price_per_1k_tokens_usd"])
    }
    tiers, _ := plan["tiers"].([]any)
    if len(tiers) != 2 {
        t.Fatalf("want 2 tiers, got %d", len(tiers))
    }
    t0 := tiers[0].(map[string]any)
    if t0["name"] != "starter" || t0["tokens"] != float64(100000) || t0["price_usd"] != 4.9 {
        t.Fatalf("tier[0] wrong: %v", t0)
    }
    if plan["updated_at"] == "" || plan["config_version"] == nil {
        t.Fatalf("updated_at/config_version missing: %v", plan)
    }
    if rec.Header().Get("Cache-Control") == "" {
        t.Fatal("Cache-Control header missing")
    }
}
