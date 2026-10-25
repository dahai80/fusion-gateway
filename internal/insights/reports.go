// P3b stage 3c (plan §15.3/§15.5): periodic Markdown reports. A worker
// generates a daily report (previous calendar day) and a weekly report (last
// complete Mon-Sun) after 08:00 local time, persists into fg_reports and
// drops a file copy under ~/.fusion-gateway/reports/. Dedupe is
// (period, range_start) so restarts and overlapping ticks never duplicate
// rows; the DB row is the source of truth, the file copy is best-effort.
// Pure window/render functions live here so unit tests cover them without a
// database; fetches are the same class of bounded aggregates the admin API
// runs (same pool, same 10s statement_timeout).
package insights

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

// reportsTick: hourly check; generation is gated on 08:00 local (plan §15.3).
const reportsTick = time.Hour

// Report is one fg_reports row. ContentMD is omitted in list responses —
// reports can be tens of KB and the list only needs metadata.
type Report struct {
    ID           int64     `json:"id"`
    Period       string    `json:"period"`
    RangeStart   time.Time `json:"range_start"`
    RangeEnd     time.Time `json:"range_end"`
    CreatedAt    time.Time `json:"created_at"`
    ContentBytes int       `json:"content_bytes"`
    ContentMD    string    `json:"content_md,omitempty"`
}

// ReportsWorker is the background report generator.
type ReportsWorker struct {
    svc      *Service
    recorder EventRecorder
    dir      string
    worker   *lifecycle.Worker
}

// StartReportsWorker launches the hourly report pass. dir is the file-copy
// destination (already tilde-expanded); empty disables the file copy. Stop
// before closing the pool (same shutdown contract as the detector).
func (s *Service) StartReportsWorker(parent context.Context, rec EventRecorder, dir string) *ReportsWorker {
    rw := &ReportsWorker{svc: s, recorder: rec, dir: dir}
    rw.worker = lifecycle.Start(parent, "insights-reports", rw.loop)
    slog.Info("reports worker started", "tick", reportsTick.String(), "dir", dir)
    return rw
}

// Stop joins the reports worker.
func (rw *ReportsWorker) Stop() {
    if rw.worker != nil {
        rw.worker.Stop()
    }
}

func (rw *ReportsWorker) loop(ctx context.Context) {
    // Immediate first pass: the (period, range_start) dedupe makes a restart
    // pass a no-op, and it catches up a report missed while the gateway was
    // down (as long as it is past 08:00).
    rw.runOnce(ctx)
    ticker := time.NewTicker(reportsTick)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            rw.runOnce(ctx)
        }
    }
}

// runOnce generates every report period that is due at this instant.
func (rw *ReportsWorker) runOnce(ctx context.Context) {
    now := time.Now()
    for _, period := range dueReports(now) {
        rep, err := rw.svc.GenerateReport(ctx, period)
        if err != nil {
            slog.Warn("reports worker generation failed", "period", period, "error", err)
            continue
        }
        rw.writeFile(rep)
        if rw.recorder != nil {
            rw.recorder.RecordEvent("info", "report_generated", map[string]any{
                "period": rep.Period, "range_start": rep.RangeStart.Format(time.RFC3339),
                "content_bytes": rep.ContentBytes,
            })
        }
    }
}

// dueReports returns the periods whose window is complete and whose 08:00
// local publication time has passed (plan §15.3: daily 08:00, weekly Mon
// 08:00). Weekly is due only on Monday — a gateway down all Monday skips
// that week's report (the manual generate endpoint can still force it).
func dueReports(now time.Time) []string {
    var out []string
    if now.Hour() >= 8 {
        out = append(out, "daily")
        if now.Weekday() == time.Monday {
            out = append(out, "weekly")
        }
    }
    return out
}

