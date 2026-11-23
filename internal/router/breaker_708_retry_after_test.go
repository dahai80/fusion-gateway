package router

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #708: RetryAfterSeconds must tell a rejected caller how long to back off:
// open = seconds until the open->half_open transition; half-open with a
// probe in flight = seconds until the probe window expires (capped at 5 so
// a long-running streaming probe cannot pin callers to a stale value).
func TestBreakerRetryAfter_Open(t *testing.T) {
    cbCfg := config.CircuitBreakerConfig{Timeout: 60 * time.Second}
    cb := NewCircuitBreaker(cbCfg)
    cb.Trip("memory_overload")

    got := cb.RetryAfterSeconds()
    if got < 1 || got > 60 {
        t.Errorf("RetryAfterSeconds open = %d, want within [1, 60]", got)
    }
}

func TestBreakerRetryAfter_HalfOpenProbeInFlight(t *testing.T) {
    cbCfg := config.CircuitBreakerConfig{Timeout: 100 * time.Millisecond}
    cb := NewCircuitBreaker(cbCfg)
    cb.Trip("memory_overload")

    // Force the open->half_open transition.
    deadline := time.Now().Add(2 * time.Second)
    for cb.State() != StateHalfOpen && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    if cb.State() != StateHalfOpen {
        t.Fatalf("breaker never reached half_open")
    }

    // Reserve the probe, then a denied caller must see a small, bounded
    // backoff — never the full breaker timeout.
    if !cb.Admit() {
        t.Fatalf("first Admit in half_open must reserve the probe")
    }
    got := cb.RetryAfterSeconds()
    if got < 1 || got > 5 {
        t.Errorf("RetryAfterSeconds half-open probe in flight = %d, want within [1, 5]", got)
    }
}

func TestBreakerRetryAfter_Closed(t *testing.T) {
    cbCfg := config.CircuitBreakerConfig{Timeout: 30 * time.Second}
    cb := NewCircuitBreaker(cbCfg)
    if got := cb.RetryAfterSeconds(); got != 1 {
        t.Errorf("RetryAfterSeconds closed = %d, want 1", got)
    }
}
