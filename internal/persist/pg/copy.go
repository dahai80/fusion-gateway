package pg

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "time"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/google/uuid"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/persist"
)

// copyWriter is the production batchWriter: pgx pool + CopyFrom batches.
type copyWriter struct {
    pool *pgxpool.Pool
    cfg  config.PersistenceConfig
}

// schemaSQL provisions the tables (plan §4.1). Idempotent — safe on every
// startup. payloads/chunks carry NO foreign key: retention is per-table
// partition DROP and an FK would block dropping a partition whose referenced
// rows still exist (plan §4.3) — the association is the shared id, and
// payloads retention ≤ logs retention prevents dangling rows.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS fg_request_logs (
    -- PK includes ts: PostgreSQL requires a partitioned table's unique
    -- constraints to cover every partitioning column. id stays first for
    -- index locality; (id) alone is still effectively unique per request.
    id             uuid NOT NULL,
    ts             timestamptz NOT NULL,
    PRIMARY KEY (id, ts),
    request_id     text NOT NULL,
    api_key_name   text,
    project_id     text,
    space_id       text,
    chat_id        text,
    endpoint       text NOT NULL,
    request_type   text NOT NULL,
    is_stream      boolean NOT NULL,
    model          text NOT NULL,
    model_mapped   text,
    backend        text NOT NULL,
    backend_type   text NOT NULL,
    route_reason   text,
    status_code    int NOT NULL,
    is_success     boolean NOT NULL,
    error_message  text,
    input_tokens   int,
    output_tokens  int,
    total_tokens   int,
    cost           numeric(12,6),
    cost_usd       numeric(12,6),
    local_savings  numeric(12,6),
    duration_ms    real,
    ttft_ms        real,
    tpot_ms        real,
    tpot_p95_ms    real,
    chunk_count    int,
    tokens_per_sec real,
    client_ip      text,
    user_agent     text
) PARTITION BY RANGE (ts);

CREATE TABLE IF NOT EXISTS fg_request_payloads (
    id             uuid NOT NULL,
    ts             timestamptz NOT NULL,
    PRIMARY KEY (id, ts),
    req_body       jsonb,
    resp_body      jsonb,
    -- raw_* hold non-JSON captures (SSE frames, binary): the jsonb columns
    -- reject those, and one bad row would fail the whole CopyFrom batch.
    req_raw        text,
    resp_raw       text,
    resp_truncated boolean NOT NULL DEFAULT false,
    req_truncated  boolean NOT NULL DEFAULT false,
    resp_raw_mime  text
) PARTITION BY RANGE (ts);

-- P3b tables (plan §15.4). All low-volume, plain tables — daily partitioning
-- buys nothing here; retention is DELETE-based in the hourly sweep.
CREATE TABLE IF NOT EXISTS fg_hw_samples (
    -- hardware snapshot, 30s cadence. loaded_models is a count: the
    -- collector exposes MLXModelsLoaded as int, not a name list.
    ts               timestamptz PRIMARY KEY,
    sys_mem_ratio    real,
    mlx_mem_ratio    real,
    swap_page_rate   real,
    gpu_util         real,
    loaded_models    int,
    collection_error boolean
);

CREATE TABLE IF NOT EXISTS fg_gateway_events (
    id      bigserial PRIMARY KEY,
    ts      timestamptz NOT NULL DEFAULT now(),
    level   text NOT NULL,
    event   text NOT NULL,
    detail  jsonb
);
CREATE INDEX IF NOT EXISTS idx_fg_gateway_events_ts ON fg_gateway_events (ts DESC);

CREATE TABLE IF NOT EXISTS fg_anomalies (
    id          bigserial PRIMARY KEY,
    ts          timestamptz NOT NULL,
    detected_at timestamptz NOT NULL,
    kind        text NOT NULL,
    backend     text,
    model       text,
    severity    text NOT NULL,
    evidence    jsonb NOT NULL,
    status      text NOT NULL DEFAULT 'open',
    acked_by    text,
    muted_until timestamptz
);
CREATE INDEX IF NOT EXISTS idx_fg_anomalies_status_ts ON fg_anomalies (status, ts DESC);

CREATE TABLE IF NOT EXISTS fg_optimization_findings (
    id             bigserial PRIMARY KEY,
    generated_at   timestamptz NOT NULL,
    rule           text NOT NULL,
    scope          jsonb NOT NULL,
    evidence       jsonb NOT NULL,
    action         jsonb,
    status         text NOT NULL DEFAULT 'new',
    applied_at     timestamptz,
    applied_config text
);
CREATE INDEX IF NOT EXISTS idx_fg_findings_status ON fg_optimization_findings (status, generated_at DESC);

