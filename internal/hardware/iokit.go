package hardware

import (
    "errors"
    "fmt"
    "log/slog"
    "os/exec"
    "runtime"
    "strings"
    "sync"
)

var (
    iokitOnce    sync.Once
    iokitInitErr error //nolint:unused
    iokitReady   bool
)

func collectIOKitGPU(m *HardwareMetrics) error {
    // Non-darwin (e.g. the containerized linux image, #143): IOKit/CoreFoundation
    // frameworks do not exist. initIOKit already no-ops + clears iokitReady on
    // non-darwin, but the prior flow fell through to collectIOKitGPUViaIoreg
    // (shells out to `ioreg`), which also does not exist on linux → a noisy
    // "ioreg command failed" CollectionError every collect tick. Return a tagged
    // unsupported error here instead; it joins CollectionError (EI4) so the
    // router's P0.5 collection_error_protection sees GPU as unavailable rather
    // than shelling into a missing binary. Operators who want no GPU signal at
    // all should set hardware.iokit.enabled=false (the container default).
    if runtime.GOOS != "darwin" {
        return errors.New("iokit gpu collection unsupported on non-darwin (no IOKit framework)")
    }
    initIOKit()

    if iokitReady {
        if err := collectIOKitGPUPurego(m); err != nil {
            slog.Warn("iokit purego failed, falling back to ioreg", "error", err)
            return collectIOKitGPUViaIoreg(m)
        }
        return nil
    }

    return collectIOKitGPUViaIoreg(m)
}

func collectIOKitGPUViaIoreg(m *HardwareMetrics) error {
    slog.Debug("iokit gpu: collecting via ioreg")

    output, err := ioregCmdFn()
    if err != nil {
        return fmt.Errorf("ioreg command failed: %w", err)
    }

    m.GPUCoreCount = getGPUCoreCountFn()

    stats, err := parseIoregPerformanceStats(string(output))
    if err != nil {
        slog.Debug("iokit gpu: failed to parse performance stats", "error", err)
        return nil
    }

    m.GPUDeviceUtilization = stats.DeviceUtilization
    m.GPURendererUtilization = stats.RendererUtilization
    m.GPUTilerUtilization = stats.TilerUtilization
    m.GPUAllocMemory = stats.AllocMemory
    m.GPUInUseMemory = stats.InUseMemory

    slog.Debug("iokit gpu ioreg: collected",
        "device_util", m.GPUDeviceUtilization,
        "renderer_util", m.GPURendererUtilization,
        "alloc_mem", m.GPUAllocMemory,
        "in_use_mem", m.GPUInUseMemory,
    )

    return nil
}

type performanceStats struct {
    DeviceUtilization   float64
    RendererUtilization float64
    TilerUtilization    float64
    AllocMemory         uint64
    InUseMemory         uint64
}

func parseIoregPerformanceStats(output string) (*performanceStats, error) {
    stats := &performanceStats{}
    found := false

    lines := strings.Split(output, "\n")
    inPerfStats := false

    for _, line := range lines {
        trimmed := strings.TrimSpace(line)

        if strings.Contains(line, "PerformanceStatistics") {
            inPerfStats = true
            continue
        }

        if !inPerfStats {
            continue
        }

        if trimmed == "}" || trimmed == ")" {
            break
        }

        key, value, ok := parseIoregKeyValue(trimmed)
        if !ok {
            continue
        }

        found = true
        switch key {
        case "Device Utilization %", "Device Utilization":
            if value <= 100 {
                stats.DeviceUtilization = value / 100.0
            } else {
                stats.DeviceUtilization = value
            }
        case "Renderer Utilization %", "Renderer Utilization":
            if value <= 100 {
                stats.RendererUtilization = value / 100.0
            } else {
                stats.RendererUtilization = value
            }
        case "Tiler Utilization %", "Tiler Utilization":
            if value <= 100 {
                stats.TilerUtilization = value / 100.0
            } else {
                stats.TilerUtilization = value
            }
        case "Alloc system memory", "Allocated Memory":
            stats.AllocMemory = uint64(value)
        case "In use system memory", "In Use Memory":
            stats.InUseMemory = uint64(value)
        }
    }

    if !found {
        return nil, fmt.Errorf("no PerformanceStatistics found in ioreg output")
    }

    return stats, nil
}

func parseIoregKeyValue(line string) (string, float64, bool) {
    eqIdx := strings.Index(line, "=")
    if eqIdx < 0 {
        return "", 0, false
    }

    keyPart := strings.TrimSpace(line[:eqIdx])
    valPart := strings.TrimSpace(line[eqIdx+1:])

    key := strings.Trim(keyPart, "\"")

    if strings.HasPrefix(valPart, "<") {
        return "", 0, false
    }

    var val float64
    if _, err := fmt.Sscanf(valPart, "%f", &val); err != nil {
        return "", 0, false
    }

    return key, val, true
}

var getGPUCoreCountFn = getGPUCoreCount

var ioregCmdFn = defaultIoregCmd

func defaultIoregCmd() ([]byte, error) {
    return exec.Command("ioreg", "-r", "-d", "1", "-w", "0", "-c", "AGXAccelerator").Output()
}

var gpuCoreCountCmdFn = defaultGPUCoreCountCmd

func defaultGPUCoreCountCmd() ([]byte, error) {
    return exec.Command("sysctl", "-n", "hw.gpucorecount").Output()
}

func getGPUCoreCount() int {
    output, err := gpuCoreCountCmdFn()
    if err != nil {
        slog.Debug("iokit: failed to read gpu core count", "error", err)
        return 0
    }

    var count int
    if _, err := fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &count); err != nil {
        slog.Debug("iokit: failed to parse gpu core count", "error", err)
        return 0
    }

    return count
}
