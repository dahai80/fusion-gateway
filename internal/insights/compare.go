package insights

import (
    "context"
    "time"
)

// CompareRow is one backend×model cell of the provider comparison (plan §7.2).
type CompareRow struct {
    Backend      string  `json:"backend"`
    BackendType  string  `json:"backend_type"`
    Model        string  `json:"model"`
    Requests     int64   `json:"requests"`
    SuccessRate  float64 `json:"success_rate"`
    TTFTP50MS    float64 `json:"ttft_p50_ms"`
    TTFTP95MS    float64 `json:"ttft_p95_ms"`
    TPOTP50MS    float64 `json:"tpot_p50_ms"`
    TPOTP95MS    float64 `json:"tpot_p95_ms"`
    TokensPerSec float64 `json:"avg_tokens_per_sec"`
    AvgInputTok  float64 `json:"avg_input_tokens"`
    AvgOutputTok float64 `json:"avg_output_tokens"`
    CostTotal    float64 `json:"cost_total"`
}

// BreakerTripCount is one backend's circuit-breaker trip tally for the
// window (AC4: the compare table carries a breaker-trips line so a provider
// with great latency but constant breaker open/close cycles is not read as
// healthy).
type BreakerTripCount struct {
    Backend string `json:"backend"`
    Reason  string `json:"reason"`
    Count   int64  `json:"count"`
}

// CompareResponse is the /admin/api/insights/compare payload.
type CompareResponse struct {
    From string       `json:"from"`
    To   string       `json:"to"`
    // Overlap reports whether the overlap filter was applied (AC5: only
    // time buckets where EVERY returned group has traffic count towards the
    // aggregates, so providers are compared over shared load, not each one's
    // own peak hours).
    Overlap bool         `json:"overlap"`
    Rows    []CompareRow `json:"rows"`
    // BreakerTrips counts breaker_tripped gateway events per backend+reason
    // in the window (AC4). Keyed off fg_gateway_events detail, not the live
    // breaker state — the table shows history, not the current open/closed.
    BreakerTrips []BreakerTripCount `json:"breaker_trips"`
}

// compareAggregate runs the backend×model aggregation. keepBuckets nil/empty
// means no bucket restriction; otherwise only rows in those hourly buckets
// count. inputMin/inputMax <=0 mean unbounded (AC5 prompt-length bucket).
func (s *Service) compareAggregate(ctx context.Context, from, to time.Time,
    projectID string, inputMin, inputMax int, keepBuckets []time.Time) ([]CompareRow, error) {
    args := []any{from, to, projectID, inputMin, inputMax}
    bucketFilter := ""
    if len(keepBuckets) > 0 {
        bucketFilter = ` AND date_trunc('hour', ts) = ANY($6::timestamptz[])`
        args = append(args, keepBuckets)
    }
    rows, err := s.pool.Query(ctx, `
        SELECT backend,
               COALESCE(backend_type, '') AS backend_type,
               model,
               count(*) AS requests,
               COALESCE(avg((is_success)::int) * 100, 0),
               COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY ttft_ms), 0),
               COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms), 0),
               COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY tpot_ms) FILTER (WHERE tpot_ms > 0), 0),
               COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY tpot_ms) FILTER (WHERE tpot_ms > 0), 0),
               COALESCE(avg(tokens_per_sec) FILTER (WHERE tokens_per_sec > 0), 0),
               COALESCE(avg(input_tokens), 0),
               COALESCE(avg(output_tokens), 0),
               COALESCE(sum(cost), 0)
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2
          AND ttft_ms > 0
          AND ($3 = '' OR project_id = $3)
          AND ($4 <= 0 OR input_tokens >= $4)
          AND ($5 <= 0 OR input_tokens <= $5)`+bucketFilter+`
        GROUP BY backend, backend_type, model
        ORDER BY requests DESC`, args...)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []CompareRow{}
    for rows.Next() {
        var r CompareRow
        if err := rows.Scan(&r.Backend, &r.BackendType, &r.Model, &r.Requests,
            &r.SuccessRate, &r.TTFTP50MS, &r.TTFTP95MS, &r.TPOTP50MS, &r.TPOTP95MS,
            &r.TokensPerSec, &r.AvgInputTok, &r.AvgOutputTok, &r.CostTotal); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// compareOverlapBuckets returns the hourly buckets where EVERY group in rows
// has at least one request — the shared-load window (AC5).
func (s *Service) compareOverlapBuckets(ctx context.Context, from, to time.Time,
    projectID string, inputMin, inputMax int) ([]time.Time, error) {
    rows, err := s.pool.Query(ctx, `
        SELECT date_trunc('hour', ts) AS bucket
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2
          AND ttft_ms > 0
          AND ($3 = '' OR project_id = $3)
          AND ($4 <= 0 OR input_tokens >= $4)
          AND ($5 <= 0 OR input_tokens <= $5)
        GROUP BY 1
        HAVING count(DISTINCT (backend, model)) =
               (SELECT count(DISTINCT (backend, model))
                FROM fg_request_logs g
                WHERE g.ts >= $1 AND g.ts < $2
                  AND g.ttft_ms > 0
                  AND ($3 = '' OR g.project_id = $3)
                  AND ($4 <= 0 OR g.input_tokens >= $4)
                  AND ($5 <= 0 OR g.input_tokens <= $5))
        ORDER BY 1`, from, to, projectID, inputMin, inputMax)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var buckets []time.Time
    for rows.Next() {
        var b time.Time
        if err := rows.Scan(&b); err != nil {
            return nil, err
        }
        buckets = append(buckets, b)
    }
    return buckets, rows.Err()
}

// compareBreakerTrips tallies breaker_tripped events per backend+reason in
// the window (AC4). detail is jsonb: {"backend":..., "reason":...}.
func (s *Service) compareBreakerTrips(ctx context.Context, from, to time.Time) ([]BreakerTripCount, error) {
    rows, err := s.pool.Query(ctx, `
        SELECT COALESCE(detail->>'backend', '') AS backend,
               COALESCE(detail->>'reason', '') AS reason,
               count(*) AS count
        FROM fg_gateway_events
        WHERE ts >= $1 AND ts < $2 AND event = 'breaker_tripped'
        GROUP BY 1, 2
        ORDER BY count DESC`, from, to)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []BreakerTripCount{}
    for rows.Next() {
        var b BreakerTripCount
        if err := rows.Scan(&b.Backend, &b.Reason, &b.Count); err != nil {
            return nil, err
        }
        out = append(out, b)
    }
    return out, rows.Err()
}

// Compare aggregates per backend×model over the window so the dashboard can
// put providers side by side (plan §7.2). Generation rows only. AC5 fairness
// knobs: inputMin/inputMax restrict to a prompt-length bucket; overlap=true
// keeps only hourly buckets where every group has traffic.
func (s *Service) Compare(ctx context.Context, fromStr, toStr, projectID string,
    inputMin, inputMax int, overlap bool) (*CompareResponse, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, err
    }
    var keep []time.Time
    if overlap {
        keep, err = s.compareOverlapBuckets(ctx, from, to, projectID, inputMin, inputMax)
        if err != nil {
            return nil, err
        }
    }
    rows, err := s.compareAggregate(ctx, from, to, projectID, inputMin, inputMax, keep)
    if err != nil {
        return nil, err
    }
    trips, err := s.compareBreakerTrips(ctx, from, to)
    if err != nil {
        return nil, err
    }
    return &CompareResponse{
        From:        from.Format(time.RFC3339),
        To:          to.Format(time.RFC3339),
        Overlap:     overlap,
        Rows:        rows,
        BreakerTrips: trips,
    }, nil
}
