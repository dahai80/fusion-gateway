package pg

import (
    "context"
    "errors"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
)

func TestHWSampleFromMetrics(t *testing.T) {
    now := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
    m := hardware.HardwareMetrics{
        CollectTime:          now,
        TotalMemory:          64 << 30,
        MemoryUsedRatio:      0.55,
        SwapPageInRate:       120,
        SwapPageOutRate:      30,
        GPUDeviceUtilization: 42.5,
        MLXActiveMemory:      16 << 30,
        MLXModelsLoaded:      2,
    }
    s := hwSampleFromMetrics(m, now)
    if s.TS != now {
        t.Fatalf("ts = %v, want %v", s.TS, now)
    }
    if s.SysMemRatio != 0.55 {
        t.Fatalf("sys_mem_ratio = %v", s.SysMemRatio)
    }
    if s.MlxMemRatio != 0.25 {
        t.Fatalf("mlx_mem_ratio = %v, want 0.25 (16/64 GiB)", s.MlxMemRatio)
    }
    if s.SwapPageRate != 150 {
        t.Fatalf("swap_page_rate = %v, want 150 (in+out)", s.SwapPageRate)
    }
    if s.GpuUtil != 42.5 || s.LoadedModels != 2 {
        t.Fatalf("gpu_util=%v loaded_models=%d", s.GpuUtil, s.LoadedModels)
    }
    if s.CollectionError {
        t.Fatal("collection_error must be false without an error")
    }
}

func TestHWSampleFromMetrics_ZeroMemoryNoPanic(t *testing.T) {
    m := hardware.HardwareMetrics{CollectionError: errors.New("io: boom")}
    s := hwSampleFromMetrics(m, time.Now())
    if s.MlxMemRatio != 0 {
        t.Fatalf("mlx_mem_ratio = %v, want 0 on zero TotalMemory", s.MlxMemRatio)
    }
    if !s.CollectionError {
        t.Fatal("collection_error must reflect the collector error")
    }
}

// TestSinkEventAndSampler_NoopUnderFakeWriter: the samples/events paths must
// be inert when the writer is a test fake — StartHWSampler never starts a
// worker, RecordEvent never spawns a goroutine.
func TestSinkEventAndSampler_NoopUnderFakeWriter(t *testing.T) {
    cfg := config.PersistenceConfig{}
    cfg.Queue.Capacity = 8
    s := newSinkWithWriter(cfg, &fakeWriter{})
    s.StartHWSampler(context.Background(), func() hardware.HardwareMetrics {
        t.Fatal("hwFn must not be called under a fake writer")
        return hardware.HardwareMetrics{}
    })
    if s.sampler != nil {
        t.Fatal("sampler must not start under a fake writer")
    }
    // Must not panic; must return without spawning work.
    s.RecordEvent("info", "gateway_started", map[string]any{"version": "test"})
    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()
    s.RecordEventSync(ctx, "info", "gateway_stopped", nil)
    _ = s.Close(ctx)
}
