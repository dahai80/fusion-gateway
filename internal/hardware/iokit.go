package hardware

import (
    "fmt"
    "log/slog"
    "os/exec"
    "runtime"
    "strings"
    "sync"
    "unsafe"

    "github.com/ebitengine/purego"
)

var (
    iokitOnce    sync.Once
    iokitInitErr error //nolint:unused
    iokitReady   bool

    // IOKit function pointers
    ioMainPortFunc                   func(bootstrapPort uint32, mainPort *uint32) int32
    ioServiceMatchingFunc            func(name string) uintptr
    ioServiceGetMatchingServiceFunc  func(mainPort uint32, matching uintptr) uint32
    ioRegistryEntryCreateCFPropertyFunc func(entry uint32, key uintptr, allocator uintptr, options uint32) uintptr
    ioObjectReleaseFunc              func(object uint32) int32

    // CoreFoundation function pointers
    cfStringCreateWithCStringFunc func(alloc uintptr, cStr string, encoding uint32) uintptr
    cfDictionaryGetValueFunc      func(theDict uintptr, key uintptr) uintptr
    cfNumberGetValueFunc          func(number uintptr, theType uintptr, value unsafe.Pointer) uint8
    cfGetTypeIDFunc               func(cf uintptr) uintptr
    cfDictionaryGetTypeIDFunc     func() uintptr
    cfNumberGetTypeIDFunc         func() uintptr
    cfDataGetTypeIDFunc           func() uintptr
    cfDataGetLengthFunc           func(theData uintptr) uintptr
    cfDataGetBytePtrFunc          func(theData uintptr) uintptr
    cfReleaseFunc                 func(cf uintptr)
)

const (
    kCFAllocatorDefault   uintptr = 0
    kCFStringEncodingUTF8 uint32  = 0x08000100
    kCFNumberSInt32Type   uintptr = 3
    kCFNumberSInt64Type   uintptr = 4
    kIOMainPortDefault    uint32  = 0
)

func initIOKit() {
    iokitOnce.Do(func() {
        if runtime.GOOS != "darwin" {
            slog.Info("iokit: skipping, not darwin")
            return
        }

        iokitLib, err := purego.Dlopen(
            "/System/Library/Frameworks/IOKit.framework/IOKit",
            purego.RTLD_NOW|purego.RTLD_GLOBAL,
        )
        if err != nil {
            slog.Debug("iokit: dlopen IOKit failed", "error", err)
            iokitInitErr = err
            return
        }

        cfLib, err := purego.Dlopen(
            "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
            purego.RTLD_NOW|purego.RTLD_GLOBAL,
        )
        if err != nil {
            slog.Debug("iokit: dlopen CoreFoundation failed", "error", err)
            iokitInitErr = err
            return
        }

        purego.RegisterLibFunc(&ioMainPortFunc, iokitLib, "IOMainPort")
        purego.RegisterLibFunc(&ioServiceMatchingFunc, iokitLib, "IOServiceMatching")
        purego.RegisterLibFunc(&ioServiceGetMatchingServiceFunc, iokitLib, "IOServiceGetMatchingService")
        purego.RegisterLibFunc(&ioRegistryEntryCreateCFPropertyFunc, iokitLib, "IORegistryEntryCreateCFProperty")
        purego.RegisterLibFunc(&ioObjectReleaseFunc, iokitLib, "IOObjectRelease")

        purego.RegisterLibFunc(&cfStringCreateWithCStringFunc, cfLib, "CFStringCreateWithCString")
        purego.RegisterLibFunc(&cfDictionaryGetValueFunc, cfLib, "CFDictionaryGetValue")
        purego.RegisterLibFunc(&cfNumberGetValueFunc, cfLib, "CFNumberGetValue")
        purego.RegisterLibFunc(&cfGetTypeIDFunc, cfLib, "CFGetTypeID")
        purego.RegisterLibFunc(&cfDictionaryGetTypeIDFunc, cfLib, "CFDictionaryGetTypeID")
        purego.RegisterLibFunc(&cfNumberGetTypeIDFunc, cfLib, "CFNumberGetTypeID")
        purego.RegisterLibFunc(&cfDataGetTypeIDFunc, cfLib, "CFDataGetTypeID")
        purego.RegisterLibFunc(&cfDataGetLengthFunc, cfLib, "CFDataGetLength")
        purego.RegisterLibFunc(&cfDataGetBytePtrFunc, cfLib, "CFDataGetBytePtr")
        purego.RegisterLibFunc(&cfReleaseFunc, cfLib, "CFRelease")

        iokitReady = true
        slog.Info("iokit: purego bindings initialized successfully")
    })
}

