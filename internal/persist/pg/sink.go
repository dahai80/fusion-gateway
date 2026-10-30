// Package pg implements the persist.Sink contract on PostgreSQL (plan §4.2).
// One bounded queue, one batch-writer worker (lifecycle.Worker — joinable,
// panic-safe), one partition-manager worker. The hot path does exactly one
// thing: a non-blocking channel send.
package pg

import (
    "context"
    "log/slog"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/persist"
)

// batchWriter is the unit-testable seam: the sink's queue/flush/drop logic is
// independent of pgx. The production implementation is CopyFrom-based
// (copy.go); tests inject a fake.
type batchWriter interface {
    WriteBatch(ctx context.Context, entries []*persist.Entry) error
    // Ping verifies connectivity (admin "test connection" endpoint).
    Ping(ctx context.Context) error
    Close()
}

// Sink is the PostgreSQL persist.Sink.
type Sink struct {
    cfg    config.PersistenceConfig
    writer batchWriter

    queue   chan *persist.Entry
    dropped atomic.Int64
    enqueued atomic.Int64
    written atomic.Int64

    flusher  *lifecycle.Worker
    sweeper  *lifecycle.Worker
    sampler  *lifecycle.Worker
    // cacheSampler (#421) persists gateway cache counters every 30s.
    cacheSampler *lifecycle.Worker
    stopCh   chan struct{}
    stopOnce atomic.Bool
}

// New builds a Sink connected to the DSN read from the configured env var
// (bindSecretEnv pattern — the DSN itself never lives in config.yaml).
// Callers must call Start before Enqueue does anything useful, and Close at
// shutdown (drain).
func New(cfg config.PersistenceConfig, dsn string) (*Sink, error) {
    w, err := newCopyWriter(cfg, dsn)
    if err != nil {
        return nil, err
    }
    return newSinkWithWriter(cfg, w), nil
}

// newSinkWithWriter wires the sink around an injected writer (production:
// copyWriter; tests: fake).
func newSinkWithWriter(cfg config.PersistenceConfig, w batchWriter) *Sink {
    return &Sink{
        cfg:    cfg,
        writer: w,
        queue:  make(chan *persist.Entry, cfg.Queue.Capacity),
        stopCh: make(chan struct{}),
    }
}

// Start launches the flush worker and the partition/retention sweeper. Both
// are lifecycle.Workers — Close joins them.
func (s *Sink) Start(parent context.Context) {
    s.flusher = lifecycle.Start(parent, "pg-persist-flusher", s.flushLoop)
    s.sweeper = lifecycle.Start(parent, "pg-persist-sweeper", s.sweepLoop)
}

// Enqueue is non-blocking (hot path contract). When the queue is full the
// OLDEST entry is discarded (keep-new: the newest request is the one an
// operator is most likely investigating) and ErrDropped returned for the
// counter — persistence must never backpressure a generation request.
func (s *Sink) Enqueue(entry *persist.Entry) error {
    if s.stopOnce.Load() {
        return persist.ErrDropped
    }
    for {
        select {
        case s.queue <- entry:
            s.enqueued.Add(1)
            observability.RecordPersistEnqueued()
            return nil
        default:
            // Full: evict the oldest to make room, count the drop.
            select {
            case <-s.queue:
                s.dropped.Add(1)
                observability.RecordPersistDropped()
            default:
                // Raced empty (another consumer drained) — retry the send.
            }
            if s.stopOnce.Load() {
                return persist.ErrDropped
            }
        }
    }
}

// Stats reports queue counters for /metrics + admin API.
func (s *Sink) Stats() (enqueued, written, dropped int64) {
    return s.enqueued.Load(), s.written.Load(), s.dropped.Load()
}

