package ops

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "regexp"
    "strconv"
    "strings"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/jitter"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
)

// Sink is the emission contract the server depends on; tests fake it to
// capture events without a database.
type Sink interface {
    Emit(ev Event) bool
}

// Emitter writes request-level ops events to fusion_ops.ops_events
// (issue #689, flywheel Loop A, architecture/fusion-flywheel-1119.md §2).
// Contract mirrored from the fusion-core OpsEmitter (issue #56):
//   - Emit() NEVER blocks: on a full queue the event is dropped and counted
//     (spec: backpressure 时丢弃计数不阻塞业务 — the gateway hot path must
//     not regress);
//   - a failed flush drops the whole batch, counts it, and retries with
//     jittered exponential backoff — the worker never crashes the gateway;
//   - rows go in via pgx CopyFrom (binary COPY, same fast path as the
//     fusion-core asyncpg copy_records_to_table).
type Emitter struct {
    events    chan Event
    batchSize int
    flushInterval time.Duration
    dsn       string
    started   atomic.Bool
    worker    *lifecycle.Worker
    // Counters surface emission health (drop rate feeds the <1% acceptance).
    Emitted    atomic.Int64
    Dropped    atomic.Int64
    Flushed    atomic.Int64
    Reconnects atomic.Int64
}

// Event is one ops event row. Fields map 1:1 to the ops_events columns;
// Payload is marshaled to JSONB at flush time.
type Event struct {
    TS         time.Time
    Source     string
    TenantHash string
    EventType  string
    LatencyMS  int
    Status     string
    Payload    map[string]any
}

var columns = []string{"ts", "source", "tenant_hash", "event_type", "latency_ms", "status", "payload"}

// New builds an emitter. Call Start to launch the flush worker.
func New(maxQueue, batchSize, flushIntervalMS int, dsn string) *Emitter {
    if batchSize < 1 {
        batchSize = 1
    }
    if flushIntervalMS <= 0 {
        flushIntervalMS = 1000
    }
    return &Emitter{
        events:        make(chan Event, maxQueue),
        batchSize:     batchSize,
        flushInterval: time.Duration(flushIntervalMS) * time.Millisecond,
        dsn:           dsn,
    }
}

// Start launches the flush worker (EI10: lifecycle.Worker — joined on
// shutdown, never a bare goroutine). #692: the emitter no longer
// self-provisions — schema v1 lives in `public` and is owned by the
// fusion-ops migrations (dahai80/fusion-ops#1; the app role deliberately
// has no CREATE). A missing/mis-shaped table surfaces as flush errors
// (dropped + counted + logged) instead of a second divergent schema.
func (e *Emitter) Start(ctx context.Context) {
    if !e.started.CompareAndSwap(false, true) {
        slog.Warn("ops emitter: already started")
        return
    }
    e.worker = lifecycle.Start(ctx, "ops-emitter", e.run)
}

// Stop cancels the worker and joins it: run() drains the queue, does a final
// flush, and returns (pool ops are timeout-bounded, so Stop cannot hang
// indefinitely).
func (e *Emitter) Stop(_ time.Duration) {
    if !e.started.Load() || e.worker == nil {
        return
    }
    e.worker.Stop()
}

// Emit enqueues one event. Returns true if accepted, false if dropped.
// Never blocks (select/default on a buffered channel), never errors.
func (e *Emitter) Emit(ev Event) bool {
    if ev.TS.IsZero() {
        ev.TS = time.Now().UTC()
    }
    if ev.Source == "" {
        ev.Source = "gateway"
    }
    select {
    case e.events <- ev:
        e.Emitted.Add(1)
        return true
    default:
        d := e.Dropped.Add(1)
        // Throttle drop logs: first drop + every 100th, mirrors fusion-core.
        if d == 1 || d%100 == 0 {
            slog.Warn("ops emitter queue full, dropped", "total_dropped", d)
        }
        return false
    }
}

// ensureSchema was removed (#692): schema provisioning belongs to the
// fusion-ops migrations (public.ops_events, schema v1). The emitter role
// has no CREATE by design; a missing table fails visibly at flush time.

func (e *Emitter) newPool(ctx context.Context) (*pgxpool.Pool, error) {    cfg, err := pgxpool.ParseConfig(e.dsn)
    if err != nil {
        return nil, fmt.Errorf("ops emitter dsn parse failed: %w", err)
    }
    cfg.MaxConns = 2
    cfg.MaxConnLifetime = 30 * time.Minute
    return pgxpool.NewWithConfig(ctx, cfg)
}

