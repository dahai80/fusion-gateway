package router

import (
    "context"
    "errors"
    "log/slog"
    "time"
)

// ErrQueueTimeout is returned by Acquire when a slot does not free up within
// the configured queue_timeout. Callers translate this into a 429 (#102
// ADR-001: opt-in local wait-queue, mode=local only).
var ErrQueueTimeout = errors.New("queue timeout: local slot did not free up in time")

// slotQueue is a bounded FIFO wait-queue over local inference slots. It is a
// counting semaphore of size maxConcurrent: each Acquire blocks until a slot
// is free (or queue_timeout fires → ErrQueueTimeout), each returned release
// closes one slot. Engaged ONLY when routing.mode=local AND
// routing.local_priority.queue_enabled — in hybrid mode the engine falls
// back to cloud instead of queueing, so this stays nil. NOT a priority/fair
// queue: the gateway does not own agent semantic labels (#102 ADR-001).
type slotQueue struct {
    sem chan struct{}
}

// newSlotQueue builds a slotQueue with capacity maxConcurrent. A
// non-positive maxConcurrent means unlimited (sem buffered to a large
// number so Acquire never blocks) — but callers only construct this when
// maxConcurrent > 0, so the cap is always effective.
func newSlotQueue(maxConcurrent int) *slotQueue {
    cap := maxConcurrent
    if cap <= 0 {
        cap = 1
    }
    return &slotQueue{sem: make(chan struct{}, cap)}
}

// Acquire blocks until a local slot is free or timeout elapses. On success it
// returns a release closure the caller MUST defer. On timeout it returns
// ErrQueueTimeout (caller writes 429). ctx cancel is honored too: a canceled
// ctx returns ctx.Err() without acquiring a slot.
func (q *slotQueue) Acquire(ctx context.Context, timeout time.Duration) (func(), error) {
    if timeout <= 0 {
        // No wait budget: try once, non-blocking; fail fast if full.
        select {
        case q.sem <- struct{}{}:
            slog.Debug("local slot queue acquired (no-wait)", "occupied", len(q.sem))
            return q.release, nil
        default:
            return nil, ErrQueueTimeout
        }
    }

    timer := time.NewTimer(timeout)
    defer timer.Stop()

    select {
    case q.sem <- struct{}{}:
        slog.Debug("local slot queue acquired", "wait_budget", timeout, "occupied", len(q.sem))
        return q.release, nil
    case <-timer.C:
        slog.Warn("local slot queue timeout, rejecting request (429)", "wait_budget", timeout, "occupied", len(q.sem))
        return nil, ErrQueueTimeout
    case <-ctx.Done():
        slog.Info("local slot queue acquire canceled by client", "occupied", len(q.sem), "error", ctx.Err())
        return nil, ctx.Err()
    }
}

// release frees one slot. Idempotent-safe only if called once per Acquire —
// callers MUST use `defer release()` exactly once (the standard pattern), never
// call it manually AND in a defer (that would re-introduce the #97
// double-release shape, though here it's a channel send not a counter, so the
// worst case is one extra free slot, not an underflow).
func (q *slotQueue) release() {
    select {
    case <-q.sem:
    default:
        slog.Warn("local slot queue release on empty semaphore (double-release?)")
    }
}

// Occupied returns the current number of held slots (for metrics/logging).
func (q *slotQueue) Occupied() int {
    return len(q.sem)
}
