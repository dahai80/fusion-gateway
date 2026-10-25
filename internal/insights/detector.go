// P3b stage 2 detector (plan §15.1): periodic pass over fg_request_logs and
// fg_gateway_events comparing current buckets against an hour-of-week
// baseline. Pure evaluation functions live here so unit tests cover the
// thresholds without a database; the Detector only fetches rows and persists
// drafts. Detection is idempotent per bucket — a draft whose (kind, backend,
// model, bucket) signature already exists is skipped, so restarts and slow
// ticks never duplicate rows.
package insights

import (
    "context"
    "encoding/json"
    "log/slog"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

// detectorInterval: fastest detector window is the 5-minute error storm, so a
// 5-minute cadence covers every bucket exactly once.
const detectorInterval = 5 * time.Minute

// EventRecorder is the structured-audit sink (satisfied by *pg.Sink). Local
// interface so insights never imports the persist package.
type EventRecorder interface {
    RecordEvent(level, event string, detail map[string]any)
}

// Detector is the background anomaly worker. Shares the Service read pool —
// its queries are the same class of bounded aggregates the admin API runs.
type Detector struct {
    svc      *Service
    recorder EventRecorder
    worker   *lifecycle.Worker
}

// StartDetector launches the periodic detector pass. Returns the worker so
// shutdown can join it (call Stop before closing the pool).
func (s *Service) StartDetector(parent context.Context, rec EventRecorder) *Detector {
    d := &Detector{svc: s, recorder: rec}
    d.worker = lifecycle.Start(parent, "insights-anomaly-detector", d.loop)
    slog.Info("anomaly detector started", "interval", detectorInterval.String())
    return d
}

// Stop joins the detector worker.
func (d *Detector) Stop() {
    if d.worker != nil {
        d.worker.Stop()
    }
}

func (d *Detector) loop(ctx context.Context) {
    // Immediate first pass: buckets already covered by a previous process are
    // deduped in the database, so a restart pass is a cheap no-op.
    if err := d.runOnce(ctx); err != nil {
        slog.Warn("anomaly detector pass failed", "error", err)
    }
    ticker := time.NewTicker(detectorInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if err := d.runOnce(ctx); err != nil {
                slog.Warn("anomaly detector pass failed", "error", err)
            }
        }
    }
}

// anomalyDraft is a detection awaiting persistence.
type anomalyDraft struct {
    TS       time.Time
    Kind     string
    Backend  string
    Model    string
    Severity string
    Evidence map[string]any
}

// runOnce executes every detector and persists the fired drafts.
func (d *Detector) runOnce(ctx context.Context) error {
    now := time.Now().UTC()
    var fired []anomalyDraft

    if rows, err := d.fetchErrorRows(ctx); err != nil {
        slog.Warn("anomaly detector: error fetch failed", "error", err)
    } else {
        fired = append(fired, clusterErrors(rows, now.Truncate(5*time.Minute))...)
    }
    if rows, err := d.fetch15MinBuckets(ctx); err != nil {
        slog.Warn("anomaly detector: 15min fetch failed", "error", err)
    } else {
        fired = append(fired, evalErrorSpike(rows, now)...)
    }
    if rows, err := d.fetchHourBuckets(ctx); err != nil {
        slog.Warn("anomaly detector: hour fetch failed", "error", err)
    } else {
        fired = append(fired, evalTTFTShift(rows, now)...)
        fired = append(fired, evalTraffic(rows, now)...)
    }
    if rows, err := d.fetchBreakerDays(ctx); err != nil {
        slog.Warn("anomaly detector: breaker fetch failed", "error", err)
    } else {
        fired = append(fired, evalBreaker(rows, now)...)
    }

    for _, a := range fired {
        if d.suppressed(ctx, a) {
            continue
        }
        if err := d.insert(ctx, a); err != nil {
            slog.Warn("anomaly detector insert failed",
                "kind", a.Kind, "backend", a.Backend, "error", err)
            continue
        }
        slog.Warn("anomaly detected",
            "kind", a.Kind, "backend", a.Backend, "model", a.Model,
            "severity", a.Severity, "bucket", a.TS.Format(time.RFC3339))
        if d.recorder != nil {
            d.recorder.RecordEvent("warn", "anomaly_detected", map[string]any{
                "kind": a.Kind, "backend": a.Backend, "model": a.Model,
                "severity": a.Severity,
            })
        }
    }
    return nil
}

