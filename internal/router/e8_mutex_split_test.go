package router

import (
    "context"
    "reflect"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// Test_E8_EngineHasFourMutexes: E8 split Engine.mu (single sync.RWMutex) into
// four concern-specific locks. A revert that re-collapses to one `mu` field
// (or drops a concern lock) must FAIL this structural guard. We assert the
// Engine struct has the four named locks and NO field named `mu`.
func Test_E8_EngineHasFourMutexes(t *testing.T) {
    e := &Engine{}
    rt := reflect.TypeOf(e).Elem()
    want := map[string]bool{
        "breakerMu":  false,
        "affinityMu": false,
        "hardwareMu": false,
        "inFlightMu": false,
    }
    for i := 0; i < rt.NumField(); i++ {
        name := rt.Field(i).Name
        if name == "mu" {
            t.Fatalf("E8: Engine must NOT have a single `mu` field (revert to pre-split); found field `mu`")
        }
        if _, isWant := want[name]; isWant {
            want[name] = true
        }
    }
    for field, seen := range want {
        if !seen {
            t.Fatalf("E8: Engine missing concern lock field %q (4-way split incomplete)", field)
        }
    }
}

// Test_E8_DecideConcurrentWritersNoDeadlock: the E8 contract is that NO
// goroutine holds breakerMu + inFlightMu simultaneously — Decide snapshots
// wiring under inFlightMu, releases, then takes breakerMu. If a regression
// re-introduces nesting (e.g. decideLocked reads a wiring field via a path
// that re-acquires inFlightMu while breakerMu is held), a writer blocked on
// inFlightMu would deadlock against a Decide holding breakerMu waiting for
// inFlightMu. This guard runs N Decide readers concurrently with writers
// (SetLocalReady, UpdateConfig, RecordFailure) and asserts ALL finish within a
// bounded timeout — a deadlock hangs past the deadline and FAILs.
func Test_E8_DecideConcurrentWritersNoDeadlock(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool { return map[string]bool{"test-model": true} })
    e.SetLocalInFlight(func() int64 { return 0 })

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    baseCtx := config.WithSnapshot(context.Background(), cfg)

    makeReq := func() *RouteRequest {
        return &RouteRequest{Model: "test-model", Stream: false}
    }

    const deadline = 8 * time.Second
    done := make(chan struct{})
    var wg sync.WaitGroup

    // Readers: hot path.
    for i := 0; i < 16; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 200; j++ {
                ctx := tokenizer.WithTokenBudget(baseCtx, budget)
                dec := e.Decide(ctx, makeReq())
                if dec == nil {
                    panic("E8: Decide returned nil decision")
                }
            }
        }()
    }
    // Writers: swap inFlightMu-owned wiring + breaker mutations.
    for i := 0; i < 4; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            for j := 0; j < 200; j++ {
                e.SetLocalReady(true)
                e.UpdateConfig(cfg)
                e.RecordFailure("local")
                e.RecordSuccess("local")
            }
        }(i)
    }

    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // all goroutines finished — no deadlock, no nesting-induced stall.
    case <-time.After(deadline):
        t.Fatalf("E8: Decide-vs-writer deadlock/lock-nesting detected — did not finish within %s (breakerMu+inFlightMu held simultaneously?)", deadline)
    }
}

// Test_E8_DrainAndApplyNoNesting: DrainAndApply swaps cfg+localQueue under
// inFlightMu, releases, then swaps breakers under breakerMu — two SEPARATE
// windows, never nested. A regression that holds both while rebuilding breakers
// would deadlock against concurrent Decide readers (which take breakerMu). This
// guard runs DrainAndApply concurrently with Decide and asserts completion.
func Test_E8_DrainAndApplyNoNesting(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool { return map[string]bool{"test-model": true} })
    e.SetLocalInFlight(func() int64 { return 0 })

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    baseCtx := config.WithSnapshot(context.Background(), cfg)
    makeReq := func() *RouteRequest { return &RouteRequest{Model: "test-model", Stream: false} }

    const deadline = 8 * time.Second
    done := make(chan struct{})
    var wg sync.WaitGroup

    // Decide readers hold breakerMu.RLock for the dispatch.
    for i := 0; i < 8; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                ctx := tokenizer.WithTokenBudget(baseCtx, budget)
                if dec := e.Decide(ctx, makeReq()); dec == nil {
                    panic("E8: Decide returned nil")
                }
            }
        }()
    }
    // DrainAndApply: the two-window breaker+inFlight rebuild.
    for i := 0; i < 4; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 20; j++ {
                e.DrainAndApply(cfg)
            }
        }()
    }

    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
    case <-time.After(deadline):
        t.Fatalf("E8: DrainAndApply-vs-Decide deadlock/lock-nesting detected — did not finish within %s (breaker rebuild held inFlightMu?)", deadline)
    }
}
