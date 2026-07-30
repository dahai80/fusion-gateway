package router

import (
    "context"
    "sync/atomic"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// Benchmark routing decision hot paths
// Called by: go test -bench=. ./internal/router/
// User instruction: "继续phase 3" — Task #19 benchmark tests

func newBenchEngine() (*Engine, *hardware.Collector) {
    snap := &config.ConfigSnapshot{
        Config: config.DefaultConfig(),
    }
    snap.Config.Routing.TokenThreshold = 50
    snap.Config.Routing.LocalPriority.Enabled = true

    hwCollector := hardware.NewCollector(&snap.Config.Hardware)
    engine := NewEngine(snap, hwCollector)
    engine.SetLocalReady(true)

    var inFlight atomic.Int64
    engine.SetLocalInFlight(inFlight.Load)
    engine.SetLocalModels(func() map[string]bool {
        return map[string]bool{"qwen3-0.6b": true, "test-model": true}
    })

    return engine, hwCollector
}

func benchCtx(b *testing.B, inputTokens int) context.Context {
    ctx := context.Background()
    snap := &config.ConfigSnapshot{
        Config: config.DefaultConfig(),
    }
    snap.Config.Routing.TokenThreshold = 50
    snap.Config.Routing.LocalPriority.Enabled = true
    snap.Config.Routing.LocalPriority.MaxSystemMemoryRatio = 0.9
    snap.Config.Hardware.CollectionErrorProtection = true
    ctx = config.WithSnapshot(ctx, snap)

    budget := tokenizer.TokenBudget{
        InputTokens:         inputTokens,
        PredictOutputTokens: inputTokens / 2,
        TotalBudget:         inputTokens + inputTokens/2,
    }
    ctx = tokenizer.WithTokenBudget(ctx, budget)
    return ctx
}

func BenchmarkDecide_LocalShort(b *testing.B) {
    engine, _ := newBenchEngine()
    ctx := benchCtx(b, 10)
    req := &RouteRequest{Model: "test-model"}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        engine.Decide(ctx, req)
    }
}

func BenchmarkDecide_CloudLong(b *testing.B) {
    engine, _ := newBenchEngine()
    ctx := benchCtx(b, 2000)
    req := &RouteRequest{Model: "test-model"}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        engine.Decide(ctx, req)
    }
}

func BenchmarkDecide_ModelNotLocal(b *testing.B) {
    engine, _ := newBenchEngine()
    ctx := benchCtx(b, 10)
    req := &RouteRequest{Model: "nonexistent-model"}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        engine.Decide(ctx, req)
    }
}

func BenchmarkDecide_Parallel(b *testing.B) {
    engine, _ := newBenchEngine()
    req := &RouteRequest{Model: "test-model"}

    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        i := 0
        for pb.Next() {
            tokens := 10 + (i % 100)
            ctx := benchCtx(b, tokens)
            engine.Decide(ctx, req)
            i++
        }
    })
}
