package hardware

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/shirou/gopsutil/v3/mem"
)

type Collector struct {
    cfg     *config.HardwareConfig
    mu      sync.RWMutex
    latest  HardwareMetrics
    worker  *lifecycle.Worker

    prevPageIn  uint64
    prevPageOut uint64
    prevSampleTime time.Time
}

func NewCollector(cfg *config.HardwareConfig) *Collector {
    // Wire hardware.mlx_metrics.url into the package-level mlxMetricsURL. The
    // collector used a hardcoded http://127.0.0.1:11434/metrics and never read
    // the config key, so in a container (where fusion-mlx is on the Docker host
    // via host.docker.internal, not the in-container loopback) /metrics dialed
    // 127.0.0.1:11434 and failed every collect tick. cfg.MLXMetrics.URL is the
    // documented override (DefaultConfig seeds 127.0.0.1 for bare-metal parity).
    if cfg != nil && cfg.MLXMetrics.URL != "" {
        mlxMetricsURL = cfg.MLXMetrics.URL
    }
    return &Collector{
        cfg: cfg,
    }
}

func (c *Collector) Start(ctx context.Context) {
    if !c.cfg.Enabled {
        slog.Info("hardware collector disabled")
        return
    }

    if c.cfg.CollectInterval <= 0 {
        slog.Warn("hardware collect_interval not set or non-positive, defaulting to 5s", "configured", c.cfg.CollectInterval)
        c.cfg.CollectInterval = 5 * time.Second
    }

    // EI10: launch the collect loop as a tracked lifecycle.Worker so Stop joins
    // (waits for exit) instead of just cancelling and racing a mid-collect stop.
    c.worker = lifecycle.Start(ctx, "hardware_collect_loop", c.collectLoop)
    slog.Info("hardware collector started", "interval", c.cfg.CollectInterval)
}

func (c *Collector) Stop() {
    if c.worker != nil {
        c.worker.Stop()
    } else {
        slog.Info("hardware collector stopped (never started)")
    }
}

func (c *Collector) Latest() HardwareMetrics {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.latest
}

func (c *Collector) SetLatestForTest(m HardwareMetrics) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.latest = m
}

func (c *Collector) collectLoop(ctx context.Context) {
    ticker := time.NewTicker(c.cfg.CollectInterval)
    defer ticker.Stop()

    c.collect()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.collect()
        }
    }
}

