// Governance report read queries (LiteLLM-gap dashboard wave): PII daily
// counts and rate-limit rejections by key from fg_request_logs. Read-only
// aggregates over the existing persistence schema — no migrations.
package insights

import (
    "context"
    "fmt"
)

// PIIDailyRow is one day's PII-detection count.
type PIIDailyRow struct {
    Day   string `json:"day"`
    Count int64  `json:"count"`
}

// RejectsRow is one key's rejection count over the window.
type RejectsRow struct {
    KeyName string `json:"key_name"`
    Count   int64  `json:"count"`
}

// PIIDaily returns per-day counts of requests flagged contains_pii over the
// last `days` days (default 14, cap 90), oldest first for charting.
func (s *Service) PIIDaily(ctx context.Context, days int) ([]PIIDailyRow, error) {
    if days <= 0 {
        days = 14
    }
    if days > 90 {
        days = 90
    }
    rows, err := s.pool.Query(ctx,
        `SELECT to_char(date_trunc('day', ts), 'YYYY-MM-DD') AS day, count(*)
         FROM fg_request_logs
         WHERE contains_pii AND ts >= now() - ($1 || ' days')::interval
         GROUP BY 1 ORDER BY 1`, days)
    if err != nil {
        return nil, fmt.Errorf("pii daily query: %w", err)
    }
    defer rows.Close()
    out := []PIIDailyRow{}
    for rows.Next() {
        var r PIIDailyRow
        if err := rows.Scan(&r.Day, &r.Count); err != nil {
            return nil, fmt.Errorf("pii daily scan: %w", err)
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// RejectsByKey returns per-key counts of rate-limit rejections (429) over
// the last `days` days (default 7, cap 90), highest count first.
func (s *Service) RejectsByKey(ctx context.Context, days int) ([]RejectsRow, error) {
    if days <= 0 {
        days = 7
    }
    if days > 90 {
        days = 90
    }
    rows, err := s.pool.Query(ctx,
        `SELECT COALESCE(api_key_name, '') AS key_name, count(*)
         FROM fg_request_logs
         WHERE status_code = 429 AND ts >= now() - ($1 || ' days')::interval
         GROUP BY 1 ORDER BY 2 DESC LIMIT 20`, days)
    if err != nil {
        return nil, fmt.Errorf("rejects by key query: %w", err)
    }
    defer rows.Close()
    out := []RejectsRow{}
    for rows.Next() {
        var r RejectsRow
        if err := rows.Scan(&r.KeyName, &r.Count); err != nil {
            return nil, fmt.Errorf("rejects by key scan: %w", err)
        }
        out = append(out, r)
    }
    return out, rows.Err()
}