// suppressed reports whether a draft should be dropped: either the signature
// is muted (muted_until in the future) or this bucket already produced a row.
func (d *Detector) suppressed(ctx context.Context, a anomalyDraft) bool {
    var exists bool
    err := d.svc.pool.QueryRow(ctx,
        `SELECT EXISTS(SELECT 1 FROM fg_anomalies
          WHERE kind = $1 AND COALESCE(backend, '') = $2 AND COALESCE(model, '') = $3
            AND ((status = 'muted' AND muted_until > now()) OR ts >= $4))`,
        a.Kind, a.Backend, a.Model, a.TS).Scan(&exists)
    if err != nil {
        // Fail open: a broken suppression check must not block detection.
        slog.Warn("anomaly detector suppression check failed", "error", err)
        return false
    }
    return exists
}

// insert persists one draft. Empty backend/model land as NULL.
func (d *Detector) insert(ctx context.Context, a anomalyDraft) error {
    ev, err := json.Marshal(a.Evidence)
    if err != nil {
        return err
    }
    var backend, model any
    if a.Backend != "" {
        backend = a.Backend
    }
    if a.Model != "" {
        model = a.Model
    }
    _, err = d.svc.pool.Exec(ctx,
        `INSERT INTO fg_anomalies
             (ts, detected_at, kind, backend, model, severity, evidence)
         VALUES ($1, now(), $2, $3, $4, $5, $6)`,
        a.TS, a.Kind, backend, model, a.Severity, ev)
    return err
}

// normalizeError maps a raw error message to a stable error class (plan §15.1
// 错误归一化). More specific patterns must precede their substrings
// ("unexpected eof" before "eof"). Unknown messages cluster on a truncated
// lowercased form so identical raw errors still group.
func normalizeError(msg string) string {
    m := strings.ToLower(msg)
    patterns := []struct{ pat, class string }{
        {"content block not found", "content_block_not_found"},
        {"connection reset", "connection_reset"},
        {"connection refused", "connection_refused"},
        {"broken pipe", "broken_pipe"},
        {"context deadline", "context_deadline"},
        {"context canceled", "context_canceled"},
        {"unexpected eof", "eof"},
        {"connection closed", "eof"},
        {"eof", "eof"},
        {"timeout", "timeout"},
        {"too many requests", "rate_limited"},
        {"429", "rate_limited"},
        {"overloaded", "overloaded"},
        {"500", "http_5xx"},
        {"502", "http_5xx"},
        {"503", "http_5xx"},
        {"504", "http_5xx"},
    }
    for _, p := range patterns {
        if strings.Contains(m, p.pat) {
            return p.class
        }
    }
    if len(m) > 80 {
        m = m[:80]
    }
    return m
}

// errRow is one (backend, model, raw message) error count in the 5-minute
// error-storm window.
type errRow struct {
    Backend string
    Model   string
    Msg     string
    Count   int
}

// clusterErrors is the error-storm detector: normalized error class count in
// the current 5-minute bucket >= 50 fires high severity (plan §15.1).
func clusterErrors(rows []errRow, bucket time.Time) []anomalyDraft {
    type key struct{ backend, model, class string }
    counts := map[key]int{}
    samples := map[key][]string{}
    for _, r := range rows {
        k := key{r.Backend, r.Model, normalizeError(r.Msg)}
        counts[k] += r.Count
        if len(samples[k]) < 3 {
            samples[k] = append(samples[k], r.Msg)
        }
    }
    var out []anomalyDraft
    for k, n := range counts {
        if n < 50 {
            continue
        }
        out = append(out, anomalyDraft{
            TS: bucket, Kind: "error_storm",
            Backend: k.backend, Model: k.model, Severity: "high",
            Evidence: map[string]any{
                "error_class": k.class,
                "count":       n,
                "samples":     samples[k],
                "window_min":  5,
            },
        })
    }
    return out
}

