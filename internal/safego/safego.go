package safego

import (
    "log/slog"
    "runtime/debug"
    "sync/atomic"
    "time"
)

// totalRestarts counts goroutine restarts across all GoRestart workers. Read
// by tests/observability to confirm a worker actually restarted after a panic.
var totalRestarts atomic.Int64

func recoverLog(name string) {
    if r := recover(); r != nil {
        slog.Error("goroutine panic recovered",
            "goroutine", name,
            "panic", r,
            "stack", string(debug.Stack()),
        )
    }
}

// Go runs fn in a new goroutine, recovering and logging a single panic then
// exiting. Use for per-request/short-lived goroutines where a panic loses one
// unit of work and a restart would be wrong (stream pumps, fan-out, drain,
// cancel-watch). For long-lived workers that must run for the process lifetime
// (ticker loops, accept loops, queue processors), use GoRestart so a single
// panic does not permanently silence the worker (audit H3).
func Go(name string, fn func()) {
    go func() {
        defer recoverLog(name)
        fn()
    }()
}

// restartCfg governs the GoRestart backoff + circuit breaker.
type restartCfg struct {
    baseBackoff    time.Duration // first restart delay after a panic
    maxBackoff     time.Duration // backoff cap
    gracePeriod    time.Duration // fn running this long without panicking resets the consecutive-panic counter
    maxConsecutive int           // consecutive panics within gracePeriod before the circuit breaker trips
}

var defaultRestartCfg = restartCfg{
    baseBackoff:    100 * time.Millisecond,
    maxBackoff:     30 * time.Second,
    gracePeriod:    30 * time.Second,
    maxConsecutive: 10,
}

// GoRestart runs fn in a goroutine that restarts fn after a panic (with
// exponential backoff), so a single panic never permanently kills a long-lived
// worker. A normal return (fn exits cleanly, e.g. context canceled) does NOT
// restart — only panics do. A consecutive-panic circuit breaker stops the loop
// when fn panics too often too quickly (default: 10 panics each within 30s of
// start), logging loudly instead of burning CPU in a panic-restart cycle
// (audit H3). Use ONLY for long-lived workers; per-request goroutines use Go.
func GoRestart(name string, fn func()) {
    go restartLoop(name, fn, defaultRestartCfg)
}

// goRestartWithCfg is the test seam for GoRestart: lets a test inject a tight
// circuit breaker + short backoff so the breaker trips in milliseconds instead
// of ~50s. Not exported; only safego tests use it.
func goRestartWithCfg(name string, fn func(), cfg restartCfg) {
    go restartLoop(name, fn, cfg)
}

func restartLoop(name string, fn func(), cfg restartCfg) {
    consecutive := 0
    backoff := cfg.baseBackoff
    for {
        started := time.Now()
        panicked := true
        func() {
            defer func() {
                if r := recover(); r != nil {
                    ran := time.Since(started)
                    // If fn ran past the grace period before panicking, treat
                    // it as a healthy worker hitting a fresh incident: reset
                    // the consecutive counter and backoff. Only rapid repeated
                    // panics (each within gracePeriod) count toward the breaker.
                    if ran >= cfg.gracePeriod {
                        consecutive = 0
                        backoff = cfg.baseBackoff
                    }
                    consecutive++
                    slog.Error("goroutine panic recovered, restarting",
                        "goroutine", name,
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
            fn()
        }()
        if !panicked {
            // fn returned normally (no panic). A long-lived worker that exits
            // cleanly (context canceled, graceful shutdown) stays dead — we do
            // not restart on normal return, only on panic.
            return
        }
        if consecutive > cfg.maxConsecutive {
            slog.Error("goroutine circuit breaker tripped, worker permanently disabled",
                "goroutine", name,
                "consecutive_panics", consecutive,
                "max_consecutive", cfg.maxConsecutive,
                "grace_period", cfg.gracePeriod.String(),
            )
            return
        }
        totalRestarts.Add(1)
        time.Sleep(backoff)
        // Exponential backoff, capped.
        next := backoff * 2
        if next > cfg.maxBackoff {
            next = cfg.maxBackoff
        }
        backoff = next
    }
}

// TotalRestarts returns the cumulative count of GoRestart restarts across all
// workers. Intended for tests/observability.
func TotalRestarts() int64 {
    return totalRestarts.Load()
}
