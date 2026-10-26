// #301 cloud-overflow runtime state. The overflow POLICY (enabled, cooldown,
// cost ceiling) lives in routing.fallback.overflow and hot-reloads via the
// ConfigSnapshot; this file holds the STATE the policy needs across requests:
// the cooldown window and the daily cost counter. Bounded by construction —
// two scalars and a date string, reset on day rollover.
package server

import (
    "context"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/persist/pg"
)

type overflowState struct {
    mu            sync.Mutex
    overflowUntil time.Time
    day           string
    dayCost       float64
}

// cooldownActive reports whether the last overflow armed a still-running
// cooldown window. The caller then skips the known-bad local path and routes
// straight to cloud, so requests stop paying the local timeout latency before
// escaping (the 600s x3 eval failure arm in issue #301).
func (o *overflowState) cooldownActive(now time.Time) bool {
    o.mu.Lock()
    defer o.mu.Unlock()
    return now.Before(o.overflowUntil)
}

func (o *overflowState) armCooldown(now time.Time, cooldown time.Duration) {
    if cooldown <= 0 {
        return
    }
    o.mu.Lock()
    defer o.mu.Unlock()
    o.overflowUntil = now.Add(cooldown)
    slog.Info("cloud overflow cooldown armed", "cooldown", cooldown.String())
}

// ceilingBlocked reports whether today's cumulative overflow cost already
// reached routing.fallback.overflow.cost_ceiling. costCeiling <= 0 means
// unlimited and never blocks. Day key is UTC so the counter rolls at a
// single well-defined midnight regardless of node timezone.
func (o *overflowState) ceilingBlocked(now time.Time, costCeiling float64) bool {
    if costCeiling <= 0 {
        return false
    }
    day := now.UTC().Format("2006-01-02")
    o.mu.Lock()
    defer o.mu.Unlock()
    if o.day != day {
        return false
    }
    return o.dayCost >= costCeiling
}

// chargeOverflow adds one overflow's cost to the daily counter after the
// cloud response succeeded. Checked BEFORE the cloud call via ceilingBlocked
// and charged AFTER, so the last request that crosses the ceiling still
// completes — the ceiling stops the NEXT overflow, it never kills an
// in-flight response the client already waited for.
func (o *overflowState) chargeOverflow(now time.Time, costUSD float64) {
    day := now.UTC().Format("2006-01-02")
    o.mu.Lock()
    defer o.mu.Unlock()
    if o.day != day {
        o.day = day
        o.dayCost = 0
    }
    o.dayCost += costUSD
}

// overflowConfig resolves the live overflow policy. Prefers the request
// context snapshot (hot-reload aware, same mechanism as router.Decide),
// falls back to the server's current snapshot for paths without middleware
// ctx. Deliberately reads the ctx value directly instead of
// SnapshotFromContext: that helper falls back to the package-level global
// snapshot, which in tests (and any non-middleware caller) diverges from
// this server's own cfg.
func (s *Server) overflowConfig(ctx context.Context) config.OverflowConfig {
    if ctx != nil {
        if snap, ok := ctx.Value(config.ConfigSnapshotKey).(*config.ConfigSnapshot); ok {
            return snap.Config.Routing.Fallback.Overflow
        }
    }
    return s.snapshot().Config.Routing.Fallback.Overflow
}

// recordCloudOverflowEvent lands every overflow in fg_gateway_events (via the
// insights EventRecorder satisfied by *pg.Sink) with the four fields issue
// #301 requires: trigger reason, source backend, target backend, cost delta.
// This is the user-trust surface — "why did this request cost money" must
// have a queryable answer. No-op (log only) when persistence is off.
func (s *Server) recordCloudOverflowEvent(reason, sourceBackend, targetBackend, model string, costDelta float64) {
    detail := map[string]any{
        "reason":         reason,
        "source_backend": sourceBackend,
        "target_backend": targetBackend,
        "model":          model,
        "cost_delta":     costDelta,
    }
    if s.persistSink != nil {
        if rec, ok := s.persistSink.(pg.EventRecorder); ok {
            rec.RecordEvent("warn", "cloud_overflow", detail)
        }
    }
    slog.Warn("cloud overflow fired", "reason", reason,
        "source_backend", sourceBackend, "target_backend", targetBackend,
        "model", model, "cost_delta", costDelta)
}
