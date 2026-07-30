package hardware

import (
    "time"
)

type HardwareMetrics struct {
    CollectTime     time.Time
    CollectionError error

    // System memory (gopsutil - reliable)
    TotalMemory     uint64
    UsedMemory      uint64
    MemoryUsedRatio float64

    // Swap (gopsutil capacity + vm.stat page rate)
    SwapTotal      uint64
    SwapUsed       uint64
    SwapPageInRate uint64
    SwapPageOutRate uint64

    // GPU (IOKit AGXAccelerator, ebitengine/purego zero CGo)
    GPUDeviceUtilization   float64
    GPURendererUtilization float64
    GPUTilerUtilization    float64
    GPUAllocMemory         uint64
    GPUInUseMemory         uint64
    GPUCoreCount           int

    // MLX application metrics (fusion-mlx /metrics)
    MLXActiveMemory        uint64
    MLXModelsLoaded        int
    MLXRequestsTotal       uint64
    MLXInferenceQueueDepth int
}
