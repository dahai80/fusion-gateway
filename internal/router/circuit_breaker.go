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
    // B6 (audit): half-open single-probe gate. Without it, every request saw
    // StateHalfOpen after the open timeout and flooded the recovering backend,
    // with successCount shared across concurrent probes. One probe in flight
    // at a time; a reservation that never sees Record* (its request diverted
    // before dispatch) self-heals after probeWindow.
    probeInFlight bool
    probeDeadline time.Time
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

// probeWindow bounds how long a half-open probe reservation stays valid when
// its request never reaches RecordSuccess/RecordFailure (decide diverted it to
// another backend). Reuses the open-state timeout — already config-driven and
// on the same order as a request's reasonable lifetime.
func (cb *CircuitBreaker) probeWindow() time.Duration {
    if cb.cfg.Timeout <= 0 {
        return 30 * time.Second
    }
    return cb.cfg.Timeout
}

// Admit reports whether ONE more request may hit the protected backend, and
// for half-open enforces the single-probe rule: the first caller reserves the
// probe slot, every later caller is denied until the probe's RecordSuccess/
// RecordFailure clears it or the reservation window expires. Closed admits
// unconditionally. Open denies (transitioning to half-open on timeout, same
// rule as State()). Admission sites: engine P0 local, cloud, cluster gates.
func (cb *CircuitBreaker) Admit() bool {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    switch cb.state {
    case StateClosed:
        return true
    case StateOpen:
        if time.Since(cb.lastFailureTime) > cb.cfg.Timeout {
            cb.state = StateHalfOpen
            cb.successCount = 0
            slog.Info("circuit breaker transitioned to half_open",
                "timeout", cb.cfg.Timeout,
                "reason", cb.tripReason,
            )
        }
        return false
    default: // StateHalfOpen
        now := time.Now()
        if cb.probeInFlight && now.Before(cb.probeDeadline) {
            return false
        }
        cb.probeInFlight = true
        cb.probeDeadline = now.Add(cb.probeWindow())
        return true
    }
}

func (cb *CircuitBreaker) RecordSuccess() {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    switch cb.state {
    case StateHalfOpen:
        cb.probeInFlight = false
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
        // failure_threshold 0 disables tripping entirely (the documented
        // opt-out the admin routing PUT writes; B13 follow-up). Guard here so
        // a disabled breaker does not trip on the very first failure
        // (failureCount++ >= 0 would always hold).
        if cb.cfg.FailureThreshold <= 0 {
            return
        }
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
        cb.probeInFlight = false
        cb.state = StateOpen
        cb.lastFailureTime = time.Now()
        slog.Warn("circuit breaker reopened from half_open")
    case StateOpen:
        // B6 (audit): failures observed while open (racing requests that were
        // already admitted) must refresh lastFailureTime — the old code left
        // the pre-trip timestamp, distorting the remaining-open-time math.
        cb.lastFailureTime = time.Now()
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
    cb.probeInFlight = false
    cb.probeDeadline = time.Time{}
    slog.Info("circuit breaker reset to half_open for warmup")
}

// BreakerSnapshot captures the trip-relevant state of a CircuitBreaker so a
// freshly rebuilt breaker (on hot-reload DrainAndApply) can inherit it. EI3:
// without inheritance, DrainAndApply swaps e.breakers for a brand-new map of
// closed breakers — an already-open (failing) backend looks healthy to the new
// breaker, so requests keep hitting it until the new breaker re-accumulates
// enough failures to trip. Inheriting state → fail the same way from the start.
type BreakerSnapshot struct {
    State           CircuitBreakerState
    FailureCount    int
    LastFailureTime time.Time
    TripReason      string
}

// Snapshot returns the breaker's trip-relevant state under the read lock.
func (cb *CircuitBreaker) Snapshot() BreakerSnapshot {
    cb.mu.RLock()
    defer cb.mu.RUnlock()
    return BreakerSnapshot{
        State:           cb.state,
        FailureCount:    cb.failureCount,
        LastFailureTime: cb.lastFailureTime,
        TripReason:      cb.tripReason,
    }
}

// InheritSnapshot copies a prior breaker's trip state onto this breaker. The
// cfg/failureCount are already set at construction; this only restores the
// runtime trip fields (state, lastFailureTime, tripReason, and the in-flight
// failureCount toward the threshold). A closed prior breaker leaves the new one
// closed (no-op). An open/half_open prior breaker is restored open/half_open
// with its tripReason, so the new breaker opens immediately and the operator
// sees WHY (not a blank "config applied"). successCount is NOT inherited — a
// fresh half_open starts its recovery probe from 0 successes.
func (cb *CircuitBreaker) InheritSnapshot(s BreakerSnapshot) {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    if s.State == StateClosed {
        return
    }
    cb.state = s.State
    cb.failureCount = s.FailureCount
    cb.lastFailureTime = s.LastFailureTime
    cb.tripReason = s.TripReason
    slog.Info("circuit breaker inherited prior trip state on reload",
        "state", s.State.String(),
        "reason", s.TripReason,
        "failure_count", s.FailureCount,
    )
}
