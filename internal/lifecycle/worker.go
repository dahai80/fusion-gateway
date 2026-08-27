package lifecycle

import (
    "context"
    "log/slog"
    "runtime/debug"
    "sync"
    "time"
)

// Worker wraps a single long-lived background goroutine with explicit
// start-run-stop lifecycle. EI10: the prior pattern launched goroutines via
// safeGo and cancelled a context at shutdown but never WAITED for the goroutine
// to actually exit — Server.Shutdown returned while goroutines were still
// writing metrics/store, so a stop could land mid-write. Worker fixes this:
// Stop() cancels the goroutine's context then blocks on a WaitGroup until the
// goroutine returns. H3: the goroutine restarts on panic (in-loop recovery
// with exponential backoff + a consecutive-panic circuit breaker) so a single
// panic does not permanently silence the worker. The WaitGroup is Done exactly
// once, only when fn exits cleanly (no panic) OR the circuit breaker trips and
// the worker is permanently disabled — both are terminal states, so Stop never
// hangs.
//
// One Worker per long-lived goroutine. Stop is idempotent (sync.Once) and safe
// to call from the shutdown path. The goroutine MUST honor ctx.Done() — Worker
// cannot force-stop a goroutine that ignores its context.
type Worker struct {
    name   string
    cancel context.CancelFunc
    wg     sync.WaitGroup
    stop   sync.Once
}

// Start launches a long-lived goroutine. The fn receives a child context
// derived from parent; cancelling the child (via Stop) is how the goroutine is
// told to exit. Start returns the Worker so the caller can Stop it later. If
// parent is nil a background context is used. The goroutine restarts on panic
// (H3) with backoff; a clean return of fn is terminal (no restart).
func Start(parent context.Context, name string, fn func(ctx context.Context)) *Worker {
    if parent == nil {
        parent = context.Background()
    }
    childCtx, cancel := context.WithCancel(parent)
    w := &Worker{name: name, cancel: cancel}
    w.wg.Add(1)
    go w.run(childCtx, fn)
    slog.Info("lifecycle worker started", "worker", name)
    return w
}

// run executes fn, recovering panics and restarting with backoff. The loop is
// terminal when fn returns without panicking (clean exit, e.g. ctx canceled)
// or when the consecutive-panic circuit breaker trips. In either terminal case
// the WaitGroup is released exactly once.
func (w *Worker) run(ctx context.Context, fn func(ctx context.Context)) {
    defer w.wg.Done()
    const (
        baseBackoff    = 100 * time.Millisecond
        maxBackoff     = 30 * time.Second
        gracePeriod    = 30 * time.Second
        maxConsecutive = 10
    )
    consecutive := 0
    backoff := baseBackoff
    for {
        started := time.Now()
        panicked := true
        func() {
            defer func() {
                if r := recover(); r != nil {
                    ran := time.Since(started)
                    if ran >= gracePeriod {
                        consecutive = 0
                        backoff = baseBackoff
                    }
                    consecutive++
                    slog.Error("lifecycle worker panic recovered, restarting",
                        "worker", w.name,
                        "panic", r,
                        "stack", string(debug.Stack()),
                        "consecutive_panics", consecutive,
                        "ran_before_panic", ran.String(),
                        "next_backoff", backoff.String(),
                    )
                    return
                }
                panicked = false
            }()
            fn(ctx)
        }()
        if !panicked {
            // Clean return: terminal. A long-lived fn that honors ctx.Done()
            // exits here when Stop cancels ctx.
            return
        }
        if consecutive > maxConsecutive {
            slog.Error("lifecycle worker circuit breaker tripped, permanently disabled",
                "worker", w.name,
                "consecutive_panics", consecutive,
                "max_consecutive", maxConsecutive,
                "grace_period", gracePeriod.String(),
            )
            return
        }
        select {
        case <-time.After(backoff):
        case <-ctx.Done():
            // Shutdown requested during backoff — stop restarting.
            return
        }
        next := backoff * 2
        if next > maxBackoff {
            next = maxBackoff
        }
        backoff = next
    }
}

// Stop signals the goroutine to exit (cancel its context) and blocks until it
// has actually returned. Idempotent: a second Stop is a no-op. A goroutine that
// ignores ctx.Done() will block Stop indefinitely — that is a bug in the
// goroutine, not in Worker, and surfaces as a stuck shutdown (visible) rather
// than a silent mid-write stop.
func (w *Worker) Stop() {
    w.stop.Do(func() {
        if w.cancel != nil {
            w.cancel()
        }
        w.wg.Wait()
        slog.Info("lifecycle worker stopped", "worker", w.name)
    })
}