// reportWindow computes the last complete window for a period, anchored on
// local midnights (single-host local-first gateway; operator reads the
// report in their own timezone). The second return is the weekday of the
// window end — dueReports needs it without re-deriving the window.
func reportWindow(period string, now time.Time) (time.Time, time.Time, time.Weekday) {
    end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
    switch period {
    case "daily":
        return end.AddDate(0, 0, -1), end, end.Weekday()
    case "weekly":
        // Walk back to the most recent Monday midnight (exclusive end of a
        // Mon..Sun window). Today-Monday 00:00 covers yesterday back 7 days.
        for end.Weekday() != time.Monday {
            end = end.AddDate(0, 0, -1)
        }
        return end.AddDate(0, 0, -7), end, end.Weekday()
    default:
        return end.AddDate(0, 0, -1), end, end.Weekday()
    }
}

// GenerateReport builds and persists one report for the last complete window
// of the period. Idempotent: when a report for (period, range_start) already
// exists it is returned unchanged — the manual admin trigger and the worker
// share this path.
func (s *Service) GenerateReport(ctx context.Context, period string) (*Report, error) {
    if period != "daily" && period != "weekly" {
        return nil, fmt.Errorf("invalid period %q (daily|weekly)", period)
    }
    start, end, _ := reportWindow(period, time.Now())
    var exists bool
    if err := s.pool.QueryRow(ctx,
        `SELECT EXISTS(SELECT 1 FROM fg_reports WHERE period = $1 AND range_start = $2)`,
        period, start).Scan(&exists); err != nil {
        return nil, fmt.Errorf("reports dedupe check: %w", err)
    }
    if exists {
        return s.reportByRange(ctx, period, start)
    }
    data, err := s.fetchReportData(ctx, start, end)
    if err != nil {
        return nil, err
    }
    content := renderReport(period, start, end, data)
    var rep Report
    err = s.pool.QueryRow(ctx,
        `INSERT INTO fg_reports (period, range_start, range_end, content_md, created_at)
         VALUES ($1, $2, $3, $4, now())
         RETURNING id, period, range_start, range_end, created_at`,
        period, start, end, content).Scan(
        &rep.ID, &rep.Period, &rep.RangeStart, &rep.RangeEnd, &rep.CreatedAt)
    if err != nil {
        return nil, fmt.Errorf("reports insert: %w", err)
    }
    rep.ContentMD = content
    rep.ContentBytes = len(content)
    slog.Info("report generated", "period", period,
        "range_start", start.Format(time.RFC3339), "bytes", len(content))
    return &rep, nil
}

// reportByRange fetches an existing report row (dedupe hit path).
func (s *Service) reportByRange(ctx context.Context, period string, start time.Time) (*Report, error) {
    var rep Report
    err := s.pool.QueryRow(ctx,
        `SELECT id, period, range_start, range_end, created_at, octet_length(content_md), content_md
         FROM fg_reports WHERE period = $1 AND range_start = $2`,
        period, start).Scan(&rep.ID, &rep.Period, &rep.RangeStart, &rep.RangeEnd,
        &rep.CreatedAt, &rep.ContentBytes, &rep.ContentMD)
    if err != nil {
        return nil, fmt.Errorf("reports fetch: %w", err)
    }
    return &rep, nil
}

