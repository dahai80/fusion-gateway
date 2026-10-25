package pg

import (
    "context"
    "fmt"
    "log/slog"
    "time"
)

// retentionSweepInterval: daily partitions need at most hourly checks.
const retentionSweepInterval = 1 * time.Hour

// partitionedTables maps each daily-partitioned table to its retention window
// (days). Retention is per-table DROP (plan §4.3) — payloads retention must
// stay ≤ logs retention, enforced in config validate.
type partitionedTable struct {
    name          string
    retentionDays int
}

// sweepOnce runs one partition-management pass: ensure today + the next 3 days
// exist for every table, then DROP partitions fully past each table's
// retention window. Idempotent; safe to run concurrently (IF NOT EXISTS /
// IF EXISTS guards).
func (s *Sink) sweepOnce(ctx context.Context) {
    tables := []partitionedTable{
        {name: "fg_request_logs", retentionDays: s.cfg.Retention.RequestLogsDays},
        {name: "fg_request_payloads", retentionDays: s.cfg.Retention.RequestPayloadsDays},
    }
    now := time.Now().UTC()
    for _, t := range tables {
        // Pre-create today and the next 3 days (cold start self-provisions;
        // a gateway down over midnight never misses a partition).
        for i := 0; i < 4; i++ {
            day := now.AddDate(0, 0, i).Truncate(24 * time.Hour)
            if err := s.ensurePartition(ctx, t.name, day); err != nil {
                slog.Warn("pg persist partition create failed",
                    "table", t.name, "day", day.Format("2006-01-02"), "error", err)
                return
            }
        }
        // Drop partitions whose window ended before the retention horizon.
        horizon := now.AddDate(0, 0, -t.retentionDays)
        if err := s.dropExpiredPartitions(ctx, t.name, horizon); err != nil {
            slog.Warn("pg persist retention drop failed", "table", t.name, "error", err)
        }
    }
}

// ensurePartition creates the daily partition for day if absent.
func (s *Sink) ensurePartition(ctx context.Context, table string, day time.Time) error {
    cw, ok := s.writer.(*copyWriter)
    if !ok {
        // injected fake writer (tests): partition management is a
        // production-writer concern
        return nil
    }
    partName := fmt.Sprintf("%s_%s", table, day.Format("2006_01_02"))
    from := day.Format("2006-01-02")
    to := day.AddDate(0, 0, 1).Format("2006-01-02")
    q := fmt.Sprintf(
        `CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
        partName, table, from, to)
    _, err := cw.pool.Exec(ctx, q)
    return err
}

// dropExpiredPartitions lists the table's partitions and DROPs each whose
// window upper bound is older than horizon. DROP is O(1) with no bloat — the
// plan §4.3 retention mechanism.
func (s *Sink) dropExpiredPartitions(ctx context.Context, table string, horizon time.Time) error {
    cw, ok := s.writer.(*copyWriter)
    if !ok {
        return nil
    }
    pool := cw.pool
    prefix := table + "_20"
    rows, err := pool.Query(ctx,
        `SELECT c.relname FROM pg_class c JOIN pg_inherits i ON i.inhrelid = c.oid
         WHERE i.inhparent = $1::regclass AND c.relname LIKE $2`,
        table, prefix+"%")
    if err != nil {
        return err
    }
    defer rows.Close()
    var dropped []string
    for rows.Next() {
        var relname string
        if err := rows.Scan(&relname); err != nil {
            return err
        }
        // Partition name suffix _YYYY_MM_DD is the window start date.
        dayStr := relname[len(prefix):] + "T00:00:00Z"
        start, err := time.Parse("2006_01_02T15:04:05Z", dayStr)
        if err != nil {
            continue
        }
        if start.Before(horizon.AddDate(0, 0, -1)) {
            if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, relname)); err != nil {
                return err
            }
            dropped = append(dropped, relname)
        }
    }
    if len(dropped) > 0 {
        slog.Info("pg persist retention dropped partitions", "table", table, "dropped", dropped)
    }
    return rows.Err()
}
