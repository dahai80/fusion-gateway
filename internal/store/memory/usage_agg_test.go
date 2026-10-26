package memory

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// #326: per-(app, scenario, model) cost aggregation.
func TestLogStore_Aggregate(t *testing.T) {
    ls := NewLogStore(100)
    base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

    addLog := func(offMinutes int, app, scenario, model string, in, out int, cost float64) {
        if err := ls.Append(&store.RequestLog{
            Timestamp:   base.Add(time.Duration(offMinutes) * time.Minute),
            APIKeyName:  app,
            Scenario:    scenario,
            Model:       model,
            InputTokens: in, OutputTokens: out, TotalTokens: in + out,
            CostUSD: cost,
        }); err != nil {
            t.Fatalf("Append failed: %v", err)
        }
    }
    addLog(0, "app-a", "question-gen", "glm-5", 100, 50, 0.10)
    addLog(1, "app-a", "question-gen", "glm-5", 200, 100, 0.20)
    addLog(2, "app-a", "oral-scoring", "glm-5", 50, 25, 0.05)
    addLog(3, "app-b", "question-gen", "glm-5", 10, 5, 0.01)
    addLog(4, "app-b", "", "glm-5", 10, 5, 0.02)

    from := base.Add(-time.Hour)
    to := base.Add(time.Hour)

    // No filters: one group per distinct (app, scenario, model).
    rows := ls.Aggregate(from, to, "", "", "")
    if len(rows) != 4 {
        t.Fatalf("expected 4 groups, got %d", len(rows))
    }
    // Sorted by cost desc: question-gen glm-5 app-a (0.30) first.
    if rows[0].App != "app-a" || rows[0].Scenario != "question-gen" || rows[0].Model != "glm-5" {
        t.Errorf("unexpected top group: %+v", rows[0])
    }
    if rows[0].RequestCount != 2 || rows[0].InputTokens != 300 || rows[0].OutputTokens != 150 {
        t.Errorf("unexpected sums: %+v", rows[0])
    }
    if rows[0].TotalTokens != 450 {
        t.Errorf("expected total 450, got %d", rows[0].TotalTokens)
    }
    if rows[0].CostUSD < 0.299 || rows[0].CostUSD > 0.301 {
        t.Errorf("expected cost ~0.30, got %f", rows[0].CostUSD)
    }

    // app filter
    rows = ls.Aggregate(from, to, "app-a", "", "")
    if len(rows) != 2 {
        t.Fatalf("expected 2 groups for app-a, got %d", len(rows))
    }
    for _, r := range rows {
        if r.App != "app-a" {
            t.Errorf("unexpected app in filtered rows: %s", r.App)
        }
    }

    // scenario filter
    rows = ls.Aggregate(from, to, "", "oral-scoring", "")
    if len(rows) != 1 || rows[0].CostUSD < 0.049 || rows[0].CostUSD > 0.051 {
        t.Errorf("unexpected oral-scoring rows: %+v", rows)
    }

    // model filter
    rows = ls.Aggregate(from, to, "", "", "other-model")
    if len(rows) != 0 {
        t.Errorf("expected 0 rows for other-model, got %d", len(rows))
    }

    // time window excludes everything
    rows = ls.Aggregate(base.Add(24*time.Hour), base.Add(25*time.Hour), "", "", "")
    if len(rows) != 0 {
        t.Errorf("expected 0 rows outside window, got %d", len(rows))
    }
}

// #326: LogFilter.Scenario passthrough in Query.
func TestLogStore_FilterScenario(t *testing.T) {
    ls := NewLogStore(100)

    if err := ls.Append(&store.RequestLog{Model: "glm-5", Scenario: "question-gen"}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }
    if err := ls.Append(&store.RequestLog{Model: "glm-5", Scenario: "oral-scoring"}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }

    logs, total, err := ls.Query(store.LogFilter{Scenario: "oral-scoring", Page: 1, PageSize: 10})
    if err != nil {
        t.Fatalf("Query failed: %v", err)
    }
    if total != 1 || len(logs) != 1 {
        t.Fatalf("expected 1 log, got total=%d len=%d", total, len(logs))
    }
    if logs[0].Scenario != "oral-scoring" {
        t.Errorf("unexpected scenario: %s", logs[0].Scenario)
    }
}