CREATE TABLE IF NOT EXISTS fg_reports (
    id          bigserial PRIMARY KEY,
    period      text NOT NULL,
    range_start timestamptz NOT NULL,
    range_end   timestamptz NOT NULL,
    content_md  text NOT NULL,
    created_at  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fg_reports_created ON fg_reports (created_at DESC);
`

// newCopyWriter connects the pool and provisions the schema.
func newCopyWriter(cfg config.PersistenceConfig, dsn string) (*copyWriter, error) {
    poolCfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, fmt.Errorf("pg persist: parse DSN: %w", err)
    }
    poolCfg.MaxConns = int32(cfg.Postgres.MaxOpenConns)
    poolCfg.MinConns = int32(cfg.Postgres.MaxIdleConns)
    poolCfg.MaxConnLifetime = time.Hour
    timeout := time.Duration(cfg.Postgres.ConnTimeoutSecs) * time.Second
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    defer cancel()
    pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
    if err != nil {
        return nil, fmt.Errorf("pg persist: connect: %w", err)
    }
    if _, err := pool.Exec(ctx, schemaSQL); err != nil {
        pool.Close()
        return nil, fmt.Errorf("pg persist: schema provision: %w", err)
    }
    slog.Info("pg persist connected, schema provisioned",
        "max_conns", cfg.Postgres.MaxOpenConns)
    return &copyWriter{pool: pool, cfg: cfg}, nil
}

func (w *copyWriter) Ping(ctx context.Context) error {
    return w.pool.Ping(ctx)
}

func (w *copyWriter) Close() {
    w.pool.Close()
}

// WriteBatch COPYs both tables in one transaction: logs rows always, payload
// rows only when a body was captured. CopyFrom is the plan §4.2 choice —
// row-by-row INSERT would be ~10× slower at batch scale.
func (w *copyWriter) WriteBatch(ctx context.Context, entries []*persist.Entry) error {
    tx, err := w.pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("begin: %w", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()

    now := time.Now().UTC()
    logRows := make([][]any, 0, len(entries))
    payloadRows := make([][]any, 0, len(entries))
    for _, e := range entries {
        // The in-memory store assigns ids like "log-42" (not a UUID); the
        // PG column is uuid. Prefer whichever of ID/RequestID parses as a
        // UUID (InitRequestLog seeds RequestID with a real uuid), else
        // generate one — a row must never fail the whole batch.
        id := resolveRowID(e)
        ts := e.Log.Timestamp
        if ts.IsZero() {
            ts = now
        }
        backend := e.Log.ChannelName
        backendType := e.BackendType
        if backendType == "" {
            backendType = e.Log.ChannelType
        }
        logRows = append(logRows, []any{
            id, ts, e.Log.RequestID, e.Log.APIKeyName,
            e.Log.ProjectID, e.Log.SpaceID, e.Log.ChatID,
            e.Endpoint, e.Log.RequestType, e.Log.IsStream,
            e.Log.Model, e.Log.ModelMapped,
            backend, backendType, e.Log.RouteReason,
            e.Log.StatusCode, e.Log.IsSuccess, e.Log.ErrorMessage,
            e.Log.InputTokens, e.Log.OutputTokens, e.Log.TotalTokens,
            e.Log.Cost, e.Log.CostUSD, e.Log.LocalSavings,
            // RequestLog.Latency stores seconds despite its latency_ms JSON
            // tag — convert at this boundary, the column is duration_ms.
            e.Log.Latency * 1000, e.Log.TTFT, e.Log.TPotMS, e.Log.TPotP95,
            e.Log.ChunkCount, e.Log.TokensPerSec,
            e.ClientIP, e.UserAgent,
        })
        if len(e.ReqBody) > 0 || len(e.RespBody) > 0 {
            reqJSON, reqRaw := splitBody(e.ReqBody)
            respJSON, respRaw := splitBody(e.RespBody)
            payloadRows = append(payloadRows, []any{
                id, ts,
                reqJSON, respJSON, reqRaw, respRaw,
                e.RespTruncated, e.ReqTruncated, e.RespRawMime,
            })
        }
    }

    if _, err := tx.CopyFrom(ctx,
        pgx.Identifier{"fg_request_logs"},
        []string{
            "id", "ts", "request_id", "api_key_name",
            "project_id", "space_id", "chat_id",
            "endpoint", "request_type", "is_stream",
            "model", "model_mapped", "backend", "backend_type", "route_reason",
            "status_code", "is_success", "error_message",
            "input_tokens", "output_tokens", "total_tokens",
            "cost", "cost_usd", "local_savings",
            "duration_ms", "ttft_ms", "tpot_ms", "tpot_p95_ms",
            "chunk_count", "tokens_per_sec",
            "client_ip", "user_agent",
        },
        pgx.CopyFromRows(logRows)); err != nil {
        return fmt.Errorf("copy fg_request_logs: %w", err)
    }
    if len(payloadRows) > 0 {
        if _, err := tx.CopyFrom(ctx,
            pgx.Identifier{"fg_request_payloads"},
            []string{
                "id", "ts", "req_body", "resp_body", "req_raw", "resp_raw",
                "resp_truncated", "req_truncated", "resp_raw_mime",
            },
            pgx.CopyFromRows(payloadRows)); err != nil {
            return fmt.Errorf("copy fg_request_payloads: %w", err)
        }
    }
    if err := tx.Commit(ctx); err != nil {
        return fmt.Errorf("commit: %w", err)
    }
    return nil
}

// splitBody routes a captured body to its column pair: valid JSON goes to the
// jsonb column (queryable), anything else (SSE frames, binary, truncated
// mid-token JSON) goes to the raw text column. Streaming responses are the
// common raw case — resp_raw_mime records text/event-stream.
func splitBody(b []byte) (jsonVal, rawVal any) {
    if len(b) == 0 {
        return nil, nil
    }
    if json.Valid(b) {
        return b, nil
    }
    return nil, string(b)
}

// resolveRowID picks the row uuid: Log.ID when it is a UUID (store-agnostic),
// else RequestID (uuid-seeded by InitRequestLog), else a fresh uuid.
func resolveRowID(e *persist.Entry) [16]byte {
    for _, cand := range []string{e.Log.ID, e.Log.RequestID} {
        if u, err := uuid.Parse(cand); err == nil {
            return u
        }
    }
    return uuid.New()
}
