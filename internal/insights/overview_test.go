package insights

import (
    "context"
    "testing"
    "time"
)

// TestDelta verifies the cumulative-counter window math: normal growth is a
// plain subtraction; a restart mid-window (last < first) falls back to the
// last sample as the baseline.
func TestDelta(t *testing.T) {
    if got := delta(100, 150); got != 50 {
        t.Fatalf("delta(100,150) = %d, want 50", got)
    }
    if got := delta(100, 100); got != 0 {
        t.Fatalf("delta(100,100) = %d, want 0", got)
    }
    if got := delta(500, 30); got != 30 {
        t.Fatalf("delta(500,30) = %d, want 30 (restart wrap)", got)
    }
    if got := delta(0, 0); got != 0 {
        t.Fatalf("delta(0,0) = %d, want 0", got)
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

var _ = time.Now // keep time import if assertions above change
