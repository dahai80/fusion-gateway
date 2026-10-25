package insights

import (
    "testing"
    "time"
)

// anchor: a fixed UTC moment so bucket math is deterministic.
// 2026-10-26 is a Monday.
var anchor = time.Date(2026, 10, 26, 9, 7, 30, 0, time.UTC)

func TestNormalizeError(t *testing.T) {
    cases := []struct{ in, want string }{
        {"Content Block Not Found: x", "content_block_not_found"},
        {"read tcp: connection reset by peer", "connection_reset"},
        {"dial tcp: connection refused", "connection_refused"},
        {"write: broken pipe", "broken_pipe"},
        {"context deadline exceeded", "context_deadline"},
        {"context canceled", "context_canceled"},
        {"unexpected EOF", "eof"},
        {"connection closed before response", "eof"},
        {"read tcp: EOF", "eof"},
        {"i/o timeout", "timeout"},
        {"HTTP 429 Too Many Requests", "rate_limited"},
        {"overloaded_error", "overloaded"},
        {"HTTP 503 Service Unavailable", "http_5xx"},
        {"something completely different", "something completely different"},
    }
    for _, c := range cases {
        if got := normalizeError(c.in); got != c.want {
            t.Errorf("normalizeError(%q) = %q, want %q", c.in, got, c.want)
        }
    }
    // Long unknown message truncates to 80 chars.
    long := make([]byte, 200)
    for i := range long {
        long[i] = 'x'
    }
    if got := normalizeError(string(long)); len(got) != 80 {
        t.Errorf("normalizeError truncation: len=%d, want 80", len(got))
    }
}

func TestClusterErrors(t *testing.T) {
    bucket := anchor.Truncate(5 * time.Minute)
    rows := []errRow{
        {Backend: "mlx", Model: "m1", Msg: "connection reset", Count: 30},
        {Backend: "mlx", Model: "m1", Msg: "read tcp: connection reset", Count: 25},
        // Same backend+model, different class — must not merge into the above.
        {Backend: "mlx", Model: "m1", Msg: "HTTP 503", Count: 10},
        // Below threshold.
        {Backend: "cloud", Model: "m2", Msg: "connection reset", Count: 49},
    }
    drafts := clusterErrors(rows, bucket)
    if len(drafts) != 1 {
        t.Fatalf("drafts=%d, want 1", len(drafts))
    }
    d := drafts[0]
    if d.Kind != "error_storm" || d.Backend != "mlx" || d.Model != "m1" || d.Severity != "high" {
        t.Errorf("draft fields wrong: %+v", d)
    }
    if d.Evidence["error_class"] != "connection_reset" || d.Evidence["count"] != 55 {
        t.Errorf("evidence wrong: %+v", d.Evidence)
    }
    if !d.TS.Equal(bucket) {
        t.Errorf("draft ts=%v, want bucket %v", d.TS, bucket)
    }
}

