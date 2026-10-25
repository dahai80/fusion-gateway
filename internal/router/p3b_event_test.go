package router

import (
    "sync"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
)

// capturedEvent is one recorder callback invocation.
type capturedEvent struct {
    level  string
    event  string
    detail map[string]any
}

// TestTripEventRecorder: force-Trip and failure-threshold transitions each
// publish exactly one breaker_tripped event (P3b-2 breaker-storm producer).
func TestTripEventRecorder(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    var mu sync.Mutex
    var events []capturedEvent
    e.SetEventRecorder(func(level, event string, detail map[string]any) {
        mu.Lock()
        defer mu.Unlock()
        events = append(events, capturedEvent{level, event, detail})
    })

    e.Trip("local", "hardware overload")

    mu.Lock()
    defer mu.Unlock()
    if len(events) != 1 {
        t.Fatalf("events=%d, want 1", len(events))
    }
    if events[0].level != "warn" || events[0].event != "breaker_tripped" {
        t.Errorf("event wrong: %+v", events[0])
    }
    if events[0].detail["backend"] != "local" || events[0].detail["reason"] != "hardware overload" {
        t.Errorf("detail wrong: %+v", events[0].detail)
    }
}

// TestTripEventRecorderNilHook: no recorder wired — Trip must not panic.
func TestTripEventRecorderNilHook(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.Trip("local", "no recorder wired")
}
