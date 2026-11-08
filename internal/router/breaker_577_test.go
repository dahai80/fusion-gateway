package router

import (
    "bytes"
    "log/slog"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #577 ask 1: breaker state transitions must log WHICH backend tripped —
// during the r36 outage the gateway log had no way to attribute the OPEN to
// the fusion-mlx backend (the breaker had no identity).

type syncBuf577 struct {
    mu sync.Mutex
    b  bytes.Buffer
}

func (s *syncBuf577) Write(p []byte) (int, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.b.Write(p)
}

func (s *syncBuf577) String() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.b.String()
}

func capture577Logs(t *testing.T) *syncBuf577 {
    t.Helper()
    buf := &syncBuf577{}
    old := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
    t.Cleanup(func() { slog.SetDefault(old) })
    return buf
}

func TestBreakerTransitions_LogBackendName(t *testing.T) {
    logs := capture577Logs(t)
    cb := newNamedBreaker("local", config.CircuitBreakerConfig{
        FailureThreshold: 2,
        SuccessThreshold: 1,
        Timeout:          50 * time.Millisecond,
    })

    cb.RecordFailure()
    cb.RecordFailure() // threshold reached → OPEN
    if got := logs.String(); !strings.Contains(got, "backend=local") {
        t.Fatalf("open transition must log the backend name (#577), logs:\n%s", got)
    }

    time.Sleep(60 * time.Millisecond)
    if cb.State() != StateHalfOpen {
        t.Fatalf("expected half_open after timeout, got %s", cb.State())
    }
    if got := logs.String(); !strings.Contains(got, "half_open") || !strings.Contains(got, "backend=local") {
        t.Fatalf("half_open transition must log the backend name, logs:\n%s", got)
    }

    cb.RecordSuccess()
    if got := logs.String(); !strings.Contains(got, "transitioned to closed") || !strings.Contains(got, "backend=local") {
        t.Fatalf("closed transition must log the backend name, logs:\n%s", got)
    }
}