func TestEvalErrorSpike(t *testing.T) {
    now := anchor
    curBucket := now.Truncate(15 * time.Minute)
    mkRow := func(day int, bucket time.Time, total, errs int) bucket15Row {
        return bucket15Row{Backend: "mlx", Bucket: bucket.AddDate(0, 0, -day), Total: total, Errors: errs}
    }
    // Baseline: 3 prior weeks, same quarter-of-week slot, 100 req / 1 err = 1%.
    rows := []bucket15Row{
        {Backend: "mlx", Bucket: curBucket, Total: 100, Errors: 5},
        mkRow(7, curBucket, 100, 1),
        mkRow(14, curBucket, 100, 1),
        mkRow(21, curBucket, 100, 1),
        // Different quarter slot — ignored by baseline.
        mkRow(7, curBucket.Add(-30*time.Minute), 100, 100),
    }
    drafts := evalErrorSpike(rows, now)
    if len(drafts) != 1 {
        t.Fatalf("drafts=%d, want 1", len(drafts))
    }
    if drafts[0].Kind != "error_spike" || drafts[0].Severity != "high" {
        t.Errorf("draft wrong: %+v", drafts[0])
    }
    // 5% > 1% absolute AND > 2x baseline 1% — both conditions hold.

    // Guard: only 2× baseline, not above it → no fire.
    rows[0].Errors = 2
    if d := evalErrorSpike(rows, now); len(d) != 0 {
        t.Errorf("2x baseline should not fire, got %d", len(d))
    }
    // Guard: below 1% absolute → no fire even at 10x.
    rows[0] = bucket15Row{Backend: "mlx", Bucket: curBucket, Total: 100, Errors: 1}
    if d := evalErrorSpike(rows, now); len(d) != 0 {
        t.Errorf("1%% absolute should not fire, got %d", len(d))
    }
    // Guard: current sample too small.
    rows[0] = bucket15Row{Backend: "mlx", Bucket: curBucket, Total: 10, Errors: 5}
    if d := evalErrorSpike(rows, now); len(d) != 0 {
        t.Errorf("small current sample should not fire, got %d", len(d))
    }
    // Guard: no baseline history → no fire.
    rows = []bucket15Row{{Backend: "mlx", Bucket: curBucket, Total: 100, Errors: 50}}
    if d := evalErrorSpike(rows, now); len(d) != 0 {
        t.Errorf("no baseline should not fire, got %d", len(d))
    }
}

func TestEvalTTFTShift(t *testing.T) {
    now := anchor
    curBucket := now.Truncate(time.Hour)
    rows := []bucket1hRow{
        {Backend: "mlx", Bucket: curBucket, Total: 50, Gen: 10, P95: 3000},
        // Baseline: 2 prior weeks same hour-of-week, P95 1000 each.
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -7), Total: 50, Gen: 10, P95: 1000},
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -14), Total: 50, Gen: 10, P95: 1000},
        // Different hour-of-week — ignored.
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -7).Add(3 * time.Hour), Total: 50, Gen: 10, P95: 9000},
        // Gen below 5 — excluded from baseline.
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -21), Total: 5, Gen: 2, P95: 100},
    }
    drafts := evalTTFTShift(rows, now)
    if len(drafts) != 1 {
        t.Fatalf("drafts=%d, want 1", len(drafts))
    }
    d := drafts[0]
    if d.Kind != "ttft_shift" || d.Severity != "medium" {
        t.Errorf("draft wrong: %+v", d)
    }
    if d.Evidence["current_p95_ms"] != 3000.0 || d.Evidence["baseline_p95_ms"] != 1000.0 {
        t.Errorf("evidence wrong: %+v", d.Evidence)
    }
    // Guard: 1.4x — below the 1.5x threshold.
    rows[0].P95 = 1400
    if d := evalTTFTShift(rows, now); len(d) != 0 {
        t.Errorf("1.4x should not fire, got %d", len(d))
    }
    // Guard: current gen too small.
    rows[0].Gen = 4
    rows[0].P95 = 9000
    if d := evalTTFTShift(rows, now); len(d) != 0 {
        t.Errorf("small gen sample should not fire, got %d", len(d))
    }
    // Guard: single baseline bucket.
    rows = rows[:2]
    rows[0].Gen = 10
    rows[0].P95 = 3000
    if d := evalTTFTShift(rows, now); len(d) != 0 {
        t.Errorf("single baseline bucket should not fire, got %d", len(d))
    }
}

