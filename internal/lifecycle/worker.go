package lifecycle

import (
    "context"
    "log/slog"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// Worker wraps a single long-lived background goroutine with explicit
// start-run-stop lifecycle. EI10: the prior pattern launched goroutines via
// safeGo and cancelled a context at shutdown but never WAITED for the goroutine
// to actually exit — Server.Shutdown returned while goroutines were still
// writing metrics/store, so a stop could land mid-write. Worker fixes this:
// Stop() cancels the goroutine's context then blocks on a WaitGroup until the
// goroutine returns. safeGo still owns panic recovery; Worker owns lifecycle.
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
// parent is nil a background context is used.
func Start(parent context.Context, name string, fn func(ctx context.Context)) *Worker {
    if parent == nil {
        parent = context.Background()
    }
    childCtx, cancel := context.WithCancel(parent)
    w := &Worker{name: name, cancel: cancel}
    w.wg.Add(1)
    safego.Go(name, func() {
        defer w.wg.Done()
        fn(childCtx)
    })
    slog.Info("lifecycle worker started", "worker", name)
    return w
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

// Done returns a channel closed when the goroutine exits. Useful for ordered
// shutdown or tests that want to observe exit without calling Stop.
func (w *Worker) Done() <-chan struct{} {
    d := make(chan struct{})
    go func() {
        w.wg.Wait()
        close(d)
    }()
    return d
}
