// #421: persistent Dashboard aggregate — one windowed rollup over
// fg_request_logs plus gateway-cache deltas from fg_cache_stats. Serves
// /admin/api/insights/overview.
package insights

import (
    "context"
    "sort"
    "time"
)

// OverviewTotals is the headline KPI block of the Dashboard.
type OverviewTotals struct {
    Requests     int64   `json:"requests"`
    Successes    int64   `json:"successes"`
    Errors       int64   `json:"errors"`
    SuccessRate  float64 `json:"success_rate"`
    InputTokens  int64   `json:"input_tokens"`
    OutputTokens int64   `json:"output_tokens"`
    CachedTokens int64   `json:"cached_tokens"`
    TotalTokens  int64   `json:"total_tokens"`
    Cost         float64 `json:"cost"`
    LocalSavings float64 `json:"local_savings"`
}

// OverviewLatency carries the window's duration/TTFT percentiles (ms).
// Rows without a measured value are excluded per metric.
type OverviewLatency struct {
    DurationP50 float64 `json:"duration_p50_ms"`
    DurationP95 float64 `json:"duration_p95_ms"`
    DurationP99 float64 `json:"duration_p99_ms"`
    TTFTP50     float64 `json:"ttft_p50_ms"`
    TTFTP95     float64 `json:"ttft_p95_ms"`
    TTFTP99     float64 `json:"ttft_p99_ms"`
}

// OverviewCache is one gateway cache's per-window hit/miss delta. The stored
// counters are cumulative and reset to zero on every gateway restart, so the
// window activity is the SUM of adjacent-sample increments: each run segment
// between restarts contributes its own growth (a wrap — cur < prev — counts
// the new run's value from zero). Only the sub-interval before the first
// sample is lost.
type OverviewCache struct {
    Cache   string  `json:"cache"`
    Hits    int64   `json:"hits"`
    Misses  int64   `json:"misses"`
    HitRate float64 `json:"hit_rate"`
    Size    int64   `json:"size"`
    Samples int64   `json:"samples"`
}

// OverviewModel is a top-N aggregation row (models / backends).
type OverviewModel struct {
    Name        string  `json:"name"`
    Requests    int64   `json:"requests"`
    TotalTokens int64   `json:"total_tokens"`
    Cost        float64 `json:"cost"`
}

// OverviewBucket is one daily/hourly series point of the trend chart.
type OverviewBucket struct {
    Bucket       string `json:"bucket"`
    Requests     int64  `json:"requests"`
    Errors       int64  `json:"errors"`
    InputTokens  int64  `json:"input_tokens"`
    OutputTokens int64  `json:"output_tokens"`
    CachedTokens int64  `json:"cached_tokens"`
}

// OverviewResponse is the /admin/api/insights/overview payload.
type OverviewResponse struct {
    From          string          `json:"from"`
    To            string          `json:"to"`
    Granularity   string          `json:"granularity"`
    Totals        OverviewTotals  `json:"totals"`
    Latency       OverviewLatency `json:"latency"`
    GatewayCaches []OverviewCache `json:"gateway_caches"`
    TopModels     []OverviewModel `json:"top_models"`
    TopBackends   []OverviewModel `json:"top_backends"`
    Series        []OverviewBucket `json:"series"`
}