func (e *Emitter) run(ctx context.Context) {
    var pool *pgxpool.Pool
    backoff := time.Second
    batch := make([]Event, 0, e.batchSize)
    ticker := time.NewTicker(e.flushInterval)
    defer ticker.Stop()

    flush := func() {
        if len(batch) == 0 {
            return
        }
        if pool == nil {
            p, err := e.newPool(ctx)
            if err != nil {
                slog.Error("ops emitter: pool connect failed, dropping batch", "size", len(batch), "error", err)
                e.Dropped.Add(int64(len(batch)))
                e.Reconnects.Add(1)
                batch = batch[:0]
                return
            }
            pool = p
        }
        rows := make([][]any, len(batch))
        for i, ev := range batch {
            payload := "{}"
            if b, err := json.Marshal(ev.Payload); err == nil {
                payload = string(b)
            }
            // Schema v1 (#692): status INTEGER (NULL when unset),
            // tenant_hash NOT NULL (empty string, not NULL), payload NOT NULL.
            var status any
            if ev.Status != "" {
                if n, err := strconv.Atoi(ev.Status); err == nil {
                    status = n
                } else {
                    slog.Warn("ops emitter: non-numeric status, storing NULL", "status", ev.Status)
                }
            }
            rows[i] = []any{ev.TS, ev.Source, ev.TenantHash, ev.EventType, ev.LatencyMS, status, payload}
        }
        fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
        src := pgx.CopyFromRows(rows)
        n, err := pool.CopyFrom(fctx, pgx.Identifier{"ops_events"}, columns, src)
        cancel()
        if err != nil {
            slog.Error("ops emitter: flush failed, dropping batch", "size", len(batch), "error", err)
            e.Dropped.Add(int64(len(batch)))
            e.Reconnects.Add(1)
            batch = batch[:0]
            pool.Close()
            pool = nil
            time.Sleep(jitter.Duration(backoff))
            backoff = min(backoff*2, 30*time.Second)
            return
        }
        e.Flushed.Add(n)
        backoff = time.Second
        batch = batch[:0]
    }

    for {
        select {
        case ev := <-e.events:
            batch = append(batch, ev)
            for len(batch) < e.batchSize {
                select {
                case ev := <-e.events:
                    batch = append(batch, ev)
                default:
                    goto drained
                }
            }
        drained:
            if len(batch) >= e.batchSize {
                flush()
            }
        case <-ticker.C:
            flush()
        case <-ctx.Done():
            // Shutdown: drain remaining events, final flush, exit. Worker.Stop
            // joins this goroutine, so main's later pool teardown is safe.
            for {
                select {
                case ev := <-e.events:
                    batch = append(batch, ev)
                default:
                    flush()
                    if pool != nil {
                        pool.Close()
                    }
                    return
                }
            }
        }
    }
}

// nilIfEmpty was removed (#692): schema v1 declares tenant_hash NOT NULL
// DEFAULT '' — empty strings are stored verbatim, never NULL.

// TenantHash derives the desensitized tenant marker: sha256(tenant)[:8]
// (flywheel desensitize standard v1: 用户名/租户名 → hash 前 8 位).
func TenantHash(tenant string) string {
    if tenant == "" {
        return ""
    }
    sum := sha256.Sum256([]byte(tenant))
    return hex.EncodeToString(sum[:4])
}

// idSegment matches path segments that are ids: pure digits (3+), UUIDs,
// long hex/base64-ish tokens, and captcha/challenge id shapes. Collapsing
// them yields the route template the flywheel clusters on (raw paths with
// ids are NEVER stored — issue #689 desensitization requirement).
var (
    digitsSeg   = regexp.MustCompile(`^\d{3,}$`)
    uuidSeg     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
    tokenishSeg = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
    hexSeg      = regexp.MustCompile(`^[0-9a-fA-F]{8,}$`)
)

// RouteTemplate collapses id-like path segments to {id}. Query strings are
// dropped by the caller (never passed in). Known version segments (v1, v2)
// and short action words pass through untouched.
func RouteTemplate(path string) string {
    segs := strings.Split(strings.Trim(path, "/"), "/")
    out := make([]string, 0, len(segs))
    for _, seg := range segs {
        if seg == "" {
            continue
        }
        if digitsSeg.MatchString(seg) || uuidSeg.MatchString(seg) ||
            tokenishSeg.MatchString(seg) || hexSeg.MatchString(seg) {
            out = append(out, "{id}")
            continue
        }
        out = append(out, seg)
    }
    if len(out) == 0 {
        return "/"
    }
    return "/" + strings.Join(out, "/")
}

// ErrorSignature builds the stable cluster key feeding mined_issues
// (flywheel §5: cluster_key = source + error signature = status + route).
func ErrorSignature(status int, route string) string {
    return strconv.Itoa(status) + " " + route
}