// bucket15Row is one backend's 15-minute request/error tally.
type bucket15Row struct {
    Backend string
    Bucket  time.Time
    Total   int
    Errors  int
}

// quarterKeyOf identifies the 15-minute slot within a week (0..671).
func quarterKeyOf(t time.Time) int {
    return (int(t.Weekday())*24+t.Hour())*4 + t.Minute()/15
}

// evalErrorSpike is the error-rate detector (plan §15.1): current 15-minute
// error rate >1% absolute AND >2× the same-slot-of-week baseline from the
// previous 7 days. Guards: current >= 20 requests, baseline >= 3 buckets and
// >= 30 requests — tiny-sample ratios are noise, not anomalies.
func evalErrorSpike(rows []bucket15Row, now time.Time) []anomalyDraft {
    curKey := quarterKeyOf(now)
    curBucket := now.Truncate(15 * time.Minute)
    type stats struct{ total, errs, buckets int }
    cur := map[string]*stats{}
    base := map[string]*stats{}
    for _, r := range rows {
        if r.Bucket.Equal(curBucket) {
            s := cur[r.Backend]
            if s == nil {
                s = &stats{}
                cur[r.Backend] = s
            }
            s.total += r.Total
            s.errs += r.Errors
            continue
        }
        if quarterKeyOf(r.Bucket) != curKey {
            continue
        }
        s := base[r.Backend]
        if s == nil {
            s = &stats{}
            base[r.Backend] = s
        }
        s.total += r.Total
        s.errs += r.Errors
        s.buckets++
    }
    var out []anomalyDraft
    for backend, c := range cur {
        b := base[backend]
        if b == nil || c.total < 20 || b.buckets < 3 || b.total < 30 {
            continue
        }
        curRate := float64(c.errs) / float64(c.total)
        baseRate := float64(b.errs) / float64(b.total)
        if curRate <= 0.01 || curRate <= 2*baseRate {
            continue
        }
        out = append(out, anomalyDraft{
            TS: curBucket, Kind: "error_spike",
            Backend: backend, Severity: "high",
            Evidence: map[string]any{
                "current_rate":    curRate,
                "baseline_rate":   baseRate,
                "current_errors":  c.errs,
                "current_total":   c.total,
                "baseline_errors": b.errs,
                "baseline_total":  b.total,
            },
        })
    }
    return out
}

// bucket1hRow is one backend's hourly aggregate.
type bucket1hRow struct {
    Backend string
    Bucket  time.Time
    Total   int
    Gen     int
    P95     float64
}

// hourOfWeekKeyOf identifies the hour slot within a week (0..167).
func hourOfWeekKeyOf(t time.Time) int {
    return int(t.Weekday())*24 + t.Hour()
}

