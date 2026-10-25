// P3b stage 1 (plan §15.4): fg_hw_samples writer (30s hardware snapshots)
// and fg_gateway_events writer (structured lifecycle events replacing
// stdout-only run logs). Both are best-effort side channels — a failure logs
// and is retried on the next tick, never blocks the request path.
package pg

import (
    "context"
    "encoding/json"
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// hwSampleInterval: plan §15.4 — one row per 30s.
const hwSampleInterval = 30 * time.Second

// EventRecorder is the optional interface other components type-assert on the
// sink (server config-reload hook, main.go lifecycle stamps) so the persist.Sink
// contract itself stays request-log-only.
type EventRecorder interface {
    RecordEvent(level, event string, detail map[string]any)
}

// HWSample is one fg_hw_samples row.
type HWSample struct {
    TS              time.Time
    SysMemRatio     float64
    MlxMemRatio     float64
    SwapPageRate    float64
    GpuUtil         float64
    LoadedModels    int
    CollectionError bool
}

// hwSampleFromMetrics projects the collector's latest snapshot into a row.
// Pure function — unit-tested without PG.
func hwSampleFromMetrics(m hardware.HardwareMetrics, now time.Time) HWSample {
    mlxRatio := 0.0
    if m.TotalMemory > 0 {
        mlxRatio = float64(m.MLXActiveMemory) / float64(m.TotalMemory)
    }
    return HWSample{
        TS:              now,
        SysMemRatio:     m.MemoryUsedRatio,
        MlxMemRatio:     mlxRatio,
        SwapPageRate:    float64(m.SwapPageInRate + m.SwapPageOutRate),
        GpuUtil:         m.GPUDeviceUtilization,
        LoadedModels:    m.MLXModelsLoaded,
        CollectionError: m.CollectionError != nil,
    }
}

// writeHWSample inserts one snapshot; a ts PK collision (restart within the
// same second) is ignored.
func (w *copyWriter) writeHWSample(ctx context.Context, s HWSample) error {
    _, err := w.pool.Exec(ctx,
        `INSERT INTO fg_hw_samples
             (ts, sys_mem_ratio, mlx_mem_ratio, swap_page_rate, gpu_util,
              loaded_models, collection_error)
         VALUES ($1,$2,$3,$4,$5,$6,$7)
         ON CONFLICT (ts) DO NOTHING`,
        s.TS, s.SysMemRatio, s.MlxMemRatio, s.SwapPageRate, s.GpuUtil,
        s.LoadedModels, s.CollectionError)
    return err
}

// writeEvent inserts one fg_gateway_events row.
func (w *copyWriter) writeEvent(ctx context.Context, level, event string, detail []byte) error {
    _, err := w.pool.Exec(ctx,
        `INSERT INTO fg_gateway_events (level, event, detail) VALUES ($1,$2,$3)`,
        level, event, detail)
    return err
}

// StartHWSampler launches the 30s snapshot worker (lifecycle.Worker — joined
// by Close). hwFn is typically hardware.Collector.Latest. No-op when the
// writer is a test fake or persistence is shutting down.
func (s *Sink) StartHWSampler(parent context.Context, hwFn func() hardware.HardwareMetrics) {
    if _, ok := s.writer.(*copyWriter); !ok {
        return
    }
    s.sampler = lifecycle.Start(parent, "pg-hw-sampler", func(ctx context.Context) {
        s.hwSampleLoop(ctx, hwFn)
    })
    slog.Info("pg hw sampler started", "interval", hwSampleInterval.String())
}

// hwSampleLoop is the sampler worker body: immediate first sample, then every
// 30s. Insert failures log at WARN and wait for the next tick — a down PG
// must not spin this loop.
func (s *Sink) hwSampleLoop(ctx context.Context, hwFn func() hardware.HardwareMetrics) {
    s.sampleOnce(ctx, hwFn)
    ticker := time.NewTicker(hwSampleInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-s.stopCh:
            return
        case <-ticker.C:
            s.sampleOnce(ctx, hwFn)
        }
    }
}

func (s *Sink) sampleOnce(ctx context.Context, hwFn func() hardware.HardwareMetrics) {
    cw, ok := s.writer.(*copyWriter)
    if !ok {
        return
    }
    sample := hwSampleFromMetrics(hwFn(), time.Now().UTC())
    writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    if err := cw.writeHWSample(writeCtx, sample); err != nil {
        slog.Warn("pg hw sample write failed", "error", err)
        return
    }
    if sample.CollectionError {
        slog.Warn("pg hw sample recorded with collection_error", "ts", sample.TS)
    }
}

// RecordEvent persists a structured gateway lifecycle event (plan §15.4:
// startup/shutdown, hot reload, PG degraded/recovered, breaker trips).
// Async + best-effort: events are audit flavor and must never block the
// caller — a 5s-bounded one-shot goroutine does the insert; failures log only.
func (s *Sink) RecordEvent(level, event string, detail map[string]any) {
    cw, ok := s.writer.(*copyWriter)
    if !ok || s.stopOnce.Load() {
        return
    }
    payload := marshalEventDetail(event, detail)
    safego.Go("pg-event-insert", func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := cw.writeEvent(ctx, level, event, payload); err != nil {
            slog.Warn("pg gateway event write failed", "event", event, "error", err)
        }
    })
}

// RecordEventSync is the shutdown-path variant: blocks until the insert lands
// or ctx expires — the async RecordEvent races sink.Close (pool goes away,
// event lost), and gateway_stopped must survive the drain by design.
func (s *Sink) RecordEventSync(ctx context.Context, level, event string, detail map[string]any) {
    cw, ok := s.writer.(*copyWriter)
    if !ok {
        return
    }
    if err := cw.writeEvent(ctx, level, event, marshalEventDetail(event, detail)); err != nil {
        slog.Warn("pg gateway event write failed", "event", event, "error", err)
    }
}

func marshalEventDetail(event string, detail map[string]any) []byte {
    payload, err := json.Marshal(detail)
    if err != nil {
        slog.Warn("pg gateway event marshal failed", "event", event, "error", err)
        return []byte(`{}`)
    }
    return payload
}
