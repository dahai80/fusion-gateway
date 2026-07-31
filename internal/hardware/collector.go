package hardware

import (
    "context"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    "github.com/shirou/gopsutil/v3/mem"
)

type Collector struct {
    cfg     *config.HardwareConfig
    mu      sync.RWMutex
    latest  HardwareMetrics
    cancel  context.CancelFunc

    prevPageIn  uint64
    prevPageOut uint64
    prevSampleTime time.Time
}

func NewCollector(cfg *config.HardwareConfig) *Collector {
    return &Collector{
        cfg: cfg,
    }
}

func (c *Collector) Start(ctx context.Context) {
    if !c.cfg.Enabled {
        slog.Info("hardware collector disabled")
        return
    }

    childCtx, cancel := context.WithCancel(ctx)
    c.cancel = cancel

    safego.Go("hardware_collect_loop", func() { c.collectLoop(childCtx) })
    slog.Info("hardware collector started", "interval", c.cfg.CollectInterval)
}

func (c *Collector) Stop() {
    if c.cancel != nil {
        c.cancel()
    }
    slog.Info("hardware collector stopped")
}

func (c *Collector) Latest() HardwareMetrics {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.latest
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

    // Source 1: gopsutil system memory
    if c.cfg.Gopsutil.Enabled {
        if err := c.collectGopsutil(&m); err != nil {
            slog.Error("gopsutil collection error", "error", err)
            collectErr = err
        }
    }

    // Source 2: Swap page rate (sampling diff)
    if c.cfg.Swap.PageRateSampling {
        c.collectSwapPageRate(&m)
    }

    // Source 3: IOKit GPU metrics
    if c.cfg.IOKit.Enabled {
        if err := collectIOKitGPU(&m); err != nil {
            slog.Error("iokit gpu collection error", "error", err)
            collectErr = err
        }
    }

    // Source 4: MLX /metrics
    if c.cfg.MLXMetrics.Enabled {
        if err := collectMLXMetrics(&m); err != nil {
            slog.Warn("mlx metrics collection error", "error", err)
            // MLX metrics failure is not fatal - local might be offline
        }
    }

    m.CollectionError = collectErr

    c.mu.Lock()
    c.latest = m
    c.mu.Unlock()
}

func (c *Collector) collectGopsutil(m *HardwareMetrics) error {
    vmStat, err := mem.VirtualMemory()
    if err != nil {
        return err
    }

    m.TotalMemory = vmStat.Total
    m.UsedMemory = vmStat.Used
    m.MemoryUsedRatio = vmStat.UsedPercent / 100.0

    swapStat, err := mem.SwapMemory()
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

func (c *Collector) collectSwapPageRate(m *HardwareMetrics) {
    pageIn, pageOut, err := readSwapPageCounts()
    if err != nil {
        slog.Debug("swap page rate sampling failed", "error", err)
        return
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
}
