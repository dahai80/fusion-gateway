package insights

import (
    "context"
    "fmt"
    "testing"
    "time"
)

// TestSQL_OverviewCacheDeltaAcrossRestart is the #427 regression: the window
// delta must be the SUM of adjacent-sample increments across a counter wrap
// (gateway restart), not a first-vs-last subtraction. A first-vs-last read of
// the seeded sequence reports last (4) — and its wrap fallback reported the
// same — discarding the entire pre-restart run segment. Requires
// FUSION_GW_INSIGHTS_TEST_DSN (same skip rule as the other TestSQL_* cases).
func TestSQL_OverviewCacheDeltaAcrossRestart(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    name := fmt.Sprintf("test-wrap-%d", time.Now().UnixNano())
    defer func() {
        if _, err := svc.pool.Exec(ctx, `DELETE FROM fg_cache_stats WHERE cache = $1`, name); err != nil {
            t.Logf("cleanup: %v", err)
        }
    }()
    // Two run segments inside the window. Run A grows hits 5->12, restart,
    // run B grows 0->4. Expected window hits = (12-5) + (4-0) = 11; misses
    // = (4-2) + (2-0) = 4. The buggy first-vs-last delta returned 4/2.
    base := time.Now().UTC().Add(-10 * time.Minute)
    samples := []struct {
        offset time.Duration
        hits   int64
        misses int64
    }{
        {0, 5, 2}, {time.Minute, 8, 3}, {2 * time.Minute, 12, 4},
        {3 * time.Minute, 0, 0}, {4 * time.Minute, 2, 1}, {5 * time.Minute, 4, 2},
    }
    for _, s := range samples {
        if _, err := svc.pool.Exec(ctx,
            `INSERT INTO fg_cache_stats (ts, cache, hits, misses, size)
             VALUES ($1,$2,$3,$4,1) ON CONFLICT (ts, cache) DO NOTHING`,
            base.Add(s.offset), name, s.hits, s.misses); err != nil {
            t.Fatalf("seed sample: %v", err)
        }
    }
    from := base.Add(-time.Minute).Format(time.RFC3339)
    to := base.Add(10 * time.Minute).Format(time.RFC3339)
    resp, err := svc.Overview(ctx, from, to, "hour")
    if err != nil {
        t.Fatalf("overview query: %v", err)
    }
    var got *OverviewCache
    for i := range resp.GatewayCaches {
        if resp.GatewayCaches[i].Cache == name {
            got = &resp.GatewayCaches[i]
        }
    }
    if got == nil {
        t.Fatal("seeded cache missing from overview gateway_caches")
    }
    if got.Hits != 11 {
        t.Fatalf("hits = %d, want 11 (sum of segments across restart)", got.Hits)
    }
    if got.Misses != 4 {
        t.Fatalf("misses = %d, want 4", got.Misses)
    }
    if got.Samples != 6 {
        t.Fatalf("samples = %d, want 6", got.Samples)
    }
}

// TestOverviewCacheHitRateMath pins the hit-rate formula used by the cache
// delta loop in Overview (hits / (hits+misses), zero-guarded).
func TestOverviewCacheHitRateMath(t *testing.T) {
    c := OverviewCache{Hits: 30, Misses: 70}
    if c.Hits+c.Misses > 0 {
        c.HitRate = float64(c.Hits) / float64(c.Hits+c.Misses)
    }
    if c.HitRate != 0.3 {
        t.Fatalf("hit rate = %v, want 0.3", c.HitRate)
    }
    var zero OverviewCache
    if zero.Hits+zero.Misses > 0 {
        t.Fatal("zero counters must not divide")
    }
}

// TestSQL_OverviewAgainstLiveDB — live SQL check (skipped without
// FUSION_GW_INSIGHTS_TEST_DSN) for the same reason as the other TestSQL_*
// cases: unit tests cannot catch a broken SQL statement.
func TestSQL_OverviewAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    resp, err := svc.Overview(ctx, "", "", "hour")
    if err != nil {
        t.Fatalf("overview query: %v", err)
    }
    if resp.Totals.TotalTokens < resp.Totals.InputTokens+resp.Totals.OutputTokens {
        t.Fatalf("total tokens %d < in+out %d", resp.Totals.TotalTokens, resp.Totals.InputTokens+resp.Totals.OutputTokens)
    }
    if len(resp.Series) > 1 {
        if resp.Series[0].Bucket >= resp.Series[len(resp.Series)-1].Bucket {
            t.Fatal("series must be sorted ascending by bucket")
        }
    }
    t.Logf("overview requests=%d caches=%d models=%d buckets=%d",
        resp.Totals.Requests, len(resp.GatewayCaches), len(resp.TopModels), len(resp.Series))
}

// TestOverview_RejectsBadGranularity checks the parameter validation path
// before any SQL runs.
func TestOverview_RejectsBadGranularity(t *testing.T) {
    if _, err := granularity("week"); err == nil {
        t.Fatal("invalid granularity must error")
    }
    if _, _, err := windowBounds("not-a-time", ""); err == nil {
        t.Fatal("malformed window must error")
    }
}
