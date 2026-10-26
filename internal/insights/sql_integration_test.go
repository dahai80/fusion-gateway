package insights

import (
    "context"
    "os"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// requireTestDSN skips unless FUSION_GW_INSIGHTS_TEST_DSN points at a
// PostgreSQL database carrying the persistence schema. CI has no PG, so these
// run locally/deploy-side — they exist because unit tests cannot catch a
// broken SQL statement (the compare query's missing `AS requests` alias
// shipped to production and only surfaced as a 400 at runtime).
func requireTestDSN(t *testing.T) *Service {
    t.Helper()
    dsn := os.Getenv("FUSION_GW_INSIGHTS_TEST_DSN")
    if dsn == "" {
        t.Skip("set FUSION_GW_INSIGHTS_TEST_DSN to run live SQL checks")
    }
    svc, err := New(config.PersistenceConfig{}, dsn)
    if err != nil {
        t.Fatalf("connect: %v", err)
    }
    t.Cleanup(svc.Close)
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := svc.Ping(ctx); err != nil {
        t.Fatalf("ping: %v", err)
    }
    return svc
}

func TestSQL_LatencyAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    resp, err := svc.Latency(ctx, "", "", "hour", "", "", "")
    if err != nil {
        t.Fatalf("latency query: %v", err)
    }
    t.Logf("latency buckets=%d", len(resp.Series))
}

func TestSQL_CompareAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    resp, err := svc.Compare(ctx, "", "", "", 0, 0, false)
    if err != nil {
        t.Fatalf("compare query: %v", err)
    }
    t.Logf("compare rows=%d", len(resp.Rows))
}

func TestSQL_SearchAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    sq := SearchQuery{Q: "", Limit: 5}
    resp, err := svc.Search(ctx, sq)
    if err != nil {
        t.Fatalf("search query: %v", err)
    }
    t.Logf("search total=%d returned=%d", resp.Total, len(resp.Rows))
    if len(resp.Rows) > 0 {
        if _, err := svc.Payload(ctx, resp.Rows[0].ID); err != nil {
            t.Fatalf("payload query: %v", err)
        }
    }
}

func TestSQL_EventsAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    events, err := svc.Events(ctx, "", "", 10)
    if err != nil {
        t.Fatalf("events query: %v", err)
    }
    t.Logf("events returned=%d", len(events))
    if _, err := svc.Events(ctx, "info", "", 10); err != nil {
        t.Fatalf("events level filter: %v", err)
    }
}

func TestSQL_HWSamplesAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    samples, err := svc.HWSamples(ctx, "", "", 0)
    if err != nil {
        t.Fatalf("hw samples query: %v", err)
    }
    t.Logf("hw samples returned=%d", len(samples))
}