// Overview aggregates the window in five targeted queries (totals+latency,
// cache deltas, top models, top backends, series) — same map-merge economy
// as Timeline, no mega-JOIN.
func (s *Service) Overview(ctx context.Context, fromStr, toStr, gran string) (*OverviewResponse, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, err
    }
    unit, err := granularity(gran)
    if err != nil {
        return nil, err
    }
    resp := &OverviewResponse{
        From:        from.Format(time.RFC3339),
        To:          to.Format(time.RFC3339),
        Granularity: unit,
    }

    // Totals + latency percentiles in one pass.
    row := s.pool.QueryRow(ctx, `
        SELECT count(*),
               count(*) FILTER (WHERE is_success),
               count(*) FILTER (WHERE NOT is_success),
               coalesce(sum(input_tokens), 0),
               coalesce(sum(output_tokens), 0),
               coalesce(sum(cached_tokens), 0),
               coalesce(sum(total_tokens), 0),
               coalesce(sum(cost), 0),
               coalesce(sum(local_savings), 0),
               coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms > 0), 0),
               coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms > 0), 0),
               coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms > 0), 0),
               coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms > 0), 0),
               coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms > 0), 0),
               coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms > 0), 0)
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2`, from, to)
    if err := row.Scan(&resp.Totals.Requests, &resp.Totals.Successes, &resp.Totals.Errors,
        &resp.Totals.InputTokens, &resp.Totals.OutputTokens, &resp.Totals.CachedTokens,
        &resp.Totals.TotalTokens, &resp.Totals.Cost, &resp.Totals.LocalSavings,
        &resp.Latency.DurationP50, &resp.Latency.DurationP95, &resp.Latency.DurationP99,
        &resp.Latency.TTFTP50, &resp.Latency.TTFTP95, &resp.Latency.TTFTP99); err != nil {
        return nil, err
    }
    if resp.Totals.Requests > 0 {
        resp.Totals.SuccessRate = float64(resp.Totals.Successes) / float64(resp.Totals.Requests)
    }

    // Gateway cache deltas (#421/#427): SUM of adjacent-sample increments with
    // wrap detection. The stored counters are cumulative but reset to zero on
    // every gateway restart, so a first-vs-last subtraction discards the whole
    // pre-restart segment when a restart lands inside the window (and shows 0
    // for both caches right after one). lag() walks consecutive samples per
    // cache: normal growth contributes cur-prev; a wrap (cur < prev, i.e. a
    // restart between samples) contributes the new run's value; the first row
    // has no baseline and contributes 0 (sub-30s pre-window activity is lost,
    // same as before).
    rows, err := s.pool.Query(ctx, `
        WITH s AS (
            SELECT cache, hits, misses, size,
                   lag(hits)   OVER (PARTITION BY cache ORDER BY ts) AS prev_hits,
                   lag(misses) OVER (PARTITION BY cache ORDER BY ts) AS prev_misses
            FROM fg_cache_stats
            WHERE ts >= $1 AND ts < $2
        )
        SELECT cache,
               coalesce(sum(CASE WHEN prev_hits IS NULL THEN 0
                                 WHEN hits >= prev_hits THEN hits - prev_hits
                                 ELSE hits END), 0),
               coalesce(sum(CASE WHEN prev_misses IS NULL THEN 0
                                 WHEN misses >= prev_misses THEN misses - prev_misses
                                 ELSE misses END), 0),
               max(size),
               count(*)
        FROM s
        GROUP BY cache`, from, to)
    if err != nil {
        return nil, err
    }
    for rows.Next() {
        var c OverviewCache
        if err := rows.Scan(&c.Cache, &c.Hits, &c.Misses, &c.Size, &c.Samples); err != nil {
            rows.Close()
            return nil, err
        }
        if c.Hits+c.Misses > 0 {
            c.HitRate = float64(c.Hits) / float64(c.Hits+c.Misses)
        }
        resp.GatewayCaches = append(resp.GatewayCaches, c)
    }
    rows.Close()
    if err := rows.Err(); err != nil {
        return nil, err
    }
    sort.Slice(resp.GatewayCaches, func(i, j int) bool { return resp.GatewayCaches[i].Cache < resp.GatewayCaches[j].Cache })

    resp.TopModels, err = s.overviewTopN(ctx, from, to, `model`)
    if err != nil {
        return nil, err
    }
    resp.TopBackends, err = s.overviewTopN(ctx, from, to, `backend`)
    if err != nil {
        return nil, err
    }

    // Trend series.
    const bucketFmt = `to_char(date_trunc($1, ts), 'YYYY-MM-DD"T"HH24:MI:SS')`
    rows, err = s.pool.Query(ctx, `
        SELECT `+bucketFmt+`, count(*),
               count(*) FILTER (WHERE NOT is_success),
               coalesce(sum(input_tokens), 0),
               coalesce(sum(output_tokens), 0),
               coalesce(sum(cached_tokens), 0)
        FROM fg_request_logs
        WHERE ts >= $2 AND ts < $3
        GROUP BY 1`, unit, from, to)
    if err != nil {
        return nil, err
    }
    for rows.Next() {
        var b OverviewBucket
        if err := rows.Scan(&b.Bucket, &b.Requests, &b.Errors,
            &b.InputTokens, &b.OutputTokens, &b.CachedTokens); err != nil {
            rows.Close()
            return nil, err
        }
        resp.Series = append(resp.Series, b)
    }
    rows.Close()
    if err := rows.Err(); err != nil {
        return nil, err
    }
    sort.Slice(resp.Series, func(i, j int) bool { return resp.Series[i].Bucket < resp.Series[j].Bucket })
    return resp, nil
}

// overviewTopN ranks models or backends by request volume, top 10.
func (s *Service) overviewTopN(ctx context.Context, from, to time.Time, col string) ([]OverviewModel, error) {
    rows, err := s.pool.Query(ctx, `
        SELECT `+col+`, count(*), coalesce(sum(total_tokens), 0), coalesce(sum(cost), 0)
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2
        GROUP BY 1
        ORDER BY count(*) DESC
        LIMIT 10`, from, to)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []OverviewModel{}
    for rows.Next() {
        var m OverviewModel
        if err := rows.Scan(&m.Name, &m.Requests, &m.TotalTokens, &m.Cost); err != nil {
            return nil, err
        }
        out = append(out, m)
    }
    return out, rows.Err()
}
