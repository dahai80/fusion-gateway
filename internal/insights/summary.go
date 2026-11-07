// Summary group-by (P2, #564): Portkey-style any-dimension aggregation over
// the request window — requests / errors / success rate / tokens / priced
// cost / duration p95 grouped by one whitelisted column. Dimensions are
// hard-whitelisted: caller input never reaches the SQL splice unguarded.
package insights

import (
    "context"
    "fmt"
    "sort"
)

// summaryColumns maps the public dimension name to the physical column.
// Anything not in this map is rejected by summaryColumn.
var summaryColumns = map[string]string{
    "model":        "model",
    "backend":      "backend",
    "endpoint":     "endpoint",
    "api_key_name": "api_key_name",
    "scenario":     "scenario",
}

// summaryColumn resolves and guards the group-by dimension. An empty
// dimension defaults to model (dashboard default view); unknown names are
// rejected — never interpolated.
func summaryColumn(dimension string) (string, bool) {
    if dimension == "" {
        return "model", true
    }
    col, ok := summaryColumns[dimension]
    return col, ok
}

// SummaryRow is one group's window aggregation (cost priced at read time).
type SummaryRow struct {
    Name          string  `json:"name"`
    Requests      int64   `json:"requests"`
    Errors        int64   `json:"errors"`
    SuccessRate   float64 `json:"success_rate"`
    InputTokens   int64   `json:"input_tokens"`
    OutputTokens  int64   `json:"output_tokens"`
    TotalTokens   int64   `json:"total_tokens"`
    Cost          float64 `json:"cost"`
    DurationP95Ms float64 `json:"duration_p95_ms"`
}

// Summary aggregates the window grouped by one whitelisted dimension,
// cost-desc. The query groups by (dimension, model) so the pricing pass can
// price each group's model mix; the p95 window function is computed per
// dimension before the outer group-by. LLM-scoped like Overview.
func (s *Service) Summary(ctx context.Context, fromStr, toStr, dimension, scope string, limit int) ([]SummaryRow, error) {
    col, ok := summaryColumn(dimension)
    if !ok {
        return nil, fmt.Errorf("unknown group_by dimension %q (allowed: model, backend, endpoint, api_key_name, scenario)", dimension)
    }
    if limit <= 0 {
        limit = 20
    }
    if limit > 100 {
        limit = 100
    }
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, fmt.Errorf("summary window: %w", err)
    }
    rows, err := s.pool.Query(ctx, `
        SELECT `+col+`, model, count(*),
               count(*) FILTER (WHERE NOT is_success),
               coalesce(sum(input_tokens), 0),
               coalesce(sum(output_tokens), 0),
               coalesce(sum(total_tokens), 0),
               coalesce(sum(cost), 0)
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2`+scopeClause(scope)+`
        GROUP BY 1, 2`, from, to)
    if err != nil {
        return nil, fmt.Errorf("summary query: %w", err)
    }
    defer rows.Close()
    table := s.pricingTable()
    // p95 per dimension in its own aggregate (percentile_cont is an
    // ordered-set aggregate — no window form, so it cannot ride the
    // (dimension, model) group-by).
    p95Rows, err := s.pool.Query(ctx, `
        SELECT `+col+`, coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms > 0), 0)
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2`+scopeClause(scope)+`
        GROUP BY 1`, from, to)
    if err != nil {
        return nil, fmt.Errorf("summary p95 query: %w", err)
    }
    defer p95Rows.Close()
    p95By := map[string]float64{}
    for p95Rows.Next() {
        var name string
        var p95 float64
        if err := p95Rows.Scan(&name, &p95); err != nil {
            return nil, fmt.Errorf("summary p95 scan: %w", err)
        }
        p95By[name] = p95
    }
    if err := p95Rows.Err(); err != nil {
        return nil, err
    }
    type acc struct {
        row     SummaryRow
        priced  map[string][2]int64 // model -> (in, out) token sums for priced models
        stored  float64
    }
    byName := map[string]*acc{}
    var order []string
    for rows.Next() {
        var name, model string
        var reqs, errs, inTok, outTok, totTok int64
        var stored float64
        if err := rows.Scan(&name, &model, &reqs, &errs, &inTok, &outTok, &totTok, &stored); err != nil {
            return nil, fmt.Errorf("summary scan: %w", err)
        }
        a, ok := byName[name]
        if !ok {
            a = &acc{priced: map[string][2]int64{}}
            byName[name] = a
            order = append(order, name)
        }
        a.row.Name = name
        a.row.Requests += reqs
        a.row.Errors += errs
        a.row.InputTokens += inTok
        a.row.OutputTokens += outTok
        a.row.TotalTokens += totTok
        a.row.DurationP95Ms = p95By[name]
        if _, priced := priceFor(table, model); priced {
            m := a.priced[model]
            a.priced[model] = [2]int64{m[0] + inTok, m[1] + outTok}
        } else {
            a.stored += stored
        }
    }
    if err := rows.Err(); err != nil {
        return nil, err
    }
    out := make([]SummaryRow, 0, len(order))
    for _, name := range order {
        a := byName[name]
        cost := a.stored
        for model, toks := range a.priced {
            if p, ok := priceFor(table, model); ok {
                cost += float64(toks[0])/1e6*p.InputPerMTok + float64(toks[1])/1e6*p.OutputPerMTok
            }
        }
        a.row.Cost = cost
        if a.row.Requests > 0 {
            a.row.SuccessRate = float64(a.row.Requests-a.row.Errors) / float64(a.row.Requests)
        }
        out = append(out, a.row)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
    if len(out) > limit {
        out = out[:limit]
    }
    return out, nil
}

// RescuedCounts counts gateway strategy interventions in the window
// (Portkey's "rescued by gateway" tile): breaker trips that diverted to
// cloud and cloud-overflow cooldowns. Events come from fg_gateway_events.
type RescuedCounts struct {
    BreakerTrips  int64 `json:"breaker_trips"`
    CloudOverflows int64 `json:"cloud_overflows"`
    Total         int64 `json:"total"`
}

// Rescued tallies breaker_tripped + cloud_overflow events over the window.
func (s *Service) Rescued(ctx context.Context, fromStr, toStr string) (*RescuedCounts, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, fmt.Errorf("rescued window: %w", err)
    }
    rows, err := s.pool.Query(ctx, `
        SELECT event, count(*)
        FROM fg_gateway_events
        WHERE event IN ('breaker_tripped', 'cloud_overflow')
          AND ts >= $1 AND ts < $2
        GROUP BY 1`, from, to)
    if err != nil {
        return nil, fmt.Errorf("rescued query: %w", err)
    }
    defer rows.Close()
    out := &RescuedCounts{}
    for rows.Next() {
        var event string
        var n int64
        if err := rows.Scan(&event, &n); err != nil {
            return nil, fmt.Errorf("rescued scan: %w", err)
        }
        switch event {
        case "breaker_tripped":
            out.BreakerTrips = n
        case "cloud_overflow":
            out.CloudOverflows = n
        }
    }
    if err := rows.Err(); err != nil {
        return nil, err
    }
    out.Total = out.BreakerTrips + out.CloudOverflows
    return out, nil
}
