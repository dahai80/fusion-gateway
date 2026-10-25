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

// CompareResponse is the /admin/api/analytics/compare payload.
type CompareResponse struct {
    From string       `json:"from"`
    To   string       `json:"to"`
    Rows []CompareRow `json:"rows"`
}

// Compare aggregates per backend×model over the window so the dashboard can
// put providers side by side (plan §7.2). Generation rows only.
func (s *Service) Compare(ctx context.Context, fromStr, toStr, projectID string) (*CompareResponse, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, err
    }
    rows, err := s.pool.Query(ctx, `
        SELECT backend,
               COALESCE(backend_type, '') AS backend_type,
               model,
               count(*),
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
        GROUP BY backend, backend_type, model
        ORDER BY requests DESC`, from, to, projectID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    resp := &CompareResponse{
        From: from.Format(time.RFC3339),
        To:   to.Format(time.RFC3339),
        Rows: []CompareRow{},
    }
    for rows.Next() {
        var r CompareRow
        if err := rows.Scan(&r.Backend, &r.BackendType, &r.Model, &r.Requests,
            &r.SuccessRate, &r.TTFTP50MS, &r.TTFTP95MS, &r.TPOTP50MS, &r.TPOTP95MS,
            &r.TokensPerSec, &r.AvgInputTok, &r.AvgOutputTok, &r.CostTotal); err != nil {
            return nil, err
        }
        resp.Rows = append(resp.Rows, r)
    }
    return resp, rows.Err()
}
