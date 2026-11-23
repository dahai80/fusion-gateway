package router

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func testBreaker708() *CircuitBreaker {
    return NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold:    10,
        Timeout:             30 * time.Second,
        HalfOpenMaxRequests: 1,
        SuccessThreshold:    1,
    })
}

// #708: RetryAfterSeconds must give callers a usable backoff window.
// open = time until the half-open transition; half-open with a probe in
// flight = time until the probe window expires (capped at 5); otherwise 1.
func TestCircuitBreaker_RetryAfterSeconds_Open(t *testing.T) {
    cb := testBreaker708()
    cb.Trip("memory_overload")

    got := cb.RetryAfterSeconds()
    if got < 1 || got > 30 {
        t.Errorf("RetryAfterSeconds = %d, want in [1,30] for a 30s open breaker", got)
    }

    cb.mu.Lock()
    cb.lastFailureTime = time.Now().Add(-29 * time.Second)
    cb.mu.Unlock()
    if got := cb.RetryAfterSeconds(); got != 1 {
        t.Errorf("RetryAfterSeconds = %d, want 1 when open window nearly elapsed", got)
    }
}

func TestCircuitBreaker_RetryAfterSeconds_HalfOpenProbe(t *testing.T) {
    cb := testBreaker708()
    cb.Trip("memory_overload")

    cb.mu.Lock()
    cb.state = StateHalfOpen
    cb.probeInFlight = true
    cb.probeDeadline = time.Now().Add(3 * time.Second)
    cb.mu.Unlock()

    if got := cb.RetryAfterSeconds(); got < 1 || got > 5 {
        t.Errorf("RetryAfterSeconds = %d, want in [1,5] for half-open probe window", got)
    }

    cb.mu.Lock()
    cb.probeInFlight = false
    cb.mu.Unlock()

    if got := cb.RetryAfterSeconds(); got != 1 {
        t.Errorf("RetryAfterSeconds = %d, want 1 when half-open without probe", got)
    }
}

func TestCircuitBreaker_RetryAfterSeconds_Closed(t *testing.T) {
    cb := testBreaker708()
    if got := cb.RetryAfterSeconds(); got != 1 {
        t.Errorf("RetryAfterSeconds = %d, want 1 for closed breaker", got)
    }
}
