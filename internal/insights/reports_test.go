package insights

import (
    "strings"
    "testing"
    "time"
)

// fixed location so window math is deterministic regardless of host TZ.
var reportLoc = time.FixedZone("CST", 8*3600)

// reportNow: Wednesday 2026-10-28 10:30 local — past 08:00, mid-week.
var reportNow = time.Date(2026, 10, 28, 10, 30, 0, 0, reportLoc)

func TestReportWindow_Daily(t *testing.T) {
    start, end, _ := reportWindow("daily", reportNow)
    wantStart := time.Date(2026, 10, 27, 0, 0, 0, 0, reportLoc)
    wantEnd := time.Date(2026, 10, 28, 0, 0, 0, 0, reportLoc)
    if !start.Equal(wantStart) || !end.Equal(wantEnd) {
        t.Fatalf("daily window = %s..%s, want %s..%s", start, end, wantStart, wantEnd)
    }
}

func TestReportWindow_WeeklyMidWeek(t *testing.T) {
    start, end, wd := reportWindow("weekly", reportNow)
    // Last complete Mon..Sun: Mon 2026-10-19 00:00 → Mon 2026-10-26 00:00.
    wantStart := time.Date(2026, 10, 19, 0, 0, 0, 0, reportLoc)
    wantEnd := time.Date(2026, 10, 26, 0, 0, 0, 0, reportLoc)
    if !start.Equal(wantStart) || !end.Equal(wantEnd) {
        t.Fatalf("weekly window = %s..%s, want %s..%s", start, end, wantStart, wantEnd)
    }
    if wd != time.Monday {
        t.Fatalf("weekly window end weekday = %s, want Monday", wd)
    }
}

func TestReportWindow_WeeklyOnMonday(t *testing.T) {
    // Monday 2026-10-26: the window that just closed is Mon 10-19 → Mon 10-26.
    now := time.Date(2026, 10, 26, 9, 0, 0, 0, reportLoc)
    start, end, _ := reportWindow("weekly", now)
    wantStart := time.Date(2026, 10, 19, 0, 0, 0, 0, reportLoc)
    wantEnd := time.Date(2026, 10, 26, 0, 0, 0, 0, reportLoc)
    if !start.Equal(wantStart) || !end.Equal(wantEnd) {
        t.Fatalf("weekly window = %s..%s, want %s..%s", start, end, wantStart, wantEnd)
    }
}

func TestDueReports(t *testing.T) {
    // Wednesday 10:30 — daily only.
    got := dueReports(reportNow)
    if len(got) != 1 || got[0] != "daily" {
        t.Fatalf("mid-week due = %v, want [daily]", got)
    }
    // Monday 10:30 — daily + weekly.
    now := time.Date(2026, 10, 26, 10, 30, 0, 0, reportLoc)
    got = dueReports(now)
    if len(got) != 2 || got[0] != "daily" || got[1] != "weekly" {
        t.Fatalf("monday due = %v, want [daily weekly]", got)
    }
    // Before 08:00 — nothing due, even on Monday.
    now = time.Date(2026, 10, 26, 7, 59, 0, 0, reportLoc)
    if got = dueReports(now); len(got) != 0 {
        t.Fatalf("pre-0800 due = %v, want none", got)
    }
}

func TestPctDelta(t *testing.T) {
    if got := pctDelta(110, 100); got != "+10.0%" {
        t.Fatalf("pctDelta(110,100) = %s, want +10.0%%", got)
    }
    if got := pctDelta(50, 0); got != "n/a" {
        t.Fatalf("pctDelta(50,0) = %s, want n/a", got)
    }
    if got := pctDelta(90, 100); got != "-10.0%" {
        t.Fatalf("pctDelta(90,100) = %s, want -10.0%%", got)
    }
}

func TestRenderReport_Sections(t *testing.T) {
    d := &reportData{
        Cur:  totalsRow{Requests: 1200, SuccessPct: 98.5, TTFTP95: 850},
        Prev: totalsRow{Requests: 1000, SuccessPct: 99.1, TTFTP95: 1000},
        WorstModels: []worstModelRow{
            {Model: "glm-5", N: 400, P95: 2100},
            {Model: "qwen3.5-4b", N: 300, P95: 900},
        },
        OpenAnomalies: 3,
        AnomaliesByKind: []anomalyKindRow{
            {Kind: "BREAKER_STORM", N: 2, Severity: "warn"},
        },
        Cost: costRow{CloudCost: 1.25, LocalSavings: 0.8, WasteCost: 0.05},
        Findings: []findingRow{
            {Rule: RuleMigrateToLocal, Scope: `{"model":"glm-5"}`, Status: "new"},
            {Rule: RuleMigrateToLocal, Scope: `{"model":"qwen3.5-4b"}`, Status: "applied",
                Model: "qwen3.5-4b", LocalN: 80, CloudN: 20},
        },
    }
    out := renderReport("daily", reportNow, reportNow.Add(24*time.Hour), d)
    for _, want := range []string{
        "# Fusion Gateway Daily Report",
        "Requests: **1200** (vs previous +20.0%)",
        "Success rate: **98.50%** (-0.60 pp)",
        "Global TTFT P95: **850 ms** (vs previous -15.0%)",
        "| glm-5 | 400 | 2100 |",
        "Open anomalies now: **3**",
        "| BREAKER_STORM | 2 | warn |",
        "Cloud cost: **1.2500**",
        "Local savings (estimated): **0.8000**",
        "Waste estimate (oversized prompts, input > 50x output): **0.0500**",
        "New finding `MIGRATE_TO_LOCAL` — {\"model\":\"glm-5\"}",
        "Applied `MIGRATE_TO_LOCAL` (qwen3.5-4b) — follow-up: 100 requests in 7d, 80.0% local / 20.0% cloud",
    } {
        if !strings.Contains(out, want) {
            t.Fatalf("rendered report missing %q\n---\n%s", want, out)
        }
    }
}

func TestRenderReport_EmptyData(t *testing.T) {
    out := renderReport("weekly", reportNow, reportNow.Add(7*24*time.Hour), &reportData{})
    for _, want := range []string{
        "No model reached 10 generation samples",
        "No anomalies detected in this window",
        "No optimization findings in this window",
        "vs previous n/a",
    } {
        if !strings.Contains(out, want) {
            t.Fatalf("empty report missing %q\n---\n%s", want, out)
        }
    }
}
