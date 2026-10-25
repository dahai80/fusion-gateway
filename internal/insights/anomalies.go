// P3b stage 2 (plan §15.5): fg_anomalies read + lifecycle mutations. All
// admin-only; ack/mute audit through the gateway event recorder.
package insights

import (
    "context"
    "encoding/json"
    "fmt"
    "time"
)

// Anomaly is one fg_anomalies row.
type Anomaly struct {
    ID         int64           `json:"id"`
    TS         time.Time       `json:"ts"`
    DetectedAt time.Time       `json:"detected_at"`
    Kind       string          `json:"kind"`
    Backend    *string         `json:"backend"`
    Model      *string         `json:"model"`
    Severity   string          `json:"severity"`
    Evidence   json.RawMessage `json:"evidence"`
    Status     string          `json:"status"`
    AckedBy    *string         `json:"acked_by"`
    MutedUntil *time.Time      `json:"muted_until"`
}

// Anomalies lists detections, newest first. status/kind/backend are
// exact-match optional filters; limit defaults to 100, caps at 500.
func (s *Service) Anomalies(ctx context.Context, status, kind, backend string, limit int) ([]Anomaly, error) {
    if limit <= 0 {
        limit = 100
    }
    if limit > 500 {
        limit = 500
    }
    where := ""
    args := []any{}
    if status != "" {
        args = append(args, status)
        where += fmt.Sprintf(" status = $%d AND", len(args))
    }
    if kind != "" {
        args = append(args, kind)
        where += fmt.Sprintf(" kind = $%d AND", len(args))
    }
    if backend != "" {
        args = append(args, backend)
        where += fmt.Sprintf(" backend = $%d AND", len(args))
    }
    if where != "" {
        where = " WHERE" + where
        where = where[:len(where)-4]
    }
    args = append(args, limit)
    q := `SELECT id, ts, detected_at, kind, backend, model, severity,
                 evidence, status, acked_by, muted_until
          FROM fg_anomalies` + where +
        fmt.Sprintf(" ORDER BY ts DESC, id DESC LIMIT $%d", len(args))
    rows, err := s.pool.Query(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("anomalies query: %w", err)
    }
    defer rows.Close()
    out := []Anomaly{}
    for rows.Next() {
        var a Anomaly
        if err := rows.Scan(&a.ID, &a.TS, &a.DetectedAt, &a.Kind, &a.Backend,
            &a.Model, &a.Severity, &a.Evidence, &a.Status, &a.AckedBy,
            &a.MutedUntil); err != nil {
            return nil, fmt.Errorf("anomalies scan: %w", err)
        }
        out = append(out, a)
    }
    return out, rows.Err()
}

// AckAnomaly marks an anomaly handled by `by`. No-op transition when already
// acked; muted/resolved rows are left alone (un-muting via ack would silently
// re-arm a detector the operator suppressed).
func (s *Service) AckAnomaly(ctx context.Context, id int64, by string) (*Anomaly, error) {
    var a Anomaly
    err := s.pool.QueryRow(ctx,
        `UPDATE fg_anomalies
         SET status = 'acked', acked_by = $2
         WHERE id = $1 AND status IN ('open', 'acked')
         RETURNING id, ts, detected_at, kind, backend, model, severity,
                   evidence, status, acked_by, muted_until`,
        id, by).Scan(&a.ID, &a.TS, &a.DetectedAt, &a.Kind, &a.Backend,
        &a.Model, &a.Severity, &a.Evidence, &a.Status, &a.AckedBy, &a.MutedUntil)
    if err != nil {
        return nil, fmt.Errorf("ack anomaly: %w", err)
    }
    return &a, nil
}

// MuteAnomaly suppresses re-detection of the same signature until `until`
// (plan §15.5 屏蔽 N 天). The detector checks muted_until before inserting.
func (s *Service) MuteAnomaly(ctx context.Context, id int64, until time.Time) (*Anomaly, error) {
    var a Anomaly
    err := s.pool.QueryRow(ctx,
        `UPDATE fg_anomalies
         SET status = 'muted', muted_until = $2
         WHERE id = $1 AND status IN ('open', 'acked')
         RETURNING id, ts, detected_at, kind, backend, model, severity,
                   evidence, status, acked_by, muted_until`,
        id, until).Scan(&a.ID, &a.TS, &a.DetectedAt, &a.Kind, &a.Backend,
        &a.Model, &a.Severity, &a.Evidence, &a.Status, &a.AckedBy, &a.MutedUntil)
    if err != nil {
        return nil, fmt.Errorf("mute anomaly: %w", err)
    }
    return &a, nil
}