// flushLoop is the batch-writer worker body: collect up to BatchSize entries
// or FlushInterval, whichever first, then hand the batch to the writer.
// Write failures keep the batch in memory and retry with exponential backoff
// (plan §4.2: retry-period entries stay queued; the bounded queue drops
// oldest under sustained PG outage — degraded, never blocking).
func (s *Sink) flushLoop(ctx context.Context) {
    interval := time.Duration(s.cfg.Queue.FlushIntervalMS) * time.Millisecond
    ticker := time.NewTicker(interval)
    defer ticker.Stop()

    batch := make([]*persist.Entry, 0, s.cfg.Queue.BatchSize)
    backoff := time.Duration(0)
    degraded := false

    for {
        select {
        case <-ctx.Done():
            s.finalDrain(batch)
            return
        case <-s.stopCh:
            s.finalDrain(batch)
            return
        case <-ticker.C:
        case e := <-s.queue:
            batch = append(batch, e)
            if len(batch) < s.cfg.Queue.BatchSize {
                // opportunistically grab whatever else is immediately ready
                // without blocking, then wait for the next tick/event
                continue
            }
        }

        if len(batch) == 0 {
            continue
        }
        // Backoff after a failed write: pause before retrying the batch so a
        // down PG does not turn the loop into a hot spin.
        if backoff > 0 {
            select {
            case <-time.After(backoff):
            case <-ctx.Done():
                s.finalDrain(batch)
                return
            }
        }
        writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
        err := s.writer.WriteBatch(writeCtx, batch)
        cancel()
        if err != nil {
            if backoff == 0 {
                // First failure of an outage: one structured event, not one
                // per retry (plan §4.2 — 连续失败记 fg_gateway_events).
                s.RecordEvent("error", "persist_degraded",
                    map[string]any{"batch_len": len(batch), "error": err.Error()})
                degraded = true
                backoff = 500 * time.Millisecond
            } else {
                backoff *= 2
                if backoff > 30*time.Second {
                    backoff = 30 * time.Second
                }
            }
            slog.Warn("pg persist batch write failed, will retry",
                "error", err, "batch_len", len(batch), "backoff", backoff.String())
            // Keep draining the queue into the retrying batch, capped at
            // 2× BatchSize to bound memory during an outage.
            for len(batch) < s.cfg.Queue.BatchSize*2 {
                select {
                case e := <-s.queue:
                    batch = append(batch, e)
                default:
                    goto retryDone
                }
            }
        retryDone:
            continue
        }
        backoff = 0
        if degraded {
            s.RecordEvent("info", "persist_recovered", map[string]any{"batch_len": len(batch)})
        }
        s.written.Add(int64(len(batch)))
        for i := 0; i < len(batch); i++ {
            observability.RecordPersistWritten()
        }
        batch = batch[:0]
    }
}

// finalDrain is the shutdown path: merge the flusher's accumulated batch
// with whatever is still instantly available in the queue and write it once,
// best-effort, under a fresh background context (the worker ctx is already
// cancelled at this point). The pending batch MUST be written here — the
// flusher consumed those entries from the queue, so skipping them would lose
// them silently (the drain bug the Close test caught).
func (s *Sink) finalDrain(batch []*persist.Entry) {
    for {
        select {
        case e := <-s.queue:
            batch = append(batch, e)
        default:
            goto write
        }
    }
write:
    if len(batch) == 0 {
        return
    }
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := s.writer.WriteBatch(ctx, batch); err != nil {
        slog.Error("pg persist shutdown drain failed", "error", err, "batch_len", len(batch))
    } else {
        s.written.Add(int64(len(batch)))
        for i := 0; i < len(batch); i++ {
            observability.RecordPersistWritten()
        }
    }
}

// sweepLoop runs the partition manager: pre-create future daily partitions
// and DROP past-retention ones (plan §4.3). Hourly cadence, immediate first
// run so a cold start self-provisions.
func (s *Sink) sweepLoop(ctx context.Context) {
    s.sweepOnce(ctx)
    ticker := time.NewTicker(retentionSweepInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            s.sweepOnce(ctx)
        }
    }
}

// Close drains the queue and stops both workers. Blocks until the flusher has
// exited or ctx expires.
func (s *Sink) Close(ctx context.Context) error {
    if !s.stopOnce.CompareAndSwap(false, true) {
        return nil
    }
    close(s.stopCh)
    if s.flusher != nil {
        s.flusher.Stop()
    }
    if s.sweeper != nil {
        s.sweeper.Stop()
    }
    if s.sampler != nil {
        s.sampler.Stop()
    }
    if s.cacheSampler != nil {
        s.cacheSampler.Stop()
    }
    s.writer.Close()
    return nil
}