// evalTTFTShift is the latency detector (plan §15.1): current hour's TTFT
// P95 >50% above the average P95 of the same hour-of-week over the previous
// 7 days. Guards: >= 5 generation requests now, >= 2 baseline buckets.
func evalTTFTShift(rows []bucket1hRow, now time.Time) []anomalyDraft {
    curKey := hourOfWeekKeyOf(now)
    curBucket := now.Truncate(time.Hour)
    type cur struct {
        gen int
        p95 float64
    }
    type base struct {
        buckets int
        p95Sum  float64
    }
    curs := map[string]*cur{}
    bases := map[string]*base{}
    for _, r := range rows {
        if r.Bucket.Equal(curBucket) {
            c := curs[r.Backend]
            if c == nil {
                c = &cur{}
                curs[r.Backend] = c
            }
            c.gen += r.Gen
            c.p95 = max(c.p95, r.P95)
            continue
        }
        if hourOfWeekKeyOf(r.Bucket) != curKey || r.Gen < 5 || r.P95 <= 0 {
            continue
        }
        b := bases[r.Backend]
        if b == nil {
            b = &base{}
            bases[r.Backend] = b
        }
        b.buckets++
        b.p95Sum += r.P95
    }
    var out []anomalyDraft
    for backend, c := range curs {
        b := bases[backend]
        if b == nil || c.gen < 5 || c.p95 <= 0 || b.buckets < 2 {
            continue
        }
        baseAvg := b.p95Sum / float64(b.buckets)
        if c.p95 <= 1.5*baseAvg {
            continue
        }
        change := (c.p95 - baseAvg) / baseAvg * 100
        out = append(out, anomalyDraft{
            TS: curBucket, Kind: "ttft_shift",
            Backend: backend, Severity: "medium",
            Evidence: map[string]any{
                "current_p95_ms": c.p95,
                "baseline_p95_ms": baseAvg,
                "change_pct":      change,
                "gen_requests":    c.gen,
                "baseline_buckets": b.buckets,
            },
        })
    }
    return out
}

// evalTraffic is the global volume detector (plan §15.1): current hour's
// total requests deviating >±60% from the same hour-of-week baseline.
// Global, not per-backend — backend-level shifts are dominated by the
// routing/cloud_models allowlist, which is config, not an anomaly.
func evalTraffic(rows []bucket1hRow, now time.Time) []anomalyDraft {
    curKey := hourOfWeekKeyOf(now)
    curBucket := now.Truncate(time.Hour)
    curTotal := 0
    baseTotal := 0
    baseBuckets := 0
    for _, r := range rows {
        if r.Bucket.Equal(curBucket) {
            curTotal += r.Total
            continue
        }
        if hourOfWeekKeyOf(r.Bucket) != curKey || r.Total <= 0 {
            continue
        }
        baseTotal += r.Total
        baseBuckets++
    }
    if curTotal < 30 || baseBuckets < 3 || baseTotal < 60 {
        return nil
    }
    baseAvg := float64(baseTotal) / float64(baseBuckets)
    dev := (float64(curTotal) - baseAvg) / baseAvg
    if dev > -0.6 && dev < 0.6 {
        return nil
    }
    direction := "drop"
    if dev > 0 {
        direction = "spike"
    }
    return []anomalyDraft{{
        TS: curBucket, Kind: "traffic", Severity: "medium",
        Evidence: map[string]any{
            "current":        curTotal,
            "baseline_avg":   baseAvg,
            "deviation_pct":  dev * 100,
            "direction":      direction,
            "baseline_buckets": baseBuckets,
        },
    }}
}

// breakerDayRow is one backend's daily breaker-trip count from
// fg_gateway_events (event = breaker_tripped).
type breakerDayRow struct {
    Backend string
    Bucket  time.Time
    Count   int
}

// evalBreaker is the breaker-storm detector (plan §15.1): today's trips >3×
// the previous 7 days' daily average. With no baseline history the guard is
// >= 3 trips — a fresh storm on a quiet backend still fires.
func evalBreaker(rows []breakerDayRow, now time.Time) []anomalyDraft {
    curBucket := now.Truncate(24 * time.Hour)
    cur := map[string]int{}
    base := map[string]struct{ sum, days int }{}
    for _, r := range rows {
        if r.Bucket.Equal(curBucket) {
            cur[r.Backend] += r.Count
            continue
        }
        b := base[r.Backend]
        b.sum += r.Count
        b.days++
        base[r.Backend] = b
    }
    var out []anomalyDraft
    for backend, n := range cur {
        b := base[backend]
        baseAvg := 0.0
        if b.days > 0 {
            baseAvg = float64(b.sum) / float64(b.days)
        }
        if baseAvg > 0 {
            if float64(n) <= 3*baseAvg {
                continue
            }
        } else if n < 3 {
            continue
        }
        out = append(out, anomalyDraft{
            TS: curBucket, Kind: "breaker",
            Backend: backend, Severity: "medium",
            Evidence: map[string]any{
                "current_trips":     n,
                "baseline_daily_avg": baseAvg,
                "baseline_days":     b.days,
            },
        })
    }
    return out
}

