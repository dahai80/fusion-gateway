package jitter

import (
    "log/slog"
    "math/rand/v2"
    "time"
)

// jitterFactor is the default ±fraction applied to a base duration. H5 (audit
// P1): a cluster of gateways all polling on a fixed tick boundary produces a
// synchronized request spike against every shared upstream (fusion-mlx admin,
// master /api/nodes, node /v1/models) at each interval edge. Spreading each
// gateway's tick by ±20% turns a single-point 150 req/s spike into smeared
// load across a ~4s window for a 10s interval — same aggregate rate, no herd.
const jitterFactor = 0.20

// Duration returns base perturbed by ±jitterFactor. A zero or negative base
// returns zero unchanged (callers gate on a positive interval before calling).
// The spread is uniform over [base*(1-f), base*(1+f)] so the mean stays base
// and no tick is shorter than base*(1-f) — a node that needs >interval to
// recover is never polled faster than the configured floor.
func Duration(base time.Duration) time.Duration {
    if base <= 0 {
        return 0
    }
    if jitterFactor <= 0 {
        return base
    }
    span := float64(base) * jitterFactor
    offset := (rand.Float64()*2 - 1) * span
    d := time.Duration(float64(base) + offset)
    if d < 0 {
        d = 0
    }
    return d
}

// After returns a channel that fires after a Duration(base)-jittered interval.
// Mirrors time.After so a caller drops it into an existing select alongside
// ctx.Done()/stopCh — this is the surgical H5 fix for the discovery tick loops
// and the retry backoff: each tick/retry wakes on its own de-synced boundary
// rather than a shared fixed edge. Logs the chosen interval at Debug for
// traceability.
func After(base time.Duration) <-chan time.Time {
    d := Duration(base)
    if d <= 0 {
        ch := make(chan time.Time, 1)
        ch <- time.Now()
        return ch
    }
    slog.Debug("jitter after scheduled", "base", base.String(), "jittered", d.String())
    return time.After(d)
}