// Reports lists generated reports, newest first. period is an exact-match
// optional filter; limit defaults to 50, caps at 200. List rows omit
// content_md — the detail endpoint serves it.
func (s *Service) Reports(ctx context.Context, period string, limit int) ([]Report, error) {
    if limit <= 0 {
        limit = 50
    }
    if limit > 200 {
        limit = 200
    }
    where := ""
    args := []any{}
    if period != "" {
        args = append(args, period)
        where = fmt.Sprintf(" WHERE period = $%d", len(args))
    }
    args = append(args, limit)
    rows, err := s.pool.Query(ctx,
        `SELECT id, period, range_start, range_end, created_at, octet_length(content_md)
         FROM fg_reports`+where+
        fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args)), args...)
    if err != nil {
        return nil, fmt.Errorf("reports query: %w", err)
    }
    defer rows.Close()
    out := []Report{}
    for rows.Next() {
        var r Report
        if err := rows.Scan(&r.ID, &r.Period, &r.RangeStart, &r.RangeEnd,
            &r.CreatedAt, &r.ContentBytes); err != nil {
            return nil, fmt.Errorf("reports scan: %w", err)
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// ReportByID fetches a single report including content_md.
func (s *Service) ReportByID(ctx context.Context, id int64) (*Report, error) {
    var r Report
    err := s.pool.QueryRow(ctx,
        `SELECT id, period, range_start, range_end, created_at, octet_length(content_md), content_md
         FROM fg_reports WHERE id = $1`, id).Scan(
        &r.ID, &r.Period, &r.RangeStart, &r.RangeEnd,
        &r.CreatedAt, &r.ContentBytes, &r.ContentMD)
    if err != nil {
        return nil, fmt.Errorf("report fetch: %w", err)
    }
    return &r, nil
}

// writeFile drops the file copy under the worker dir. Best-effort: the DB row
// is authoritative, a failed file write is a WARN not an error.
func (rw *ReportsWorker) writeFile(rep *Report) {
    if rw.dir == "" || rep.ContentMD == "" {
        return
    }
    if err := os.MkdirAll(rw.dir, 0o755); err != nil {
        slog.Warn("reports file copy mkdir failed", "dir", rw.dir, "error", err)
        return
    }
    name := fmt.Sprintf("report-%s-%s.md", rep.Period, rep.RangeStart.Format("2006-01-02"))
    path := filepath.Join(rw.dir, name)
    if err := os.WriteFile(path, []byte(rep.ContentMD), 0o644); err != nil {
        slog.Warn("reports file copy write failed", "path", path, "error", err)
        return
    }
    slog.Info("report file written", "path", path, "bytes", rep.ContentBytes)
}

// ---- report data fetch ----

// totalsRow is the volume + latency headline of one window.
type totalsRow struct {
    Requests   int64
    SuccessPct float64
    TTFTP95    float64
}

// worstModelRow is one high-latency model (TTFT P95, worst first).
type worstModelRow struct {
    Model string
    N     int64
    P95   float64
}

// anomalyKindRow is one anomaly kind detected in the window.
type anomalyKindRow struct {
    Kind     string
    N        int64
    Severity string
}

// costRow aggregates the money columns for the window.
type costRow struct {
    CloudCost    float64
    LocalSavings float64
    WasteCost    float64
}

// findingRow is one optimization finding lifecycle event in the window.
type findingRow struct {
    Rule    string
    Scope   string
    Status  string // new|applied in this window
    Model   string // extracted for the applied follow-up
    LocalN  int64  // follow-up: 7-day local requests (applied findings only)
    CloudN  int64  // follow-up: 7-day cloud requests
}

// reportData is everything one report renders; all fetches already merged.
type reportData struct {
    Cur, Prev       totalsRow
    WorstModels     []worstModelRow
    OpenAnomalies   int64
    AnomaliesByKind []anomalyKindRow
    Cost            costRow
    Findings        []findingRow
}

// fetchReportData runs the section queries for the window (and the previous
// window for the deltas). Each section fails soft: a broken query must not
// kill the whole report — the section renders with zeros and a note.
func (s *Service) fetchReportData(ctx context.Context, start, end time.Time) (*reportData, error) {
    d := &reportData{}
    var err error
    if d.Cur, err = s.fetchTotals(ctx, start, end); err != nil {
        slog.Warn("report totals fetch failed", "error", err)
    }
    if d.Prev, err = s.fetchTotals(ctx, start.Add(-end.Sub(start)), start); err != nil {
        slog.Warn("report previous-totals fetch failed", "error", err)
    }
    if d.WorstModels, err = s.fetchWorstModels(ctx, start, end); err != nil {
        slog.Warn("report worst-models fetch failed", "error", err)
    }
    if err = s.pool.QueryRow(ctx,
        `SELECT count(*) FROM fg_anomalies WHERE status = 'open'`).Scan(&d.OpenAnomalies); err != nil {
        slog.Warn("report open-anomalies fetch failed", "error", err)
    }
    if d.AnomaliesByKind, err = s.fetchAnomalyKinds(ctx, start, end); err != nil {
        slog.Warn("report anomaly-kinds fetch failed", "error", err)
    }
    if err = s.pool.QueryRow(ctx,
        `SELECT COALESCE(sum(cost), 0), COALESCE(sum(local_savings), 0),
                COALESCE(sum(cost) FILTER (WHERE input_tokens > 0 AND output_tokens > 0
                    AND input_tokens > 50 * output_tokens), 0)
         FROM fg_request_logs WHERE ts >= $1 AND ts < $2`, start, end).
        Scan(&d.Cost.CloudCost, &d.Cost.LocalSavings, &d.Cost.WasteCost); err != nil {
        slog.Warn("report cost fetch failed", "error", err)
    }
    if d.Findings, err = s.fetchFindingsForReport(ctx, start, end); err != nil {
        slog.Warn("report findings fetch failed", "error", err)
    }
    return d, nil
}

func (s *Service) fetchTotals(ctx context.Context, from, to time.Time) (totalsRow, error) {
    var t totalsRow
    err := s.pool.QueryRow(ctx,
        `SELECT count(*),
                COALESCE(avg((is_success)::int) * 100, 0),
                COALESCE((SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms)
                          FROM fg_request_logs
                          WHERE ts >= $1 AND ts < $2 AND ttft_ms > 0), 0)
         FROM fg_request_logs WHERE ts >= $1 AND ts < $2`, from, to).
        Scan(&t.Requests, &t.SuccessPct, &t.TTFTP95)
    return t, err
}

func (s *Service) fetchWorstModels(ctx context.Context, from, to time.Time) ([]worstModelRow, error) {
    rows, err := s.pool.Query(ctx,
        `SELECT model, count(*),
                percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms) AS p95
         FROM fg_request_logs
         WHERE ts >= $1 AND ts < $2 AND ttft_ms > 0
         GROUP BY model HAVING count(*) >= 10
         ORDER BY p95 DESC LIMIT 3`, from, to)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []worstModelRow{}
    for rows.Next() {
        var r worstModelRow
        if err := rows.Scan(&r.Model, &r.N, &r.P95); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

func (s *Service) fetchAnomalyKinds(ctx context.Context, from, to time.Time) ([]anomalyKindRow, error) {
    rows, err := s.pool.Query(ctx,
        `SELECT kind, count(*), max(severity)
         FROM fg_anomalies WHERE ts >= $1 AND ts < $2
         GROUP BY kind ORDER BY count(*) DESC`, from, to)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []anomalyKindRow{}
    for rows.Next() {
        var r anomalyKindRow
        if err := rows.Scan(&r.Kind, &r.N, &r.Severity); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetchFindingsForReport pulls the window's finding lifecycle: new findings
// generated in the window, plus applied findings (applied_at in window) with
// the 7-day local/cloud split of their model — the 采纳回访 follow-up
// (plan §15.3): did the supply entry actually move traffic local?
func (s *Service) fetchFindingsForReport(ctx context.Context, from, to time.Time) ([]findingRow, error) {
    rows, err := s.pool.Query(ctx,
        `SELECT rule, COALESCE(scope::text, ''), status,
                COALESCE(scope->>'model', ''),
                count(*) FILTER (WHERE backend_type = 'local'),
                count(*) FILTER (WHERE backend_type = 'cloud')
         FROM (
             SELECT rule, scope, 'applied' AS status, applied_at AS at
               FROM fg_optimization_findings
              WHERE applied_at >= $1 AND applied_at < $2
             UNION ALL
             SELECT rule, scope, 'new', generated_at
               FROM fg_optimization_findings
              WHERE generated_at >= $1 AND generated_at < $2
         ) f
         LEFT JOIN fg_request_logs l
           ON f.status = 'applied'
          AND COALESCE(f.scope->>'model', '') <> ''
          AND l.model = f.scope->>'model'
          AND l.ts >= now() - interval '7 days'
         GROUP BY rule, COALESCE(scope::text, ''), status, COALESCE(scope->>'model', '')
         ORDER BY status, rule`, from, to)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []findingRow{}
    for rows.Next() {
        var r findingRow
        if err := rows.Scan(&r.Rule, &r.Scope, &r.Status, &r.Model, &r.LocalN, &r.CloudN); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// ---- render (pure, unit-tested) ----

// pctDelta formats "cur vs prev" as a signed percentage-point / percent
// delta string; prev==0 renders "n/a" (first period after deploy).
func pctDelta(cur, prev float64) string {
    if prev == 0 {
        return "n/a"
    }
    return fmt.Sprintf("%+.1f%%", (cur-prev)/prev*100)
}

func ppDelta(cur, prev float64) string {
    return fmt.Sprintf("%+.2f pp", cur-prev)
}

// renderReport builds the Markdown body (plan §15.3 template).
func renderReport(period string, start, end time.Time, d *reportData) string {
    var b strings.Builder
    title := "Daily"
    if period == "weekly" {
        title = "Weekly"
    }
    fmt.Fprintf(&b, "# Fusion Gateway %s Report\n\n", title)
    fmt.Fprintf(&b, "- Window: %s → %s\n", start.Format("2006-01-02 15:04 MST"), end.Format("2006-01-02 15:04 MST"))
    fmt.Fprintf(&b, "- Generated: %s\n\n", time.Now().Format("2006-01-02 15:04 MST"))

    b.WriteString("## 总量\n\n")
    fmt.Fprintf(&b, "- Requests: **%d** (vs previous %s)\n",
        d.Cur.Requests, pctDelta(float64(d.Cur.Requests), float64(d.Prev.Requests)))
    fmt.Fprintf(&b, "- Success rate: **%.2f%%** (%s)\n\n",
        d.Cur.SuccessPct, ppDelta(d.Cur.SuccessPct, d.Prev.SuccessPct))

    b.WriteString("## 延迟\n\n")
    fmt.Fprintf(&b, "- Global TTFT P95: **%.0f ms** (vs previous %s)\n",
        d.Cur.TTFTP95, pctDelta(d.Cur.TTFTP95, d.Prev.TTFTP95))
    if len(d.WorstModels) == 0 {
        b.WriteString("- No model reached 10 generation samples — latency ranking skipped.\n")
    } else {
        b.WriteString("- Worst models by TTFT P95:\n\n")
        b.WriteString("| Model | Requests | TTFT P95 (ms) |\n|---|---|---|\n")
        for _, m := range d.WorstModels {
            fmt.Fprintf(&b, "| %s | %d | %.0f |\n", m.Model, m.N, m.P95)
        }
    }
    b.WriteString("\n")

    b.WriteString("## 异常\n\n")
    fmt.Fprintf(&b, "- Open anomalies now: **%d**\n", d.OpenAnomalies)
    if len(d.AnomaliesByKind) == 0 {
        b.WriteString("- No anomalies detected in this window.\n")
    } else {
        b.WriteString("- Detected in this window:\n\n")
        b.WriteString("| Kind | Count | Max severity |\n|---|---|---|\n")
        for _, a := range d.AnomaliesByKind {
            fmt.Fprintf(&b, "| %s | %d | %s |\n", a.Kind, a.N, a.Severity)
        }
    }
    b.WriteString("\n")

    b.WriteString("## 成本\n\n")
    fmt.Fprintf(&b, "- Cloud cost: **%.4f**\n", d.Cost.CloudCost)
    fmt.Fprintf(&b, "- Local savings (estimated): **%.4f**\n", d.Cost.LocalSavings)
    fmt.Fprintf(&b, "- Waste estimate (oversized prompts, input > 50x output): **%.4f**\n\n", d.Cost.WasteCost)

    b.WriteString("## 优化\n\n")
    if len(d.Findings) == 0 {
        b.WriteString("- No optimization findings in this window.\n")
    } else {
        for _, f := range d.Findings {
            if f.Status == "new" {
                fmt.Fprintf(&b, "- New finding `%s` — %s\n", f.Rule, f.Scope)
                continue
            }
            // 采纳回访: applied finding + its 7-day local/cloud split.
            total := f.LocalN + f.CloudN
            if total > 0 {
                fmt.Fprintf(&b, "- Applied `%s` (%s) — follow-up: %d requests in 7d, %.1f%% local / %.1f%% cloud\n",
                    f.Rule, f.Model, total,
                    float64(f.LocalN)/float64(total)*100, float64(f.CloudN)/float64(total)*100)
            } else {
                fmt.Fprintf(&b, "- Applied `%s` (%s) — no traffic for this model in the last 7d\n",
                    f.Rule, f.Model)
            }
        }
    }
    return b.String()
}