// TestSQL_AnomaliesLifecycleAgainstLiveDB exercises insert suppression, list
// filters, ack, and mute against the live schema. Uses kind='__test__' rows
// and cleans up after itself.
func TestSQL_AnomaliesLifecycleAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()

    t.Cleanup(func() {
        _, _ = svc.pool.Exec(context.Background(),
            `DELETE FROM fg_anomalies WHERE kind = '__test__'`)
    })

    d := &Detector{svc: svc}
    bucket := time.Now().UTC().Truncate(time.Hour).Add(-time.Duration(2) * time.Hour)
    draft := anomalyDraft{
        TS: bucket, Kind: "__test__", Backend: "test-backend",
        Model: "test-model", Severity: "high",
        Evidence: map[string]any{"count": 55, "window_min": 5},
    }
    if err := d.insert(ctx, draft); err != nil {
        t.Fatalf("insert: %v", err)
    }
    // Same signature + same bucket → suppressed; a later bucket is not.
    if !d.suppressed(ctx, draft) {
        t.Fatalf("duplicate draft should be suppressed")
    }
    later := draft
    later.TS = bucket.Add(time.Hour)
    if d.suppressed(ctx, later) {
        t.Fatalf("new-bucket draft should not be suppressed")
    }

    anomalies, err := svc.Anomalies(ctx, "", "__test__", "test-backend", 10)
    if err != nil {
        t.Fatalf("list: %v", err)
    }
    if len(anomalies) != 1 {
        t.Fatalf("list rows=%d, want 1", len(anomalies))
    }
    a := anomalies[0]
    if a.Status != "open" || a.Backend == nil || *a.Backend != "test-backend" {
        t.Errorf("row wrong: %+v", a)
    }

    acked, err := svc.AckAnomaly(ctx, a.ID, "tester")
    if err != nil {
        t.Fatalf("ack: %v", err)
    }
    if acked.Status != "acked" || acked.AckedBy == nil || *acked.AckedBy != "tester" {
        t.Errorf("acked wrong: %+v", acked)
    }
    // Idempotent re-ack.
    if _, err := svc.AckAnomaly(ctx, a.ID, "tester2"); err != nil {
        t.Fatalf("re-ack: %v", err)
    }

    muted, err := svc.MuteAnomaly(ctx, a.ID, time.Now().UTC().Add(24*time.Hour))
    if err != nil {
        t.Fatalf("mute: %v", err)
    }
    if muted.Status != "muted" || muted.MutedUntil == nil {
        t.Errorf("muted wrong: %+v", muted)
    }
    // Muted signature suppresses even a fresh-bucket draft.
    if !d.suppressed(ctx, later) {
        t.Fatalf("muted signature should suppress new buckets")
    }
    // Mute of an already-muted row → no rows affected (err).
    if _, err := svc.MuteAnomaly(ctx, a.ID, time.Now().UTC().Add(48*time.Hour)); err == nil {
        t.Fatalf("re-mute of muted row should fail")
    }
}

// TestSQL_DetectorFetchesAgainstLiveDB validates the four detector SQL
// statements against the live schema (unit tests cannot catch malformed SQL).
func TestSQL_DetectorFetchesAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    d := &Detector{svc: svc}
    if _, err := d.fetchErrorRows(ctx); err != nil {
        t.Fatalf("fetchErrorRows: %v", err)
    }
    if _, err := d.fetch15MinBuckets(ctx); err != nil {
        t.Fatalf("fetch15MinBuckets: %v", err)
    }
    if _, err := d.fetchHourBuckets(ctx); err != nil {
        t.Fatalf("fetchHourBuckets: %v", err)
    }
    if _, err := d.fetchBreakerDays(ctx); err != nil {
        t.Fatalf("fetchBreakerDays: %v", err)
    }
}

// TestSQL_RulesFetchesAgainstLiveDB validates the rules engine SQL statements
// against the live schema (unit tests cannot catch malformed SQL).
func TestSQL_RulesFetchesAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    re := &RulesEngine{svc: svc}
    if _, err := re.fetchModelSplit(ctx); err != nil {
        t.Fatalf("fetchModelSplit: %v", err)
    }
    if _, err := re.fetchLocalHourErrors(ctx); err != nil {
        t.Fatalf("fetchLocalHourErrors: %v", err)
    }
    if _, err := re.fetchPromptDupes(ctx); err != nil {
        t.Fatalf("fetchPromptDupes: %v", err)
    }
    if _, _, err := re.fetchLocalModelDays(ctx); err != nil {
        t.Fatalf("fetchLocalModelDays: %v", err)
    }
}

// TestSQL_FindingsLifecycleAgainstLiveDB exercises insert, dedupe, list, and
// the new-only apply/dismiss transitions with a __test__ row.
func TestSQL_FindingsLifecycleAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()

    draft := findingDraft{
        Rule:     "__test__",
        Scope:    map[string]any{"model": "__test_model__"},
        Evidence: map[string]any{"n": 1},
        Action:   map[string]any{"type": "models_supply"},
    }
    t.Cleanup(func() {
        svc.pool.Exec(context.Background(),
            `DELETE FROM fg_optimization_findings WHERE rule = '__test__'`)
    })

    re := &RulesEngine{svc: svc}
    if err := re.insertFinding(ctx, draft); err != nil {
        t.Fatalf("insert: %v", err)
    }
    if !re.deduped(ctx, draft) {
        t.Fatalf("same rule+scope within 24h must dedupe")
    }
    findings, err := svc.Findings(ctx, "", "__test__", 10)
    if err != nil || len(findings) != 1 {
        t.Fatalf("list: %v len=%d", err, len(findings))
    }
    id := findings[0].ID

    if _, err := svc.DismissFinding(ctx, id); err != nil {
        t.Fatalf("dismiss: %v", err)
    }
    // Dismissed row can no longer be applied (new-only transition).
    if _, err := svc.ApplyFinding(ctx, id, "{}"); err == nil {
        t.Fatalf("apply of dismissed row must fail")
    }
    f, err := svc.FindingByID(ctx, id)
    if err != nil || f.Status != "dismissed" {
        t.Fatalf("finding fetch after dismiss: %v status=%s", err, f.Status)
    }
}