func collectIOKitGPU(m *HardwareMetrics) error {
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

func collectIOKitGPUPurego(m *HardwareMetrics) error {
    defer func() {
        if r := recover(); r != nil {
            slog.Error("iokit purego panic recovered", "error", r)
        }
    }()

    var mainPort uint32
    kr := ioMainPortFunc(kIOMainPortDefault, &mainPort)
    if kr != 0 {
        return fmt.Errorf("IOMainPort failed: %d", kr)
    }

    matchingDict := ioServiceMatchingFunc("AGXAccelerator")
    if matchingDict == 0 {
        return fmt.Errorf("IOServiceMatching returned null")
    }

    service := ioServiceGetMatchingServiceFunc(mainPort, matchingDict)
    if service == 0 {
        return fmt.Errorf("AGXAccelerator service not found")
    }
    defer ioObjectReleaseFunc(service)

    propKey := cfStringCreateWithCStringFunc(kCFAllocatorDefault, "PerformanceStatistics", kCFStringEncodingUTF8)
    if propKey == 0 {
        return fmt.Errorf("CFStringCreate for PerformanceStatistics failed")
    }
    defer cfReleaseFunc(propKey)

    propValue := ioRegistryEntryCreateCFPropertyFunc(service, propKey, kCFAllocatorDefault, 0)
    if propValue == 0 {
        slog.Debug("iokit: PerformanceStatistics property not found")
        m.GPUCoreCount = getGPUCoreCount()
        return nil
    }
    defer cfReleaseFunc(propValue)

    typeID := cfGetTypeIDFunc(propValue)
    dictTypeID := cfDictionaryGetTypeIDFunc()

    if typeID == dictTypeID {
        extractGPUMetricsFromDict(propValue, m)
    } else {
        slog.Debug("iokit: PerformanceStatistics is not CFDictionary", "type_id", typeID)
    }

    m.GPUCoreCount = getGPUCoreCount()

    slog.Debug("iokit gpu purego: collected",
        "device_util", m.GPUDeviceUtilization,
        "renderer_util", m.GPURendererUtilization,
        "tiler_util", m.GPUTilerUtilization,
        "alloc_mem", m.GPUAllocMemory,
        "in_use_mem", m.GPUInUseMemory,
    )

    return nil
}

func extractGPUMetricsFromDict(dict uintptr, m *HardwareMetrics) {
    // Temporary storage for percentage values (raw int64 from IOKit)
    var deviceUtilRaw, rendererUtilRaw, tilerUtilRaw int64

    type metricKey struct {
        cfKey    string
        intDest  *int64
        uintDest *uint64
    }

    keys := []metricKey{
        {cfKey: "Device Utilization %", intDest: &deviceUtilRaw},
        {cfKey: "Renderer Utilization %", intDest: &rendererUtilRaw},
        {cfKey: "Tiler Utilization %", intDest: &tilerUtilRaw},
        {cfKey: "Alloc system memory", uintDest: &m.GPUAllocMemory},
        {cfKey: "In use system memory", uintDest: &m.GPUInUseMemory},
    }

    numberTypeID := cfNumberGetTypeIDFunc()

    for _, k := range keys {
        cfKey := cfStringCreateWithCStringFunc(kCFAllocatorDefault, k.cfKey, kCFStringEncodingUTF8)
        if cfKey == 0 {
            slog.Debug("iokit: failed to create CFString for key", "key", k.cfKey)
            continue
        }

        value := cfDictionaryGetValueFunc(dict, cfKey)
        cfReleaseFunc(cfKey)

        if value == 0 {
            continue
        }

        if cfGetTypeIDFunc(value) != numberTypeID {
            continue
        }

        var intVal int64
        ok := cfNumberGetValueFunc(value, kCFNumberSInt64Type, unsafe.Pointer(&intVal))
        if ok == 0 {
            var int32Val int32
            ok = cfNumberGetValueFunc(value, kCFNumberSInt32Type, unsafe.Pointer(&int32Val))
            if ok != 0 {
                intVal = int64(int32Val)
            }
        }

        if ok != 0 {
            if k.intDest != nil {
                *k.intDest = intVal
            } else if k.uintDest != nil {
                *k.uintDest = uint64(intVal)
            }
        }
    }

    // Convert raw percentage (0-100) to ratio (0.0-1.0)
    m.GPUDeviceUtilization = float64(deviceUtilRaw) / 100.0
    m.GPURendererUtilization = float64(rendererUtilRaw) / 100.0
    m.GPUTilerUtilization = float64(tilerUtilRaw) / 100.0
}

func collectIOKitGPUViaIoreg(m *HardwareMetrics) error {
    slog.Debug("iokit gpu: collecting via ioreg")

    cmd := exec.Command("ioreg", "-r", "-d", "1", "-w", "0", "-c", "AGXAccelerator")
    output, err := cmd.Output()
    if err != nil {
        return fmt.Errorf("ioreg command failed: %w", err)
    }

    m.GPUCoreCount = getGPUCoreCount()

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

func getGPUCoreCount() int {
    cmd := exec.Command("sysctl", "-n", "hw.gpucorecount")
    output, err := cmd.Output()
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
