package pg

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "time"
    "unicode/utf8"

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
    scenario       text,
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
    user_agent     text,
    contains_pii   boolean NOT NULL DEFAULT false
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

-- G10 (plan §4.1): per-chunk stream timings for timing forensics. One row
-- per SSE chunk when capture.stream_chunks is on (default off); retention is
-- a short 7-day window (stream_chunks_days). Same no-FK rule as payloads.
CREATE TABLE IF NOT EXISTS fg_stream_chunks (
    id       uuid NOT NULL,
    ts       timestamptz NOT NULL,
    seq      int NOT NULL,
    delta_ms real NOT NULL DEFAULT 0,
    bytes    int NOT NULL DEFAULT 0,
    PRIMARY KEY (id, ts, seq)
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

-- G6: contains_pii was added after the first production deployment;
-- CREATE TABLE IF NOT EXISTS does not add columns to the existing table,
-- so the ALTER is the migration path (propagates to all partitions).
ALTER TABLE fg_request_logs ADD COLUMN IF NOT EXISTS contains_pii boolean NOT NULL DEFAULT false;

-- #325: business trace id (X-Fusion-Trace) — same ALTER migration path as
-- contains_pii. Nullable: rows written by older binaries have no trace id.
ALTER TABLE fg_request_logs ADD COLUMN IF NOT EXISTS trace_id text;

-- #326: X-Fusion-Scenario business label for per-(app, scenario, model) cost
-- aggregation. Same nullable-migration pattern as contains_pii above.
ALTER TABLE fg_request_logs ADD COLUMN IF NOT EXISTS scenario text;
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
    chunkRows := make([][]any, 0)
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
            e.Log.TraceID,
            e.Log.ProjectID, e.Log.SpaceID, e.Log.ChatID, e.Log.Scenario,
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
            e.ClientIP, e.UserAgent, e.Log.ContainsPII,
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
        // G10: per-chunk timings share the log row's id/ts so the chunks
        // endpoint joins them by id alone.
        for _, c := range e.Log.Chunks {
            cts := c.TS
            if cts.IsZero() {
                cts = ts
            }
            chunkRows = append(chunkRows, []any{id, cts, c.Seq, c.DeltaMS, c.Bytes})
        }
    }

    // Payloads copy first: a payload data error triggers the #407 isolation
    // path below, which rolls the tx back and continues in a fresh one — at
    // that point nothing else has been copied yet, so the log rows still land
    // in the fresh tx and the batch is not retried forever.
    if len(payloadRows) > 0 {
        payloadCols := []string{
            "id", "ts", "req_body", "resp_body", "req_raw", "resp_raw",
            "resp_truncated", "req_truncated", "resp_raw_mime",
        }
        if _, err := tx.CopyFrom(ctx,
            pgx.Identifier{"fg_request_payloads"},
            payloadCols,
            pgx.CopyFromRows(payloadRows)); err != nil {
            // #407: one data-error row fails the whole CopyFrom batch and the
            // flush loop would retry it forever. Isolate: copy the rows one at
            // a time, log and drop only the bad ones — the log rows and the
            // good payloads still commit.
            //
            // The failed CopyFrom leaves this tx in an aborted state: every
            // later command on it fails with 25P02 and the final Commit turns
            // into a rollback. Roll back and isolate in a fresh tx.
            _ = tx.Rollback(ctx)
            slog.Warn("pg persist payload batch copy failed, isolating rows",
                "error", err, "rows", len(payloadRows))
            tx, err = w.pool.Begin(ctx)
            if err != nil {
                return fmt.Errorf("begin isolation: %w", err)
            }
            for i := range payloadRows {
                if _, err := tx.CopyFrom(ctx,
                    pgx.Identifier{"fg_request_payloads"},
                    payloadCols,
                    pgx.CopyFromRows(payloadRows[i : i+1])); err != nil {
                    // A row-level data error aborts this tx as well — roll
                    // back and re-begin before the next row so one bad row
                    // never poisons the rest.
                    _ = tx.Rollback(ctx)
                    slog.Error("pg persist payload row dropped",
                        "error", err, "row", i)
                    tx, err = w.pool.Begin(ctx)
                    if err != nil {
                        return fmt.Errorf("begin isolation: %w", err)
                    }
                }
            }
        }
    }
    if _, err := tx.CopyFrom(ctx,
        pgx.Identifier{"fg_request_logs"},
        []string{
            "id", "ts", "request_id", "api_key_name", "trace_id",
            "project_id", "space_id", "chat_id", "scenario",
            "endpoint", "request_type", "is_stream",
            "model", "model_mapped", "backend", "backend_type", "route_reason",
            "status_code", "is_success", "error_message",
            "input_tokens", "output_tokens", "total_tokens",
            "cost", "cost_usd", "local_savings",
            "duration_ms", "ttft_ms", "tpot_ms", "tpot_p95_ms",
            "chunk_count", "tokens_per_sec",
            "client_ip", "user_agent", "contains_pii",
        },
        pgx.CopyFromRows(logRows)); err != nil {
        return fmt.Errorf("copy fg_request_logs: %w", err)
    }
    if len(chunkRows) > 0 {
        if _, err := tx.CopyFrom(ctx,
            pgx.Identifier{"fg_stream_chunks"},
            []string{"id", "ts", "seq", "delta_ms", "bytes"},
            pgx.CopyFromRows(chunkRows)); err != nil {
            return fmt.Errorf("copy fg_stream_chunks: %w", err)
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
    // PostgreSQL text/jsonb columns reject invalid UTF-8 byte sequences with
    // SQLSTATE 22021, and one such payload failed the whole payloads CopyFrom
    // batch. Replace invalid bytes with U+FFFD so the row survives with
    // repaired content instead of dropping the batch.
    if !utf8.Valid(b) {
        b = bytes.ToValidUTF8(b, []byte{0xEF, 0xBF, 0xBD})
    }
    if json.Valid(b) {
        // #407: json.Valid accepts unpaired UTF-16 surrogate escapes that
        // PostgreSQL jsonb rejects (SQLSTATE 22P05). Repair before COPY.
        if cleaned, ok := sanitizeJSON(b); ok {
            return cleaned, nil
        }
        return nil, string(b)
    }
    return nil, string(b)
}

// sanitizeJSON repairs JSON containing unpaired surrogate \uD800-\uDFFF
// escapes. json.Valid is a syntax check only — it passes "\ud800" with no low
// half, which PostgreSQL jsonb rejects with SQLSTATE 22P05, and one such
// payload failed the whole payloads CopyFrom batch (#407). Go's decoder
// replaces unpaired surrogates with U+FFFD, so a re-marshal through it is
// valid everywhere; UseNumber keeps large numbers byte-exact. Bodies without
// surrogate escapes (the overwhelming majority) return unchanged — the
// re-marshal only runs when a surrogate escape is present.
func sanitizeJSON(b []byte) ([]byte, bool) {
    if !hasSurrogateEscape(b) {
        return b, true
    }
    dec := json.NewDecoder(bytes.NewReader(b))
    dec.UseNumber()
    var v any
    if err := dec.Decode(&v); err != nil {
        return nil, false
    }
    cleaned, err := json.Marshal(v)
    if err != nil {
        return nil, false
    }
    return cleaned, true
}

// hasSurrogateEscape reports whether b contains a \u escape in the surrogate
// range (first hex digit 'd', second in 8-f). Valid surrogate pairs match too
// — that only costs one unnecessary re-marshal, never a wrong result. A
// literal "\\u" inside a JSON string also matches for the same reason.
func hasSurrogateEscape(b []byte) bool {
    for i := 0; i+5 < len(b); i++ {
        if b[i] != '\\' || b[i+1] != 'u' {
            continue
        }
        hi, lo := b[i+2] | 0x20, b[i+3] | 0x20
        if hi == 'd' && lo >= '8' && lo <= 'f' {
            return true
        }
    }
    return false
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
