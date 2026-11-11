package cost

// #628 ask 1: the site /my/usage page needs a per-call ledger over the same
// metering the /v1/cost summary aggregates — time-range + pagination filters
// and a window summary that reconciles with SummaryByKey for the same
// period. Exposed read API over the tracker's records; the ring window is
// the honest bound (reported as "source":"ring" by the handler).

import (
    "testing"
    "time"
)

func Test628_Ledger_FiltersAndPaginates(t *testing.T) {
    tr := NewTracker(100)
    base := time.Now().Add(-time.Hour)
    // 5 calls for key-a, 3 for key-b, spread over 40 minutes.
    for i := 0; i < 5; i++ {
        tr.RecordAndReturn("key-a", "local", "m1", 100, 50)
        tr.records[len(tr.records)-1].Timestamp = base.Add(time.Duration(i) * 10 * time.Minute)
    }
    for i := 0; i < 3; i++ {
        tr.RecordAndReturn("key-b", "cloud", "m2", 10, 5)
        tr.records[len(tr.records)-1].Timestamp = base.Add(time.Duration(i) * 10 * time.Minute)
    }

    since := base.Add(-time.Minute)
    until := base.Add(time.Hour)

    recs, total := tr.Ledger("", since, until, 0, 100)
    if total != 8 || len(recs) != 8 {
        t.Fatalf("all-keys ledger = %d/%d, want 8/8", len(recs), total)
    }
    recs, total = tr.Ledger("key-a", since, until, 0, 100)
    if total != 5 || len(recs) != 5 {
        t.Fatalf("key-a ledger = %d/%d, want 5/5", len(recs), total)
    }
    // Pagination: 2 per page over key-a.
    p1, total := tr.Ledger("key-a", since, until, 0, 2)
    p2, _ := tr.Ledger("key-a", since, until, 2, 2)
    if total != 5 || len(p1) != 2 || len(p2) != 2 {
        t.Fatalf("pages = %d(%d) + %d, want 2+2 of 5", len(p1), total, len(p2))
    }
    if !p1[0].Timestamp.Before(p1[1].Timestamp) {
        t.Fatalf("ledger must be timestamp-ascending")
    }
    // Time-range cut: only calls at base..base+20m.
    recs, total = tr.Ledger("", base, base.Add(21*time.Minute), 0, 100)
    if total != 6 { // 3 key-a + 3 key-b inside the first 21 minutes
        t.Fatalf("window ledger total = %d, want 6", total)
    }
    for _, r := range recs {
        if r.Timestamp.Before(base) || r.Timestamp.After(base.Add(21*time.Minute)) {
            t.Fatalf("record outside window: %v", r.Timestamp)
        }
    }
    // Zero times = unbounded.
    _, total = tr.Ledger("key-b", time.Time{}, time.Time{}, 0, 100)
    if total != 3 {
        t.Fatalf("unbounded ledger total = %d, want 3", total)
    }
}

func Test628_WindowSummary_ReconcilesWithSummaryByKey(t *testing.T) {
    tr := NewTracker(100)
    tr.RecordAndReturn("key-a", "local", "m1", 100, 50)
    tr.RecordAndReturn("key-a", "cloud", "m2", 30, 20)
    tr.RecordAndReturn("key-b", "local", "m1", 10, 5)

    ws := tr.WindowSummary("key-a", time.Time{}, time.Time{})
    kb := tr.SummaryByKey("key-a")
    if ws.TotalRequests != kb.TotalRequests || ws.TotalTokens != kb.TotalTokens {
        t.Fatalf("window summary %d/%d != by-key %d/%d",
            ws.TotalRequests, ws.TotalTokens, kb.TotalRequests, kb.TotalTokens)
    }
    if diff := ws.TotalCostUSD - kb.TotalCostUSD; diff > 1e-9 || diff < -1e-9 {
        t.Fatalf("cost mismatch: %f vs %f", ws.TotalCostUSD, kb.TotalCostUSD)
    }
    if len(ws.ByBackend) != 2 {
        t.Fatalf("by_backend entries = %d, want 2", len(ws.ByBackend))
    }
}