// TestSQL_ReportsLifecycleAgainstLiveDB validates every report SQL statement
// (totals, worst models, anomaly kinds, cost, the findings UNION+JOIN) and
// the generate/dedupe/list/fetch lifecycle with a daily report for the last
// complete window.
func TestSQL_ReportsLifecycleAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    start, end, _ := reportWindow("daily", time.Now())
    t.Cleanup(func() {
        svc.pool.Exec(context.Background(),
            `DELETE FROM fg_reports WHERE period = 'daily' AND range_start = $1`, start)
    })

    if _, err := svc.fetchTotals(ctx, start, end); err != nil {
        t.Fatalf("fetchTotals: %v", err)
    }
    if _, err := svc.fetchTotals(ctx, start.Add(-end.Sub(start)), start); err != nil {
        t.Fatalf("fetchTotals (prev window): %v", err)
    }
    if _, err := svc.fetchWorstModels(ctx, start, end); err != nil {
        t.Fatalf("fetchWorstModels: %v", err)
    }
    if _, err := svc.fetchAnomalyKinds(ctx, start, end); err != nil {
        t.Fatalf("fetchAnomalyKinds: %v", err)
    }
    if _, err := svc.fetchFindingsForReport(ctx, start, end); err != nil {
        t.Fatalf("fetchFindingsForReport: %v", err)
    }
    costRows, err := svc.pool.Query(ctx,
        `SELECT COALESCE(sum(cost), 0), COALESCE(sum(local_savings), 0),
                COALESCE(sum(cost) FILTER (WHERE input_tokens > 0 AND output_tokens > 0
                    AND input_tokens > 50 * output_tokens), 0)
         FROM fg_request_logs WHERE ts >= $1 AND ts < $2`, start, end)
    if err != nil {
        t.Fatalf("cost query: %v", err)
    }
    costRows.Close()

    rep, err := svc.GenerateReport(ctx, "daily")
    if err != nil {
        t.Fatalf("generate: %v", err)
    }
    if rep.ContentMD == "" || !strings.Contains(rep.ContentMD, "# Fusion Gateway Daily Report") {
        t.Fatalf("report content wrong: %q", rep.ContentMD[:min(80, len(rep.ContentMD))])
    }
    if rep.ID == 0 {
        t.Fatalf("report id missing")
    }
    // Idempotent: second call must return the existing row, not a duplicate.
    rep2, err := svc.GenerateReport(ctx, "daily")
    if err != nil {
        t.Fatalf("re-generate: %v", err)
    }
    if rep2.ID != rep.ID {
        t.Fatalf("dedupe failed: got id=%d, want %d", rep2.ID, rep.ID)
    }

    reports, err := svc.Reports(ctx, "daily", 10)
    if err != nil {
        t.Fatalf("list: %v", err)
    }
    found := false
    for _, r := range reports {
        if r.ID == rep.ID {
            found = true
            if r.ContentMD != "" {
                t.Fatalf("list rows must omit content_md")
            }
        }
    }
    if !found {
        t.Fatalf("generated report missing from list (rows=%d)", len(reports))
    }
    got, err := svc.ReportByID(ctx, rep.ID)
    if err != nil {
        t.Fatalf("by id: %v", err)
    }
    if got.ContentMD != rep.ContentMD || got.ContentBytes != len(rep.ContentMD) {
        t.Fatalf("detail row mismatch: bytes=%d want=%d", got.ContentBytes, len(rep.ContentMD))
    }
    if _, err := svc.GenerateReport(ctx, "bogus"); err == nil {
        t.Fatalf("invalid period must fail")
    }
}
