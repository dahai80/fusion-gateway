package router

import (
    "context"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// #535: the half-open probe reservation must be taken at dispatch time — on
// a decision that is actually about to send traffic to the local backend —
// not at P0, before the routing decision exists. Under the old order, the
// first request to arrive after the open-state timeout reserved the probe at
// P0 and was then diverted by the chain below (token budget, swap
// heuristics, session affinity...), burning the probe window on a request
// that never reached the backend and never recorded an outcome. Under
// sustained divert pressure the breaker pin-cycled open/half_open forever.

func newProbeTestEngine(t *testing.T, timeout time.Duration) (*Engine, *config.ConfigSnapshot) {
    t.Helper()
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.CircuitBreaker.Timeout = timeout
    cfg.Config.Routing.TokenThreshold = 100
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    return e, cfg
}

func probeCtx(cfg *config.ConfigSnapshot, input, total int) context.Context {
    ctx := tokenizer.WithTokenBudget(context.Background(), tokenizer.TokenBudget{
        InputTokens: input, TotalBudget: total,
    })
    return config.WithSnapshot(ctx, cfg)
}

// A cloud-diverted request (token budget exceeded) in half-open state must
// NOT consume the probe: a subsequent local-decided request still admits,
// and recording its outcomes closes the breaker again.
func TestDecide_HalfOpenProbeNotBurnedByCloudDivert(t *testing.T) {
    e, cfg := newProbeTestEngine(t, 50*time.Millisecond)

    e.Trip("local", "test trip")
    time.Sleep(80 * time.Millisecond) // open timeout elapses -> next read flips half_open

    // Cloud-diverted request: over token budget, cloud-routable model. Under
    // the old order this reserved the half-open probe at P0 and was diverted
    // without ever reaching local.
    dec := e.Decide(probeCtx(cfg, 200, 300), &RouteRequest{Model: "test-model"})
    if dec.Backend != CloudBackend {
        t.Fatalf("expected cloud divert for over-budget request, got %s: %s", dec.Backend, dec.Reason)
    }

    // Local-decided request: within budget. Must admit despite the diverted
    // request that just passed through half-open state.
    dec = e.Decide(probeCtx(cfg, 10, 90), &RouteRequest{Model: "test-model"})
    if dec.Backend != LocalBackend {
        t.Fatalf("half-open probe burned by cloud-diverted request: local decision got %s: %s", dec.Backend, dec.Reason)
    }

    // Simulate the dispatched probe succeeding: SuccessThreshold (3) recorded
    // successes must close the breaker.
    e.RecordSuccess("local")
    dec = e.Decide(probeCtx(cfg, 10, 90), &RouteRequest{Model: "test-model"})
    if dec.Backend != LocalBackend {
        t.Fatalf("second probe admission after success got %s: %s", dec.Backend, dec.Reason)
    }
    e.RecordSuccess("local")
    dec = e.Decide(probeCtx(cfg, 10, 90), &RouteRequest{Model: "test-model"})
    if dec.Backend != LocalBackend {
        t.Fatalf("third probe admission after success got %s: %s", dec.Backend, dec.Reason)
    }
    e.RecordSuccess("local")
    if state := e.breakers["local"].State(); state != StateClosed {
        t.Fatalf("breaker state after %d probe successes = %s, want closed", 3, state)
    }
}

// Single-probe enforcement still holds: while a reserved probe is in
// flight, a second local-decided request is diverted with a reason that
// names the probe, not the open state.
func TestDecide_HalfOpenSingleProbeEnforced(t *testing.T) {
    e, cfg := newProbeTestEngine(t, 50*time.Millisecond)

    e.Trip("local", "test trip")
    time.Sleep(80 * time.Millisecond)

    dec := e.Decide(probeCtx(cfg, 10, 90), &RouteRequest{Model: "test-model"})
    if dec.Backend != LocalBackend {
        t.Fatalf("first half-open request must carry the probe, got %s: %s", dec.Backend, dec.Reason)
    }

    dec = e.Decide(probeCtx(cfg, 10, 90), &RouteRequest{Model: "test-model"})
    if dec.Backend != CloudBackend {
        t.Fatalf("second concurrent local decision must divert while probe in flight, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "half_open_probe_busy" {
        t.Fatalf("divert reason = %q, want half_open_probe_busy", dec.Reason)
    }
}

// routing.mode=local overrides the breaker by design (fast-path returns
// before P0); the dispatch-time gate must not re-divert those decisions —
// all traffic reaching local is also what self-heals it.
func TestDecide_ModeLocalSkipsProbeGate(t *testing.T) {
    e, cfg := newProbeTestEngine(t, 50*time.Millisecond)
    cfg.Config.Routing.Mode = "local"

    e.Trip("local", "test trip")

    dec := e.Decide(probeCtx(cfg, 10, 90), &RouteRequest{Model: "test-model"})
    if dec.Backend != LocalBackend {
        t.Fatalf("mode=local must override tripped breaker, got %s: %s", dec.Backend, dec.Reason)
    }
}
