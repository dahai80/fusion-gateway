// Error + spend aggregations (#564): PG replaces the deprecated in-memory
// error ring, and per-key spend gives the operator the money view. Both are
// read-time aggregates over fg_request_logs — no migrations, endpoint-scoped
// like Overview (llm default, all for the raw audit view).
package insights

import (
    "context"
    "fmt"
    "sort"
)

// ErrorRow is one endpoint × status × message group of failures.
type ErrorRow struct {
    Endpoint    string `json:"endpoint"`
    StatusCode  int    `json:"status_code"`
    ErrorMessage string `json:"error_message"`
    Count       int64  `json:"count"`
    LastSeen    string `json:"last_seen"`
}

// SpendRow is one API key's window spend (priced at read time).
type SpendRow struct {
    KeyName     string  `json:"key_name"`
    Requests    int64   `json:"requests"`
    Errors      int64   `json:"errors"`
    SuccessRate float64 `json:"success_rate"`
    InputTokens int64   `json:"input_tokens"`
    OutputTokens int64  `json:"output_tokens"`
    TotalTokens int64   `json:"total_tokens"`
    Cost        float64 `json:"cost"`
}

// ErrorBreakdown groups failed requests by endpoint, status code, and
// message over the window (top `limit`, count desc). error_message may be
// empty (timeouts without a message) — grouped as "".
func (s *Service) ErrorBreakdown(ctx context.Context, fromStr, toStr, scope string, limit int) ([]ErrorRow, error) {
    if limit <= 0 {
        limit = 20
    }
    if limit > 100 {
        limit = 100
    }
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, fmt.Errorf("error breakdown window: %w", err)
    }
    rows, err := s.pool.Query(ctx, `
        SELECT endpoint, status_code, coalesce(error_message, ''), count(*),
               to_char(max(ts), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
        FROM fg_request_logs
        WHERE NOT is_success AND ts >= $1 AND ts < $2`+scopeClause(scope)+`
        GROUP BY 1, 2, 3
        ORDER BY 4 DESC
        LIMIT $3`, from, to, limit)
    if err != nil {
        return nil, fmt.Errorf("error breakdown query: %w", err)
    }
    defer rows.Close()
    out := []ErrorRow{}
    for rows.Next() {
        var r ErrorRow
        if err := rows.Scan(&r.Endpoint, &r.StatusCode, &r.ErrorMessage, &r.Count, &r.LastSeen); err != nil {
            return nil, fmt.Errorf("error breakdown scan: %w", err)
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// SpendByKey aggregates per api_key_name over the window, priced per model
// (pricing entry beats the stored cost column), sorted by cost desc. The
// per-key pricing pass needs per-(key, model) token sums first.
func (s *Service) SpendByKey(ctx context.Context, fromStr, toStr, scope string, limit int) ([]SpendRow, error) {
    if limit <= 0 {
        limit = 20
    }
    if limit > 100 {
        limit = 100
    }
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, fmt.Errorf("spend window: %w", err)
    }
    rows, err := s.pool.Query(ctx, `
        SELECT coalesce(api_key_name, '') AS key_name, model, count(*),
               count(*) FILTER (WHERE NOT is_success),
               coalesce(sum(input_tokens), 0),
               coalesce(sum(output_tokens), 0),
               coalesce(sum(total_tokens), 0),
               coalesce(sum(cost), 0)
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2`+scopeClause(scope)+`
        GROUP BY 1, 2`, from, to)
    if err != nil {
        return nil, fmt.Errorf("spend query: %w", err)
    }
    defer rows.Close()
    table := s.pricingTable()
    byKey := map[string]*SpendRow{}
    var order []string
    for rows.Next() {
        var key, model string
        var reqs, errs, inTok, outTok, totTok int64
        var storedCost float64
        if err := rows.Scan(&key, &model, &reqs, &errs, &inTok, &outTok, &totTok, &storedCost); err != nil {
            return nil, fmt.Errorf("spend scan: %w", err)
        }
        r, ok := byKey[key]
        if !ok {
            r = &SpendRow{KeyName: key}
            byKey[key] = r
            order = append(order, key)
        }
        r.Requests += reqs
        r.Errors += errs
        r.InputTokens += inTok
        r.OutputTokens += outTok
        r.TotalTokens += totTok
        r.Cost += costFor(table, model, inTok, outTok, storedCost)
    }
    if err := rows.Err(); err != nil {
        return nil, err
    }
    out := make([]SpendRow, 0, len(order))
    for _, k := range order {
        r := byKey[k]
        if r.Requests > 0 {
            r.SuccessRate = float64(r.Requests-r.Errors) / float64(r.Requests)
        }
        out = append(out, *r)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
    if len(out) > limit {
        out = out[:limit]
    }
    return out, nil
}
