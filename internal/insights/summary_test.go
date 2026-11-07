package insights

import (
    "context"
    "fmt"
    "testing"
    "time"
)

// TestSummary_DimensionWhitelist pins the group-by dimension guard: only the
// whitelisted columns reach the SQL splice, everything else errors (never
// interpolate caller input into SQL).
func TestSummary_DimensionWhitelist(t *testing.T) {
    for _, d := range []string{"model", "backend", "endpoint", "api_key_name", "scenario"} {
        if _, ok := summaryColumn(d); !ok {
            t.Fatalf("dimension %s must be whitelisted", d)
        }
    }
    for _, d := range []string{"ts", "id; DROP TABLE x", "model; --", "password"} {
        if col, ok := summaryColumn(d); ok {
            t.Fatalf("dimension %q must be rejected, got column %q", d, col)
        }
    }
    // Empty dimension falls back to model (dashboard default).
    if col, ok := summaryColumn(""); !ok || col != "model" {
        t.Fatalf("empty dimension must default to model, got %q ok=%v", col, ok)
    }
}

// TestSQL_Summary seeds rows across two models and two backends and asserts
// the group-by aggregation with read-time pricing. Requires
// FUSION_GW_INSIGHTS_TEST_DSN.
func TestSQL_Summary(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    tag := fmt.Sprintf("test-sum-%d", time.Now().UnixNano())
    defer func() {
        if _, err := svc.pool.Exec(ctx, `DELETE FROM fg_request_logs WHERE scenario = $1`, tag); err != nil {
            t.Logf("cleanup: %v", err)
        }
    }()
    svc.SetPricing(map[string]ModelPrice{"glm-5": {InputPerMTok: 1, OutputPerMTok: 2}})
    defer svc.SetPricing(nil)
    base := time.Now().UTC().Add(-5 * time.Minute)
    type row struct {
        endpoint string
        status   int
        model    string
        backend  string
        key      string
    }
    seed := []row{
        {"/v1/chat/completions", 200, "glm-5", "cloud", "team-a"},
        {"/v1/chat/completions", 200, "glm-5", "cloud", "team-a"},
        {"/v1/chat/completions", 500, "glm-5", "cloud", "team-a"},
        {"/v1/chat/completions", 200, "qwen3", "local", "team-b"},
        {"/admin/api/config/reload", 200, "", "local", ""},
    }
    for i, s := range seed {
        if err := seedRequestLogFull(ctx, svc, base.Add(time.Duration(i)*time.Second), tag, s.endpoint, s.status, s.model, s.backend, s.key, 1_000_000, 500_000); err != nil {
            t.Fatalf("seed row: %v", err)
        }
    }
    from := base.Add(-time.Minute).Format(time.RFC3339)
    to := base.Add(5 * time.Minute).Format(time.RFC3339)
    // By model: glm-5 = 3 reqs (1 err), cost 3M*1 + 1.5M*2 = 6; qwen3 stored.
    rows, err := svc.Summary(ctx, from, to, "model", "llm", 10)
    if err != nil {
        t.Fatalf("summary by model: %v", err)
    }
    if len(rows) != 2 {
        t.Fatalf("rows = %d, want 2 (LLM-scoped models)", len(rows))
    }
    if rows[0].Name != "glm-5" || rows[0].Requests != 3 || rows[0].Errors != 1 {
        t.Fatalf("top row = %+v, want glm-5 3 reqs 1 err", rows[0])
    }
    if rows[0].Cost < 5.999 || rows[0].Cost > 6.001 {
        t.Fatalf("glm-5 cost = %v, want 6", rows[0].Cost)
    }
    if rows[0].SuccessRate < 0.665 || rows[0].SuccessRate > 0.668 {
        t.Fatalf("glm-5 success rate = %v, want 2/3", rows[0].SuccessRate)
    }
    // By backend: cloud 3, local 1.
    rowsB, err := svc.Summary(ctx, from, to, "backend", "llm", 10)
    if err != nil {
        t.Fatalf("summary by backend: %v", err)
    }
    if len(rowsB) != 2 || rowsB[0].Name != "cloud" || rowsB[0].Requests != 3 {
        t.Fatalf("backend rows = %+v, want cloud 3 first", rowsB)
    }
}

// TestSQL_Rescued seeds breaker_tripped + cloud_overflow events plus noise
// and asserts the intervention counter. Requires FUSION_GW_INSIGHTS_TEST_DSN.
func TestSQL_Rescued(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    tag := fmt.Sprintf("resc-%d", time.Now().UnixNano())
    defer func() {
        if _, err := svc.pool.Exec(ctx, `DELETE FROM fg_gateway_events WHERE detail::text LIKE '%' || $1 || '%'`, tag); err != nil {
            t.Logf("cleanup: %v", err)
        }
    }()
    now := time.Now().UTC()
    for _, e := range []struct {
        event string
        level string
    }{
        {"breaker_tripped", "warn"},
        {"breaker_tripped", "warn"},
        {"cloud_overflow", "warn"},
        {"gateway_started", "info"},
    } {
        if _, err := svc.pool.Exec(ctx,
            `INSERT INTO fg_gateway_events (ts, level, event, detail) VALUES ($1, $2, $3, jsonb_build_object('tag', $4::text))`,
            now, e.level, e.event, tag); err != nil {
            t.Fatalf("seed event: %v", err)
        }
    }
    from := now.Add(-time.Minute).Format(time.RFC3339)
    to := now.Add(time.Minute).Format(time.RFC3339)
    r, err := svc.Rescued(ctx, from, to)
    if err != nil {
        t.Fatalf("rescued: %v", err)
    }
    if r.BreakerTrips < 2 || r.CloudOverflows < 1 || r.Total < 3 {
        t.Fatalf("rescued = %+v, want >= 2 breaker + 1 overflow", r)
    }
}
