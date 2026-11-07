package router

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #568: a swap_thrashing trip left the local breaker stuck half_open for
// 100+ minutes — probes that divert after admission (RR4 slot-full, client
// disconnect, multimodal refusal) never reach RecordSuccess/RecordFailure,
// so the single-probe slot stays consumed until the probe window expires
// and successCount can never reach the threshold. These tests pin the
// three recovery paths: explicit slot release, success re-arming the slot,
// and the wedge guard that force-closes a half_open past 10x the probe
// window.

func newWedgeBreaker(timeout time.Duration) *CircuitBreaker {
    return NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold: 5,
        SuccessThreshold: 3,
        Timeout:          timeout,
    })
}

// tripToHalfOpen forces a trip and waits past the open timeout so the next
// State/Admit call transitions to half_open.
func tripToHalfOpen(cb *CircuitBreaker, timeout time.Duration) {
    cb.Trip("swap_thrashing")
    time.Sleep(timeout + 30*time.Millisecond)
    if cb.State() != StateHalfOpen {
        panic("breaker did not reach half_open")
    }
}

// ReleaseProbe must re-arm the single-probe slot so the next request admits
// immediately (dispatch-diverted probes neither succeed nor fail — they must
// not hold the slot for the probe window).
func TestReleaseProbe_RearmsSlot(t *testing.T) {
    timeout := 50 * time.Millisecond
    cb := newWedgeBreaker(timeout)
    tripToHalfOpen(cb, timeout)

    if !cb.Admit() {
        t.Fatal("first probe must admit")
    }
    cb.ReleaseProbe()
    if !cb.Admit() {
        t.Fatal("probe slot must re-arm after ReleaseProbe, next probe denied")
    }
}

// ReleaseProbe is a no-op when no probe is held or state is closed — must
// not panic or corrupt state.
func TestReleaseProbe_NoOpWithoutProbe(t *testing.T) {
    cb := newWedgeBreaker(50 * time.Millisecond)
    cb.ReleaseProbe() // closed, no probe
    if cb.State() != StateClosed {
        t.Fatal("ReleaseProbe on closed breaker must not change state")
    }
    cb.Trip("test")
    time.Sleep(80 * time.Millisecond)
    _ = cb.State() // half_open
    cb.ReleaseProbe()
    cb.ReleaseProbe() // double release harmless
}

// A recorded half-open success must re-arm the probe slot immediately
// (issue item 1: successCount 1/3 must let probe 2 in without waiting for
// window expiry).
func TestRecordSuccess_RearmsProbeSlot(t *testing.T) {
    timeout := 50 * time.Millisecond
    cb := newWedgeBreaker(timeout)
    tripToHalfOpen(cb, timeout)

    if !cb.Admit() {
        t.Fatal("first probe must admit")
    }
    cb.RecordSuccess()
    if !cb.Admit() {
        t.Fatal("next probe must admit right after a recorded success")
    }
    cb.RecordSuccess()
    if !cb.Admit() {
        t.Fatal("third probe must admit after second success")
    }
    cb.RecordSuccess()
    if cb.State() != StateClosed {
        t.Fatalf("threshold 3 successes must close, got %s", cb.State())
    }
}

// Wedge guard: half_open persisting past 10x the probe window with no close
// path reachable force-closes the breaker so a stale trip_reason cannot
// wedge local-exclusive traffic permanently (issue item 3).
func TestHalfOpenWedge_ForcesClosed(t *testing.T) {
    timeout := 30 * time.Millisecond
    cb := newWedgeBreaker(timeout)
    tripToHalfOpen(cb, timeout)
    cb.Admit() // consume the probe slot; simulate a divert that never records

    // wedgeAge = 10 * probeWindow(timeout) = 300ms; outlive it.
    deadline := time.Now().Add(2 * time.Second)
    for cb.State() == StateHalfOpen && time.Now().Before(deadline) {
        time.Sleep(20 * time.Millisecond)
        cb.Admit() // every probe "diverts": admitted but never records
    }
    if cb.State() != StateClosed {
        t.Fatalf("wedged half_open must force-close, still %s", cb.State())
    }
    if !cb.Admit() {
        t.Fatal("closed breaker must admit")
    }
}

// A healthy close within the wedge window must NOT be preempted by the
// guard: normal recovery still works.
func TestHalfOpenWedge_DoesNotFireEarly(t *testing.T) {
    timeout := 30 * time.Millisecond
    cb := newWedgeBreaker(timeout)
    tripToHalfOpen(cb, timeout)

    cb.Admit()
    cb.RecordSuccess()
    time.Sleep(100 * time.Millisecond) // > wedgeAge? no: 300ms; well within
    if cb.State() != StateHalfOpen {
        t.Fatalf("normal half_open recovery must not be disturbed, got %s", cb.State())
    }
    if !cb.Admit() {
        t.Fatal("re-armed slot must admit within wedge window")
    }
}

// Engine wrapper: ReleaseProbe resolves the per-backend breaker under the
// map lock and clears its probe slot.
func TestEngine_ReleaseProbe(t *testing.T) {
    e, cfg := newProbeTestEngine(t, 50*time.Millisecond)
    e.Trip("local", "test trip")
    time.Sleep(80 * time.Millisecond)
    _ = e.breakers["local"].State() // flip half_open
    if !e.breakers["local"].Admit() {
        t.Fatal("probe must admit")
    }
    e.ReleaseProbe("local")
    if !e.breakers["local"].Admit() {
        t.Fatal("slot must re-arm after engine ReleaseProbe")
    }
    // Unknown backend: no panic.
    e.ReleaseProbe("nonexistent")
    _ = cfg
}