func (c *Collector) collect() {
    var m HardwareMetrics
    m.CollectTime = time.Now()
    var collectErr error

    // Source 1: gopsutil system memory. EI4: tag the failing subsystem so a
    // partial collect (gopsutil ok but iokit fail, or swap fail) surfaces WHICH
    // source failed. The prior `collectErr = err` clobbered an earlier failure
    // when a later source also failed — lost the first signal. errors.Join
    // preserves every failing subsystem in one error.
    if c.cfg.Gopsutil.Enabled {
        if err := collectGopsutilFn(c, &m); err != nil {
            slog.Error("gopsutil collection error", "error", err)
            collectErr = errors.Join(collectErr, fmt.Errorf("gopsutil: %w", err))
        }
    }

    // Source 2: Swap page rate (sampling diff). EI4: failure here previously
    // returned zero SwapPageInRate/OutRate with NO CollectionError — the void
    // signature only logged Debug, so collectErr stayed nil. The router's P2
    // swap-thrashing check then read 0 (never trips) AND P0.5
    // collection_error_protection never tripped (no error). A swap-read failure
    // silently disabled both overload signals. Now the func returns its error
    // and it is aggregated into CollectionError like the other sources.
    if c.cfg.Swap.PageRateSampling {
        if err := c.collectSwapPageRate(&m); err != nil {
            slog.Error("swap page rate collection error", "error", err)
            collectErr = errors.Join(collectErr, fmt.Errorf("swap_page_rate: %w", err))
        }
    }

    // Source 3: IOKit GPU metrics. EI4: tagged + joined (see Source 1).
    if c.cfg.IOKit.Enabled {
        if err := collectIOKitGPUFn(&m); err != nil {
            slog.Error("iokit gpu collection error", "error", err)
            collectErr = errors.Join(collectErr, fmt.Errorf("iokit_gpu: %w", err))
        }
    }

    // Source 4: MLX /metrics
    if c.cfg.MLXMetrics.Enabled {
        if err := collectMLXMetricsFn(&m); err != nil {
            slog.Warn("mlx metrics collection error", "error", err)
            // MLX metrics failure is not fatal - local might be offline
        }
    }

    m.CollectionError = collectErr

    c.mu.Lock()
    c.latest = m
    c.mu.Unlock()

    // Publish to Prometheus (#96): hw* gauges were declared but never set, so
    // /metrics reported stale zeros while /stats JSON showed live values.
    observability.UpdateHardwareMetrics(
        m.MemoryUsedRatio,
        m.SwapUsed, m.SwapPageInRate, m.SwapPageOutRate,
        m.GPUDeviceUtilization, m.GPURendererUtilization, m.GPUTilerUtilization,
        m.GPUInUseMemory, m.GPUAllocMemory,
        m.MLXActiveMemory,
        m.MLXModelsLoaded, m.MLXInferenceQueueDepth,
    )
    if collectErr != nil {
        // RecordCollectionError was declared but never called (#96).
        observability.RecordCollectionError("hardware_collect")
    }
}

var memVirtualMemoryFn = mem.VirtualMemory

var memSwapMemoryFn = mem.SwapMemory

func (c *Collector) collectGopsutil(m *HardwareMetrics) error {
    vmStat, err := memVirtualMemoryFn()
    if err != nil {
        return err
    }

    m.TotalMemory = vmStat.Total
    m.UsedMemory = vmStat.Used
    m.MemoryUsedRatio = vmStat.UsedPercent / 100.0

    swapStat, err := memSwapMemoryFn()
    if err != nil {
        return err
    }

    m.SwapTotal = swapStat.Total
    m.SwapUsed = swapStat.Used

    // Anomaly check: 0% or 100% memory usage is suspicious on macOS
    if m.MemoryUsedRatio == 0 || m.MemoryUsedRatio >= 0.999 {
        slog.Warn("suspicious memory ratio from gopsutil",
            "ratio", m.MemoryUsedRatio,
            "total", m.TotalMemory,
            "used", m.UsedMemory,
        )
    }

    return nil
}

var readSwapPageCountsFn = readSwapPageCounts

var collectGopsutilFn = (*Collector).collectGopsutil

var collectIOKitGPUFn = collectIOKitGPU

var collectMLXMetricsFn = collectMLXMetrics

// collectSwapPageRate returns an error when the vm.stat read fails. EI4: this
// was previously void — a swap-read failure left SwapPageInRate/OutRate zero
// with no error signal, so the router's P2 swap-thrashing check and the P0.5
// collection_error_protection guard both silently disabled. The caller now
// aggregates this into CollectionError so hardware protection fires.
func (c *Collector) collectSwapPageRate(m *HardwareMetrics) error {
    pageIn, pageOut, err := readSwapPageCountsFn()
    if err != nil {
        slog.Warn("swap page rate sampling failed", "error", err)
        return err
    }

    now := time.Now()
    if !c.prevSampleTime.IsZero() {
        elapsed := now.Sub(c.prevSampleTime).Seconds()
        if elapsed > 0 {
            m.SwapPageInRate = uint64(float64(pageIn-c.prevPageIn) / elapsed)
            m.SwapPageOutRate = uint64(float64(pageOut-c.prevPageOut) / elapsed)
        }
    }

    c.prevPageIn = pageIn
    c.prevPageOut = pageOut
    c.prevSampleTime = now
    return nil
}
