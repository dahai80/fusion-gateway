// §15.2 timeline view: per-bucket overlay of request volume, errors, breaker
// trips, and hardware memory ratios — the four tracks the Anomalies page
// charts so an operator can see a breaker trip line up with the memory
// pressure and error spike that caused it.
package insights

import (
    "context"
    "sort"
    "time"
)

// TimelineBucket is one time-bucket of the §15.2 four-track overlay.
type TimelineBucket struct {
    Bucket        string  `json:"bucket"`
    Requests      int64   `json:"requests"`
    Errors        int64   `json:"errors"`
    BreakerEvents int64   `json:"breaker_events"`
    SysMemRatio   float64 `json:"sys_mem_ratio"`
    MlxMemRatio   float64 `json:"mlx_mem_ratio"`
}

// TimelineResponse is the /admin/api/insights/timeline payload.
type TimelineResponse struct {
    From        string           `json:"from"`
    To          string           `json:"to"`
    Granularity string           `json:"granularity"`
    Series      []TimelineBucket `json:"series"`
}

// Timeline merges the four tracks per date_trunc bucket. Unlike Latency it
// counts every fg_request_logs row (probes included) — the timeline is traffic
// volume, not generation latency. Three targeted queries instead of one
// FULL OUTER JOIN: each is independently cheap and the merge is a map union.
func (s *Service) Timeline(ctx context.Context, fromStr, toStr, gran string) (*TimelineResponse, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, err
    }
    unit, err := granularity(gran)
    if err != nil {
        return nil, err
    }

    const bucketFmt = `to_char(date_trunc($1, ts), 'YYYY-MM-DD"T"HH24:MI:SS')`

    buckets := map[string]*TimelineBucket{}
    merge := func() func(bucket string, b *TimelineBucket) {
        return func(bucket string, b *TimelineBucket) {
            if existing, ok := buckets[bucket]; ok {
                existing.Requests += b.Requests
                existing.Errors += b.Errors
                existing.BreakerEvents += b.BreakerEvents
                if b.SysMemRatio > 0 {
                    existing.SysMemRatio = b.SysMemRatio
                }
                if b.MlxMemRatio > 0 {
                    existing.MlxMemRatio = b.MlxMemRatio
                }
                return
            }
            buckets[bucket] = b
        }
    }()

    // Track 1+2: request volume + errors.
    rows, err := s.pool.Query(ctx, `
        SELECT `+bucketFmt+`, count(*), count(*) FILTER (WHERE NOT is_success)
        FROM fg_request_logs
        WHERE ts >= $2 AND ts < $3
        GROUP BY 1`, unit, from, to)
    if err != nil {
        return nil, err
    }
    for rows.Next() {
        var b TimelineBucket
        if err := rows.Scan(&b.Bucket, &b.Requests, &b.Errors); err != nil {
            rows.Close()
            return nil, err
        }
        merge(b.Bucket, &b)
    }
    rows.Close()
    if err := rows.Err(); err != nil {
        return nil, err
    }

    // Track 3: breaker events (breaker_tripped today; LIKE 'breaker%' so any
    // future breaker_recovered/half_open event lands on the same track).
    rows, err = s.pool.Query(ctx, `
        SELECT `+bucketFmt+`, count(*)
        FROM fg_gateway_events
        WHERE ts >= $2 AND ts < $3 AND event LIKE 'breaker%'
        GROUP BY 1`, unit, from, to)
    if err != nil {
        return nil, err
    }
    for rows.Next() {
        var b TimelineBucket
        if err := rows.Scan(&b.Bucket, &b.BreakerEvents); err != nil {
            rows.Close()
            return nil, err
        }
        merge(b.Bucket, &b)
    }
    rows.Close()
    if err := rows.Err(); err != nil {
        return nil, err
    }

    // Track 4: hardware memory ratios (collection-error rows excluded — their
    // zeroed ratios would drag the average down and mask the very pressure
    // the timeline exists to expose).
    rows, err = s.pool.Query(ctx, `
        SELECT `+bucketFmt+`, avg(sys_mem_ratio), avg(mlx_mem_ratio)
        FROM fg_hw_samples
        WHERE ts >= $2 AND ts < $3 AND NOT collection_error
        GROUP BY 1`, unit, from, to)
    if err != nil {
        return nil, err
    }
    for rows.Next() {
        var b TimelineBucket
        if err := rows.Scan(&b.Bucket, &b.SysMemRatio, &b.MlxMemRatio); err != nil {
            rows.Close()
            return nil, err
        }
        merge(b.Bucket, &b)
    }
    rows.Close()
    if err := rows.Err(); err != nil {
        return nil, err
    }

    series := make([]TimelineBucket, 0, len(buckets))
    for _, b := range buckets {
        series = append(series, *b)
    }
    sort.Slice(series, func(i, j int) bool { return series[i].Bucket < series[j].Bucket })
    return &TimelineResponse{
        From:        from.Format(time.RFC3339),
        To:          to.Format(time.RFC3339),
        Granularity: unit,
        Series:      series,
    }, nil
}