func TestEvalTraffic(t *testing.T) {
    now := anchor
    curBucket := now.Truncate(time.Hour)
    mk := func(day, total int) bucket1hRow {
        return bucket1hRow{Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -day), Total: total}
    }
    // Spike: 500 vs baseline 150 (same hour-of-week, prior weeks).
    rows := []bucket1hRow{
        {Backend: "mlx", Bucket: curBucket, Total: 400},
        {Backend: "cloud", Bucket: curBucket, Total: 100},
        mk(7, 150), mk(14, 150), mk(21, 150),
    }
    drafts := evalTraffic(rows, now)
    if len(drafts) != 1 {
        t.Fatalf("drafts=%d, want 1", len(drafts))
    }
    if drafts[0].Evidence["direction"] != "spike" || drafts[0].Evidence["current"] != 500 {
        t.Errorf("spike evidence wrong: %+v", drafts[0].Evidence)
    }
    // Drop: 30 vs baseline 150 → -80%.
    rows[0].Total = 15
    rows[1].Total = 15
    drafts = evalTraffic(rows, now)
    if len(drafts) != 1 || drafts[0].Evidence["direction"] != "drop" {
        t.Fatalf("drop draft wrong: %+v", drafts)
    }
    // Guard: within ±60%.
    rows[0].Total = 60
    rows[1].Total = 60
    if d := evalTraffic(rows, now); len(d) != 0 {
        t.Errorf("within band should not fire, got %d", len(d))
    }
    // Guard: current below 30.
    rows[0].Total = 10
    rows[1].Total = 5
    if d := evalTraffic(rows, now); len(d) != 0 {
        t.Errorf("small current should not fire, got %d", len(d))
    }
}

func TestEvalBreaker(t *testing.T) {
    now := anchor
    curBucket := now.Truncate(24 * time.Hour)
    // With baseline: 3 days at 1 trip/day, today 10 → 10 > 3x1 fires.
    rows := []breakerDayRow{
        {Backend: "mlx", Bucket: curBucket, Count: 10},
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -1), Count: 1},
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -2), Count: 1},
        {Backend: "mlx", Bucket: curBucket.AddDate(0, 0, -3), Count: 1},
    }
    drafts := evalBreaker(rows, now)
    if len(drafts) != 1 {
        t.Fatalf("drafts=%d, want 1", len(drafts))
    }
    if drafts[0].Kind != "breaker" || drafts[0].Backend != "mlx" {
        t.Errorf("draft wrong: %+v", drafts[0])
    }
    // Guard: at exactly 3x baseline — not above → no fire.
    rows[0].Count = 3
    if d := evalBreaker(rows, now); len(d) != 0 {
        t.Errorf("3x baseline should not fire, got %d", len(d))
    }
    // No baseline: today alone, >= 3 trips fires.
    rows = []breakerDayRow{{Backend: "cloud", Bucket: curBucket, Count: 3}}
    if d := evalBreaker(rows, now); len(d) != 1 {
        t.Errorf("no-baseline 3 trips should fire, got %d", len(d))
    }
    // No baseline: 2 trips — no fire.
    rows[0].Count = 2
    if d := evalBreaker(rows, now); len(d) != 0 {
        t.Errorf("no-baseline 2 trips should not fire, got %d", len(d))
    }
}

func TestQuarterAndHourKeys(t *testing.T) {
    // Go's Weekday: Sunday=0, Saturday=6. Sunday 00:00 → quarter 0, hour 0.
    sun := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
    if q := quarterKeyOf(sun); q != 0 {
        t.Errorf("quarterKeyOf(Sun 00:00)=%d, want 0", q)
    }
    if h := hourOfWeekKeyOf(sun); h != 0 {
        t.Errorf("hourOfWeekKeyOf(Sun 00:00)=%d, want 0", h)
    }
    // Saturday 23:45 → quarter 671, hour 167 (end of week).
    sat := time.Date(2026, 10, 31, 23, 45, 0, 0, time.UTC)
    if q := quarterKeyOf(sat); q != 671 {
        t.Errorf("quarterKeyOf(Sat 23:45)=%d, want 671", q)
    }
    if h := hourOfWeekKeyOf(sat); h != 167 {
        t.Errorf("hourOfWeekKeyOf(Sat 23:45)=%d, want 167", h)
    }
    // Same slot one week apart shares the key.
    a := time.Date(2026, 10, 26, 9, 7, 0, 0, time.UTC)
    b := a.AddDate(0, 0, -7)
    if quarterKeyOf(a) != quarterKeyOf(b) || hourOfWeekKeyOf(a) != hourOfWeekKeyOf(b) {
        t.Errorf("week-apart slots must share keys")
    }
}