// fetchErrorRows pulls the current 5-minute bucket's raw error messages for
// the storm detector (current bucket, not a rolling window — the bucket TS is
// the dedupe key, so one pass per bucket).
func (d *Detector) fetchErrorRows(ctx context.Context) ([]errRow, error) {
    bucket := time.Now().UTC().Truncate(5 * time.Minute)
    rows, err := d.svc.pool.Query(ctx,
        `SELECT backend, COALESCE(model, ''), error_message, count(*)
         FROM fg_request_logs
         WHERE ts >= $1 AND is_success = false
           AND error_message IS NOT NULL AND error_message <> ''
         GROUP BY backend, COALESCE(model, ''), error_message`,
        bucket)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []errRow{}
    for rows.Next() {
        var r errRow
        if err := rows.Scan(&r.Backend, &r.Model, &r.Msg, &r.Count); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetch15MinBuckets pulls 8 days of 15-minute per-backend tallies — current
// bucket plus the 7-day hour-of-week baseline in one scan.
func (d *Detector) fetch15MinBuckets(ctx context.Context) ([]bucket15Row, error) {
    rows, err := d.svc.pool.Query(ctx,
        `SELECT backend,
                to_timestamp(floor(extract(epoch FROM ts) / 900) * 900) AS bucket,
                count(*) AS total,
                count(*) FILTER (WHERE is_success = false) AS errs
         FROM fg_request_logs
         WHERE ts >= now() - interval '8 days'
         GROUP BY backend, 2`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []bucket15Row{}
    for rows.Next() {
        var r bucket15Row
        if err := rows.Scan(&r.Backend, &r.Bucket, &r.Total, &r.Errors); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetchHourBuckets pulls 8 days of hourly per-backend aggregates: volume for
// the traffic detector and TTFT P95 (generation rows only, ttft_ms > 0 — the
// P3a zero-value guard) for the latency detector.
func (d *Detector) fetchHourBuckets(ctx context.Context) ([]bucket1hRow, error) {
    rows, err := d.svc.pool.Query(ctx,
        `SELECT backend,
                date_trunc('hour', ts) AS bucket,
                count(*) AS total,
                count(*) FILTER (WHERE ttft_ms > 0) AS gen,
                COALESCE(percentile_cont(0.95) WITHIN GROUP
                    (ORDER BY ttft_ms) FILTER (WHERE ttft_ms > 0), 0) AS p95
         FROM fg_request_logs
         WHERE ts >= now() - interval '8 days'
         GROUP BY backend, 2`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []bucket1hRow{}
    for rows.Next() {
        var r bucket1hRow
        if err := rows.Scan(&r.Backend, &r.Bucket, &r.Total, &r.Gen, &r.P95); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetchBreakerDays pulls 8 days of daily breaker-trip counts from
// fg_gateway_events — the producer is router.Engine.recordTripEvent (P3b).
func (d *Detector) fetchBreakerDays(ctx context.Context) ([]breakerDayRow, error) {
    rows, err := d.svc.pool.Query(ctx,
        `SELECT COALESCE(detail->>'backend', ''), date_trunc('day', ts), count(*)
         FROM fg_gateway_events
         WHERE event = 'breaker_tripped' AND ts >= now() - interval '8 days'
         GROUP BY 1, 2`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []breakerDayRow{}
    for rows.Next() {
        var r breakerDayRow
        if err := rows.Scan(&r.Backend, &r.Bucket, &r.Count); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}
