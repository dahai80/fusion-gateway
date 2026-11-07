package insights

import (
    "context"
    "fmt"
    "testing"
    "time"
)

// TestSQL_ErrorBreakdown seeds LLM + non-LLM error rows and asserts the
// breakdown: LLM-scoped by default (admin/proxy rows model='' excluded),
// grouped endpoint × status × message, count desc, limit respected.
// Requires FUSION_GW_INSIGHTS_TEST_DSN (same skip rule as the other
// TestSQL_* cases) — unit tests cannot catch broken SQL.
func TestSQL_ErrorBreakdown(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    tag := fmt.Sprintf("test-err-%d", time.Now().UnixNano())
    defer func() {
        if _, err := svc.pool.Exec(ctx, `DELETE FROM fg_request_logs WHERE scenario = $1`, tag); err != nil {
            t.Logf("cleanup: %v", err)
        }
    }()
    base := time.Now().UTC().Add(-5 * time.Minute)
    seed := []struct {
        endpoint string
        status   int
        errMsg   string
        model    string
    }{
        {"/v1/chat/completions", 502, "upstream connection refused", "glm-5"},
        {"/v1/chat/completions", 502, "upstream connection refused", "glm-5"},
        {"/v1/chat/completions", 502, "upstream connection refused", "glm-5"},
        {"/v1/chat/completions", 429, "quota exceeded", "glm-5"},
        {"/v1/messages", 500, "model load failed", "qwen3"},
        {"/admin/api/config/reload", 500, "internal error", ""},
    }
    for i, s := range seed {
        if err := seedRequestLog(ctx, svc, base.Add(time.Duration(i)*time.Second), tag, s.endpoint, s.status, s.errMsg, s.model); err != nil {
            t.Fatalf("seed row: %v", err)
        }
    }
    from := base.Add(-time.Minute).Format(time.RFC3339)
    to := base.Add(5 * time.Minute).Format(time.RFC3339)
    rows, err := svc.ErrorBreakdown(ctx, from, to, "llm", 10)
    if err != nil {
        t.Fatalf("error breakdown: %v", err)
    }
    if len(rows) != 3 {
        t.Fatalf("rows = %d, want 3 (LLM-scoped groups)", len(rows))
    }
    if rows[0].Count != 3 || rows[0].Endpoint != "/v1/chat/completions" || rows[0].StatusCode != 502 {
        t.Fatalf("top row = %+v, want chat/completions 502 x3", rows[0])
    }
    // Scope=all includes the admin row.
    rowsAll, err := svc.ErrorBreakdown(ctx, from, to, "all", 10)
    if err != nil {
        t.Fatalf("error breakdown all: %v", err)
    }
    if len(rowsAll) != 4 {
        t.Fatalf("rows(all) = %d, want 4", len(rowsAll))
    }
    // Limit.
    rowsLim, err := svc.ErrorBreakdown(ctx, from, to, "llm", 2)
    if err != nil {
        t.Fatalf("error breakdown limit: %v", err)
    }
    if len(rowsLim) != 2 {
        t.Fatalf("rows(limit 2) = %d, want 2", len(rowsLim))
    }
}

// TestSQL_SpendByKey seeds two keys on LLM endpoints plus one non-LLM row
// and asserts per-key request/token/cost/success aggregation with pricing
// applied. Requires FUSION_GW_INSIGHTS_TEST_DSN.
func TestSQL_SpendByKey(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    tag := fmt.Sprintf("test-spend-%d", time.Now().UnixNano())
    defer func() {
        if _, err := svc.pool.Exec(ctx, `DELETE FROM fg_request_logs WHERE scenario = $1`, tag); err != nil {
            t.Logf("cleanup: %v", err)
        }
    }()
    svc.SetPricing(map[string]ModelPrice{"glm-5": {InputPerMTok: 1, OutputPerMTok: 2}})
    defer svc.SetPricing(nil)
    base := time.Now().UTC().Add(-5 * time.Minute)
    type row struct {
        key    string
        status int
        in     int64
        out    int64
    }
    seed := []row{
        {"team-a", 200, 1_000_000, 1_000_000},
        {"team-a", 200, 2_000_000, 500_000},
        {"team-b", 200, 500_000, 0},
        {"team-b", 500, 100_000, 0},
        {"", 200, 9_999, 9_999}, // non-LLM route (model='')
    }
    for i, s := range seed {
        model := "glm-5"
        endpoint := "/v1/chat/completions"
        if s.key == "" {
            model = ""
            endpoint = "/admin/api/config/reload"
        }
        if err := seedRequestLogTokens(ctx, svc, base.Add(time.Duration(i)*time.Second), tag, endpoint, s.status, model, s.key, s.in, s.out); err != nil {
            t.Fatalf("seed row: %v", err)
        }
    }
    from := base.Add(-time.Minute).Format(time.RFC3339)
    to := base.Add(5 * time.Minute).Format(time.RFC3339)
    rows, err := svc.SpendByKey(ctx, from, to, "llm", 10)
    if err != nil {
        t.Fatalf("spend by key: %v", err)
    }
    if len(rows) != 2 {
        t.Fatalf("rows = %d, want 2 (LLM-scoped keys)", len(rows))
    }
    // Sorted by cost desc: team-a = 1M*1 + 1M*2 + 2M*1 + 0.5M*2 = 6 USD.
    if rows[0].KeyName != "team-a" {
        t.Fatalf("top key = %q, want team-a", rows[0].KeyName)
    }
    if rows[0].Requests != 2 || rows[0].Errors != 0 {
        t.Fatalf("team-a req=%d err=%d, want 2/0", rows[0].Requests, rows[0].Errors)
    }
    if rows[0].Cost < 5.999 || rows[0].Cost > 6.001 {
        t.Fatalf("team-a cost = %v, want 6", rows[0].Cost)
    }
    // team-b: 0.5M in priced = 0.5; success rate 1/2.
    if rows[1].KeyName != "team-b" || rows[1].Errors != 1 {
        t.Fatalf("team-b row = %+v, want 1 error", rows[1])
    }
    if rows[1].SuccessRate < 0.499 || rows[1].SuccessRate > 0.501 {
        t.Fatalf("team-b success rate = %v, want 0.5", rows[1].SuccessRate)
    }
}
