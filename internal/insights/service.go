// Package insights implements the P3 read layer over the persistence tables:
// latency analytics, provider comparison, and log search (plan §8). Read-only
// and strictly time-windowed — every query goes through a dedicated pool with
// a 10s statement_timeout so a heavy aggregate can never wedge the admin API.
package insights

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Service is the admin-API query service over fg_request_logs/payloads.
type Service struct {
    pool *pgxpool.Pool
}

// New connects a read pool with a statement-level timeout (plan §8 guard:
// aggregate queries time out at 10s server-side, handler maps to 504).
func New(cfg config.PersistenceConfig, dsn string) (*Service, error) {
    poolCfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, fmt.Errorf("insights: parse DSN: %w", err)
    }
    poolCfg.MaxConns = 4
    poolCfg.MaxConnLifetime = time.Hour
    poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
    pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
    if err != nil {
        return nil, fmt.Errorf("insights: connect: %w", err)
    }
    slog.Info("insights service connected",
        "max_conns", poolCfg.MaxConns, "statement_timeout", "10s")
    return &Service{pool: pool}, nil
}

// Close releases the read pool.
func (s *Service) Close() {
    if s.pool != nil {
        s.pool.Close()
    }
}

// Ping verifies connectivity (admin persistence/test endpoint).
func (s *Service) Ping(ctx context.Context) error {
    return s.pool.Ping(ctx)
}

// windowBounds enforces the plan §8 guard: default 24h lookback, max 31 days.
// Malformed timestamps are rejected by the caller (400); degenerate windows
// (to <= from) fall back to the default.
func windowBounds(fromStr, toStr string) (from, to time.Time, err error) {
    now := time.Now().UTC()
    from = now.Add(-24 * time.Hour)
    to = now
    if fromStr != "" {
        t, perr := time.Parse(time.RFC3339, fromStr)
        if perr != nil {
            return from, to, fmt.Errorf("invalid from: %w", perr)
        }
        from = t.Truncate(time.Second)
    }
    if toStr != "" {
        t, perr := time.Parse(time.RFC3339, toStr)
        if perr != nil {
            return from, to, fmt.Errorf("invalid to: %w", perr)
        }
        to = t.Truncate(time.Second)
    }
    if !to.After(from) {
        return from, to, fmt.Errorf("to must be after from")
    }
    if to.Sub(from) > 31*24*time.Hour {
        return from, to, fmt.Errorf("window exceeds 31 days")
    }
    return from, to, nil
}

// granularity maps the API param to a date_trunc unit; default hour.
func granularity(g string) (string, error) {
    switch g {
    case "", "hour":
        return "hour", nil
    case "day":
        return "day", nil
    case "minute":
        return "minute", nil
    default:
        return "", fmt.Errorf("invalid granularity %q (minute|hour|day)", g)
    }
}
