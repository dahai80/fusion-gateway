package insights

import (
    "context"
    "time"
)

// LatencyBucket is one time-bucket of the latency timeseries (plan §7.1).
type LatencyBucket struct {
    Bucket       string  `json:"bucket"`
    Requests     int64   `json:"requests"`
    TTFTP50MS    float64 `json:"ttft_p50_ms"`
    TTFTP95MS    float64 `json:"ttft_p95_ms"`
    TPOTP50MS    float64 `json:"tpot_p50_ms"`
    TPOTP95MS    float64 `json:"tpot_p95_ms"`
    AvgDuration  float64 `json:"avg_duration_ms"`
    ErrorCount   int64   `json:"error_count"`
}

// LatencyResponse is the /admin/api/analytics/latency payload.
type LatencyResponse struct {
    From         string         `json:"from"`
    To           string         `json:"to"`
    Granularity  string         `json:"granularity"`
    Backend      string         `json:"backend,omitempty"`
    Model        string         `json:"model,omitempty"`
    ProjectID    string         `json:"project_id,omitempty"`
    Series       []LatencyBucket `json:"series"`
}

// Latency runs the TTFT/TPOT percentile aggregation over the window.
func (s *Service) Latency(ctx context.Context, fromStr, toStr, gran, backend, model, projectID string) (*LatencyResponse, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, err
    }
    unit, err := granularity(gran)
    if err != nil {
        return nil, err
    }
    // Generation rows only: every generation path stamps ttft_ms, and the Go
    // zero value persists as 0.0 (never NULL) — so ttft_ms > 0 excludes
    // probes (/v1/models etc.) that carry no latency semantics.
    rows, err := s.pool.Query(ctx, `
        SELECT to_char(date_trunc($1, ts), 'YYYY-MM-DD"T"HH24:MI:SS') AS bucket,
               count(*) AS requests,
               COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY ttft_ms), 0),
               COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms), 0),
               COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY tpot_ms) FILTER (WHERE tpot_ms > 0), 0),
               COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY tpot_ms) FILTER (WHERE tpot_ms > 0), 0),
               COALESCE(avg(duration_ms) FILTER (WHERE ttft_ms > 0), 0),
               count(*) FILTER (WHERE NOT is_success)
        FROM fg_request_logs
        WHERE ts >= $2 AND ts < $3
          AND ttft_ms > 0
          AND ($4 = '' OR backend = $4)
          AND ($5 = '' OR model = $5)
          AND ($6 = '' OR project_id = $6)
        GROUP BY 1
        ORDER BY 1`, unit, from, to, backend, model, projectID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    resp := &LatencyResponse{
        From:        from.Format(time.RFC3339),
        To:          to.Format(time.RFC3339),
        Granularity: unit,
        Backend:     backend,
        Model:       model,
        ProjectID:   projectID,
        Series:      []LatencyBucket{},
    }
    for rows.Next() {
        var b LatencyBucket
        if err := rows.Scan(&b.Bucket, &b.Requests, &b.TTFTP50MS, &b.TTFTP95MS,
            &b.TPOTP50MS, &b.TPOTP95MS, &b.AvgDuration, &b.ErrorCount); err != nil {
            return nil, err
        }
        resp.Series = append(resp.Series, b)
    }
    return resp, rows.Err()
}
