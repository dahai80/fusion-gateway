package router

import (
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type CircuitBreakerState int

const (
    StateClosed   CircuitBreakerState = iota
    StateOpen
    StateHalfOpen
)

func (s CircuitBreakerState) String() string {
    switch s {
    case StateClosed:
        return "closed"
    case StateOpen:
        return "open"
    case StateHalfOpen:
        return "half_open"
    default:
        return "unknown"
    }
}

type CircuitBreaker struct {
    mu               sync.RWMutex
    state            CircuitBreakerState
    cfg              config.CircuitBreakerConfig
    failureCount     int
    successCount     int
    lastFailureTime  time.Time
    tripReason       string
}

func NewCircuitBreaker(cfg config.CircuitBreakerConfig) *CircuitBreaker {
    return &CircuitBreaker{
        state: StateClosed,
        cfg:   cfg,
    }
}

func (cb *CircuitBreaker) State() CircuitBreakerState {
    // P4 fix: use RLock for hot-path state query
    cb.mu.RLock()
    currentState := cb.state
    lastFail := cb.lastFailureTime
    cb.mu.RUnlock()

    // Only upgrade to write lock if transition is needed
    if currentState == StateOpen && time.Since(lastFail) > cb.cfg.Timeout {
        cb.mu.Lock()
        // double-check after acquiring write lock
        if cb.state == StateOpen && time.Since(cb.lastFailureTime) > cb.cfg.Timeout {
            cb.state = StateHalfOpen
            cb.successCount = 0
            slog.Info("circuit breaker transitioned to half_open",
                "timeout", cb.cfg.Timeout,
                "reason", cb.tripReason,
            )
        }
        currentState = cb.state
        cb.mu.Unlock()
    }

    return currentState
}

func (cb *CircuitBreaker) RecordSuccess() {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    switch cb.state {
    case StateHalfOpen:
        cb.successCount++
        if cb.successCount >= cb.cfg.SuccessThreshold {
            cb.state = StateClosed
            cb.failureCount = 0
            cb.successCount = 0
            slog.Info("circuit breaker transitioned to closed", "reason", cb.tripReason)
        }
    case StateClosed:
        cb.failureCount = 0
    }
}

func (cb *CircuitBreaker) RecordFailure() {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    switch cb.state {
    case StateClosed:
        cb.failureCount++
        if cb.failureCount >= cb.cfg.FailureThreshold {
            cb.state = StateOpen
            cb.lastFailureTime = time.Now()
            slog.Warn("circuit breaker opened due to failures",
                "failure_count", cb.failureCount,
                "threshold", cb.cfg.FailureThreshold,
            )
        }
    case StateHalfOpen:
        cb.state = StateOpen
        cb.lastFailureTime = time.Now()
        slog.Warn("circuit breaker reopened from half_open")
    }
}

func (cb *CircuitBreaker) Trip(reason string) {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    cb.state = StateOpen
    cb.lastFailureTime = time.Now()
    cb.tripReason = reason
    slog.Warn("circuit breaker force tripped", "reason", reason)
}

func (cb *CircuitBreaker) IsOpen() bool {
    return cb.State() == StateOpen
}

func (cb *CircuitBreaker) ResetToHalfOpen() {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    cb.state = StateHalfOpen
    cb.successCount = 0
    cb.failureCount = 0
    slog.Info("circuit breaker reset to half_open for warmup")
}
