// P3b stage 1 read side (plan §15.5): fg_gateway_events stream and
// fg_hw_samples series for the admin Anomalies/Optimize pages.
package insights

import (
    "context"
    "encoding/json"
    "fmt"
    "time"
)

// GatewayEvent is one fg_gateway_events row.
type GatewayEvent struct {
    ID    int64           `json:"id"`
    TS    time.Time       `json:"ts"`
    Level string          `json:"level"`
    Event string          `json:"event"`
    Detail json.RawMessage `json:"detail,omitempty"`
}

// Events lists the structured gateway event stream, newest first. level and
// event are exact-match optional filters; limit defaults to 100, caps at 1000.
func (s *Service) Events(ctx context.Context, level, event string, limit int) ([]GatewayEvent, error) {
    if limit <= 0 {
        limit = 100
    }
    if limit > 1000 {
        limit = 1000
    }
    where := ""
    args := []any{}
    if level != "" {
        args = append(args, level)
        where = fmt.Sprintf(" WHERE level = $%d", len(args))
    }
    if event != "" {
        args = append(args, event)
        if where == "" {
            where = " WHERE"
        } else {
            where += " AND"
        }
        where += fmt.Sprintf(" event = $%d", len(args))
    }
    args = append(args, limit)
    q := `SELECT id, ts, level, event, detail
          FROM fg_gateway_events` + where +
        fmt.Sprintf(" ORDER BY ts DESC, id DESC LIMIT $%d", len(args))
    rows, err := s.pool.Query(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("events query: %w", err)
    }
    defer rows.Close()
    out := []GatewayEvent{}
    for rows.Next() {
        var e GatewayEvent
        if err := rows.Scan(&e.ID, &e.TS, &e.Level, &e.Event, &e.Detail); err != nil {
            return nil, fmt.Errorf("events scan: %w", err)
        }
        out = append(out, e)
    }
    return out, rows.Err()
}

// HWSampleRow is one fg_hw_samples row (dashboard HW overlay, plan §14).
type HWSampleRow struct {
    TS              time.Time `json:"ts"`
    SysMemRatio     float64   `json:"sys_mem_ratio"`
    MlxMemRatio     float64   `json:"mlx_mem_ratio"`
    SwapPageRate    float64   `json:"swap_page_rate"`
    GpuUtil         float64   `json:"gpu_util"`
    LoadedModels    int       `json:"loaded_models"`
    CollectionError bool      `json:"collection_error"`
}

// HWSamples returns the hardware snapshot series for a window (same §8 guard
// as the latency API: default 24h, max 31 days), oldest first for charting.
// limit defaults to 2880 (24h at the 30s write cadence), caps at 10000.
func (s *Service) HWSamples(ctx context.Context, fromStr, toStr string, limit int) ([]HWSampleRow, error) {
    from, to, err := windowBounds(fromStr, toStr)
    if err != nil {
        return nil, err
    }
    if limit <= 0 {
        limit = 2880
    }
    if limit > 10000 {
        limit = 10000
    }
    rows, err := s.pool.Query(ctx,
        `SELECT ts, sys_mem_ratio, mlx_mem_ratio, swap_page_rate, gpu_util,
                loaded_models, collection_error
         FROM fg_hw_samples
         WHERE ts >= $1 AND ts < $2
         ORDER BY ts ASC
         LIMIT $3`, from, to, limit)
    if err != nil {
        return nil, fmt.Errorf("hw samples query: %w", err)
    }
    defer rows.Close()
    out := []HWSampleRow{}
    for rows.Next() {
        var r HWSampleRow
        if err := rows.Scan(&r.TS, &r.SysMemRatio, &r.MlxMemRatio, &r.SwapPageRate,
            &r.GpuUtil, &r.LoadedModels, &r.CollectionError); err != nil {
            return nil, fmt.Errorf("hw samples scan: %w", err)
        }
        out = append(out, r)
    }
    return out, rows.Err()
}
