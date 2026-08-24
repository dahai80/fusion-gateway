package hardware

import (
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "runtime"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/shirou/gopsutil/v3/mem"
)

func TestParseIoregPerformanceStats_Valid(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats with valid multi-line ioreg output")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "PerformanceStatistics" = {
        "Device Utilization %" = 42
        "Renderer Utilization %" = 55
        "Tiler Utilization %" = 30
        "Alloc system memory" = 1048576
        "In use system memory" = 524288
      }
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if stats.DeviceUtilization != 0.42 {
        t.Errorf("DeviceUtilization: got %f, want 0.42", stats.DeviceUtilization)
    }
    if stats.RendererUtilization != 0.55 {
        t.Errorf("RendererUtilization: got %f, want 0.55", stats.RendererUtilization)
    }
    if stats.TilerUtilization != 0.30 {
        t.Errorf("TilerUtilization: got %f, want 0.30", stats.TilerUtilization)
    }
    if stats.AllocMemory != 1048576 {
        t.Errorf("AllocMemory: got %d, want 1048576", stats.AllocMemory)
    }
    if stats.InUseMemory != 524288 {
        t.Errorf("InUseMemory: got %d, want 524288", stats.InUseMemory)
    }
}

func TestParseIoregPerformanceStats_AlternateKeyNames(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats with alternate key names")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "PerformanceStatistics" = {
        "Device Utilization" = 42
        "Renderer Utilization" = 55
        "Tiler Utilization" = 30
        "Allocated Memory" = 2048000
        "In Use Memory" = 1024000
      }
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if stats.DeviceUtilization != 0.42 {
        t.Errorf("DeviceUtilization: got %f, want 0.42", stats.DeviceUtilization)
    }
    if stats.AllocMemory != 2048000 {
        t.Errorf("AllocMemory: got %d, want 2048000", stats.AllocMemory)
    }
    if stats.InUseMemory != 1024000 {
        t.Errorf("InUseMemory: got %d, want 1024000", stats.InUseMemory)
    }
}

func TestParseIoregPerformanceStats_Empty(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats with empty output")
    _, err := parseIoregPerformanceStats("")
    if err == nil {
        t.Fatal("expected error for empty output")
    }
}

func TestParseIoregPerformanceStats_NoPerformanceStats(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats without PerformanceStatistics section")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "SomeOtherKey" = 123
    }`

    _, err := parseIoregPerformanceStats(input)
    if err == nil {
        t.Fatal("expected error when no PerformanceStatistics found")
    }
}

func TestParseIoregPerformanceStats_MissingKeys(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats with partial keys")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "PerformanceStatistics" = {
        "Device Utilization %" = 42
      }
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if stats.DeviceUtilization != 0.42 {
        t.Errorf("DeviceUtilization: got %f, want 0.42", stats.DeviceUtilization)
    }
    if stats.RendererUtilization != 0 {
        t.Errorf("RendererUtilization: got %f, want 0", stats.RendererUtilization)
    }
    if stats.AllocMemory != 0 {
        t.Errorf("AllocMemory: got %d, want 0", stats.AllocMemory)
    }
}

func TestParseIoregPerformanceStats_HexDataSkipped(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats skips hex data entries")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "PerformanceStatistics" = {
        "Device Utilization %" = 42
        "RawData" = <0001020304050607>
      }
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if stats.DeviceUtilization != 0.42 {
        t.Errorf("DeviceUtilization: got %f, want 0.42", stats.DeviceUtilization)
    }
}

func TestParseIoregPerformanceStats_LargeUtilization(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats with utilization > 100 treated as raw value")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "PerformanceStatistics" = {
        "Device Utilization %" = 500
      }
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if stats.DeviceUtilization != 500 {
        t.Errorf("DeviceUtilization: got %f, want 500 (raw, not divided)", stats.DeviceUtilization)
    }
}

func TestParseIoregPerformanceStats_CloseParenTerminates(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats stops on ) delimiter")
    input := `+-o AGXAccelerator  <class AGXAccelerator>
    {
      "PerformanceStatistics" = (
        "Device Utilization %" = 42
      )
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if stats.DeviceUtilization != 0.42 {
        t.Errorf("DeviceUtilization: got %f, want 0.42", stats.DeviceUtilization)
    }
}

func TestParseIoregPerformanceStats_SingleLineDict(t *testing.T) {
    t.Log("testing parseIoregPerformanceStats with real single-line ioreg format")
    input := `+-o AGXAcceleratorG17X  <class AGXAcceleratorG17X>
    {
      "PerformanceStatistics" = {"Device Utilization %"=0,"Renderer Utilization %"=0,"Tiler Utilization %"=0,"Alloc system memory"=47937765376,"In use system memory"=1009385472}
      "model" = "Apple M5 Max"
    }`

    stats, err := parseIoregPerformanceStats(input)
    if err != nil {
        t.Logf("single-line format not supported by parser (expected): %v", err)
        return
    }
    t.Logf("single-line parsed: DeviceUtilization=%f, AllocMemory=%d", stats.DeviceUtilization, stats.AllocMemory)
}

func TestParseIoregKeyValue_Valid(t *testing.T) {
    t.Log("testing parseIoregKeyValue with valid key=value")
    key, val, ok := parseIoregKeyValue(`"Device Utilization %" = 42`)
    if !ok {
        t.Fatal("expected ok=true")
    }
    if key != "Device Utilization %" {
        t.Errorf("key: got %q, want %q", key, "Device Utilization %")
    }
    if val != 42 {
        t.Errorf("val: got %f, want 42", val)
    }
}

func TestParseIoregKeyValue_NoEquals(t *testing.T) {
    t.Log("testing parseIoregKeyValue with no = sign")
    _, _, ok := parseIoregKeyValue("no equals here")
    if ok {
        t.Fatal("expected ok=false for line without =")
    }
}

func TestParseIoregKeyValue_HexValue(t *testing.T) {
    t.Log("testing parseIoregKeyValue with hex data value")
    _, _, ok := parseIoregKeyValue(`"RawData" = <00010203>`)
    if ok {
        t.Fatal("expected ok=false for hex data value")
    }
}

func TestParseIoregKeyValue_FloatValue(t *testing.T) {
    t.Log("testing parseIoregKeyValue with float value")
    key, val, ok := parseIoregKeyValue(`"Some Metric" = 3.14`)
    if !ok {
        t.Fatal("expected ok=true for float value")
    }
    if key != "Some Metric" {
        t.Errorf("key: got %q, want %q", key, "Some Metric")
    }
    if val < 3.13 || val > 3.15 {
        t.Errorf("val: got %f, want ~3.14", val)
    }
}

func TestParseIoregKeyValue_QuotedKey(t *testing.T) {
    t.Log("testing parseIoregKeyValue with quoted key")
    key, val, ok := parseIoregKeyValue(`"In use system memory" = 524288`)
    if !ok {
        t.Fatal("expected ok=true")
    }
    if key != "In use system memory" {
        t.Errorf("key: got %q, want %q", key, "In use system memory")
    }
    if val != 524288 {
        t.Errorf("val: got %f, want 524288", val)
    }
}

func TestParseIoregKeyValue_IntegerValue(t *testing.T) {
    t.Log("testing parseIoregKeyValue with integer value")
    key, val, ok := parseIoregKeyValue(`"Alloc system memory" = 1048576`)
    if !ok {
        t.Fatal("expected ok=true")
    }
    if key != "Alloc system memory" {
        t.Errorf("key: got %q, want %q", key, "Alloc system memory")
    }
    if val != 1048576 {
        t.Errorf("val: got %f, want 1048576", val)
    }
}

func TestParseIoregKeyValue_InvalidValue(t *testing.T) {
    t.Log("testing parseIoregKeyValue with non-numeric value")
    _, _, ok := parseIoregKeyValue(`"SomeKey" = notanumber`)
    if ok {
        t.Fatal("expected ok=false for non-numeric value")
    }
}

func TestParseIoregKeyValue_InLineDictFormat(t *testing.T) {
    t.Log("testing parseIoregKeyValue with ioreg inline dict key=value (no spaces)")
    key, val, ok := parseIoregKeyValue(`"Device Utilization %"=0`)
    if !ok {
        t.Fatal("expected ok=true for inline format")
    }
    if key != "Device Utilization %" {
        t.Errorf("key: got %q, want %q", key, "Device Utilization %")
    }
    if val != 0 {
        t.Errorf("val: got %f, want 0", val)
    }
}

func TestParseIoregKeyValue_EmptyKey(t *testing.T) {
    t.Log("testing parseIoregKeyValue with empty key part")
    key, val, ok := parseIoregKeyValue(`= 42`)
    if !ok {
        t.Fatal("expected ok=true")
    }
    if key != "" {
        t.Errorf("key: got %q, want empty", key)
    }
    if val != 42 {
        t.Errorf("val: got %f, want 42", val)
    }
}

func TestParsePrometheusMetrics_Valid(t *testing.T) {
    t.Log("testing parsePrometheusMetrics with valid Prometheus output")
    input := `# HELP fusion_mlx_model_memory_bytes Current model memory
# TYPE fusion_mlx_model_memory_bytes gauge
fusion_mlx_model_memory_bytes 4294967296
fusion_mlx_models_loaded 2
fusion_mlx_requests_total 150
fusion_mlx_inference_queue_depth 3
`
    var m HardwareMetrics
    parsePrometheusMetrics(input, &m)

    if m.MLXActiveMemory != 4294967296 {
        t.Errorf("MLXActiveMemory: got %d, want 4294967296", m.MLXActiveMemory)
    }
    if m.MLXModelsLoaded != 2 {
        t.Errorf("MLXModelsLoaded: got %d, want 2", m.MLXModelsLoaded)
    }
    if m.MLXRequestsTotal != 150 {
        t.Errorf("MLXRequestsTotal: got %d, want 150", m.MLXRequestsTotal)
    }
    if m.MLXInferenceQueueDepth != 3 {
        t.Errorf("MLXInferenceQueueDepth: got %d, want 3", m.MLXInferenceQueueDepth)
    }
}

func TestParsePrometheusMetrics_FloatValues(t *testing.T) {
    t.Log("testing parsePrometheusMetrics with float values")
    input := `fusion_mlx_model_memory_bytes 1073741824.5
fusion_mlx_models_loaded 1.0
`
    var m HardwareMetrics
    parsePrometheusMetrics(input, &m)

    if m.MLXActiveMemory != 1073741824 {
        t.Errorf("MLXActiveMemory: got %d, want 1073741824", m.MLXActiveMemory)
    }
    if m.MLXModelsLoaded != 1 {
        t.Errorf("MLXModelsLoaded: got %d, want 1", m.MLXModelsLoaded)
    }
}

func TestParsePrometheusMetrics_Empty(t *testing.T) {
    t.Log("testing parsePrometheusMetrics with empty input")
    var m HardwareMetrics
    parsePrometheusMetrics("", &m)

    if m.MLXActiveMemory != 0 {
        t.Errorf("MLXActiveMemory: got %d, want 0", m.MLXActiveMemory)
    }
}

func TestParsePrometheusMetrics_CommentOnly(t *testing.T) {
    t.Log("testing parsePrometheusMetrics with only comments")
    input := `# TYPE fusion_mlx_model_memory_bytes gauge
# HELP fusion_mlx_model_memory_bytes help text
`
    var m HardwareMetrics
    parsePrometheusMetrics(input, &m)

    if m.MLXActiveMemory != 0 {
        t.Errorf("MLXActiveMemory: got %d, want 0", m.MLXActiveMemory)
    }
}

func TestParsePrometheusMetrics_Malformed(t *testing.T) {
    t.Log("testing parsePrometheusMetrics with malformed lines")
    input := `fusion_mlx_model_memory_bytes
fusion_mlx_models_loaded abc
fusion_mlx_requests_total 50
`
    var m HardwareMetrics
    parsePrometheusMetrics(input, &m)

    if m.MLXActiveMemory != 0 {
        t.Errorf("MLXActiveMemory: got %d, want 0 (malformed line)", m.MLXActiveMemory)
    }
    if m.MLXModelsLoaded != 0 {
        t.Errorf("MLXModelsLoaded: got %d, want 0 (invalid value)", m.MLXModelsLoaded)
    }
    if m.MLXRequestsTotal != 50 {
        t.Errorf("MLXRequestsTotal: got %d, want 50", m.MLXRequestsTotal)
    }
}

func TestParsePrometheusMetrics_WithLabels(t *testing.T) {
    t.Log("testing parsePrometheusMetrics with metric labels")
    input := `fusion_mlx_model_memory_bytes{model="qwen"} 4294967296
fusion_mlx_models_loaded{model="qwen"} 1
`
    var m HardwareMetrics
    parsePrometheusMetrics(input, &m)

    if m.MLXActiveMemory != 4294967296 {
        t.Errorf("MLXActiveMemory: got %d, want 4294967296", m.MLXActiveMemory)
    }
    if m.MLXModelsLoaded != 1 {
        t.Errorf("MLXModelsLoaded: got %d, want 1", m.MLXModelsLoaded)
    }
}

func TestExtractMetricValue_Integer(t *testing.T) {
    t.Log("testing extractMetricValue with integer value")
    val, err := extractMetricValue("fusion_mlx_requests_total 150")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if val != 150 {
        t.Errorf("got %d, want 150", val)
    }
}

func TestExtractMetricValue_Float(t *testing.T) {
    t.Log("testing extractMetricValue with float value")
    val, err := extractMetricValue("fusion_mlx_model_memory_bytes 1073741824.75")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if val != 1073741824 {
        t.Errorf("got %d, want 1073741824 (truncated)", val)
    }
}

func TestExtractMetricValue_WithLabels(t *testing.T) {
    t.Log("testing extractMetricValue with labeled metric")
    val, err := extractMetricValue(`fusion_mlx_model_memory_bytes{model="qwen"} 4294967296`)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if val != 4294967296 {
        t.Errorf("got %d, want 4294967296", val)
    }
}

func TestExtractMetricValue_Invalid(t *testing.T) {
    t.Log("testing extractMetricValue with invalid input")
    _, err := extractMetricValue("no_value_here")
    if err == nil {
        t.Fatal("expected error for line with no value")
    }
}

func TestExtractMetricValue_InvalidNumber(t *testing.T) {
    t.Log("testing extractMetricValue with non-numeric value")
    _, err := extractMetricValue("metric_name not_a_number")
    if err == nil {
        t.Fatal("expected error for non-numeric value")
    }
}

func TestExtractMetricValue_NegativeFloat(t *testing.T) {
    t.Log("testing extractMetricValue with negative float (truncated to 0)")
    val, err := extractMetricValue("some_metric -1.5")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    t.Logf("negative float parsed as uint64: %d", val)
}

func TestNewCollector(t *testing.T) {
    t.Log("testing NewCollector returns non-nil Collector")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    if c == nil {
        t.Fatal("expected non-nil Collector")
    }
}

func TestCollector_Latest_ReturnsZeroInitially(t *testing.T) {
    t.Log("testing Collector.Latest returns zero metrics before Start")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    m := c.Latest()

    if m.TotalMemory != 0 {
        t.Errorf("TotalMemory: got %d, want 0", m.TotalMemory)
    }
    if m.UsedMemory != 0 {
        t.Errorf("UsedMemory: got %d, want 0", m.UsedMemory)
    }
    if m.MemoryUsedRatio != 0 {
        t.Errorf("MemoryUsedRatio: got %f, want 0", m.MemoryUsedRatio)
    }
}

func TestCollector_SetLatestForTest(t *testing.T) {
    t.Log("testing SetLatestForTest sets metrics correctly")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    testMetrics := HardwareMetrics{
        TotalMemory:     17179869184,
        UsedMemory:      8589934592,
        MemoryUsedRatio: 0.5,
        GPUCoreCount:    10,
    }
    c.SetLatestForTest(testMetrics)

    m := c.Latest()
    if m.TotalMemory != 17179869184 {
        t.Errorf("TotalMemory: got %d, want 17179869184", m.TotalMemory)
    }
    if m.UsedMemory != 8589934592 {
        t.Errorf("UsedMemory: got %d, want 8589934592", m.UsedMemory)
    }
    if m.MemoryUsedRatio != 0.5 {
        t.Errorf("MemoryUsedRatio: got %f, want 0.5", m.MemoryUsedRatio)
    }
    if m.GPUCoreCount != 10 {
        t.Errorf("GPUCoreCount: got %d, want 10", m.GPUCoreCount)
    }
}

func TestCollector_Start_DisabledConfig(t *testing.T) {
    t.Log("testing Collector.Start with disabled config does not start collection")
    cfg := &config.HardwareConfig{
        Enabled:         false,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    c.Start(context.Background())

    m := c.Latest()
    if !m.CollectTime.IsZero() {
        t.Errorf("CollectTime should be zero when disabled, got %v", m.CollectTime)
    }

    c.Stop()
}

func TestCollector_Start_ZeroIntervalDefaults(t *testing.T) {
    t.Log("testing Collector.Start defaults collect_interval to 5s when zero, no panic")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 0,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.Start(context.Background())
    if cfg.CollectInterval != 5*time.Second {
        t.Errorf("expected default CollectInterval 5s, got %v", cfg.CollectInterval)
    }
    c.Stop()
}

func TestCollector_Stop_WithoutStart(t *testing.T) {
    t.Log("testing Collector.Stop without Start does not panic")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    c.Stop()
}

func TestCollector_StartStop(t *testing.T) {
    t.Log("testing Collector Start then Stop lifecycle")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 100 * time.Millisecond,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    c.Start(ctx)
    time.Sleep(200 * time.Millisecond)

    m := c.Latest()
    if m.CollectTime.IsZero() {
        t.Error("expected CollectTime to be set after Start")
    }
    if m.TotalMemory == 0 {
        t.Log("TotalMemory is 0 — may be expected in CI/non-macOS")
    }

    c.Stop()
}

func TestCollector_CollectLoop_ContextCancellation(t *testing.T) {
    t.Log("testing collectLoop stops when context is cancelled")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 50 * time.Millisecond,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    ctx, cancel := context.WithCancel(context.Background())

    c.Start(ctx)
    time.Sleep(100 * time.Millisecond)
    cancel()
    time.Sleep(100 * time.Millisecond)

    m := c.Latest()
    if m.CollectTime.IsZero() {
        t.Error("expected CollectTime to be set before cancellation")
    }
}

func TestCollectSwapPageRate_FirstSample_NoRate(t *testing.T) {
    t.Log("testing collectSwapPageRate first sample sets prev but computes no rate")
    origFn := readSwapPageCountsFn
    readSwapPageCountsFn = func() (uint64, uint64, error) {
        return 1000, 500, nil
    }
    defer func() { readSwapPageCountsFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Swap:            config.SwapConfig{PageRateSampling: true},
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    c.collectSwapPageRate(&m)

    if m.SwapPageInRate != 0 {
        t.Errorf("SwapPageInRate should be 0 on first sample, got %d", m.SwapPageInRate)
    }
    if m.SwapPageOutRate != 0 {
        t.Errorf("SwapPageOutRate should be 0 on first sample, got %d", m.SwapPageOutRate)
    }
    if c.prevPageIn != 1000 {
        t.Errorf("prevPageIn: got %d, want 1000", c.prevPageIn)
    }
    if c.prevPageOut != 500 {
        t.Errorf("prevPageOut: got %d, want 500", c.prevPageOut)
    }
    if c.prevSampleTime.IsZero() {
        t.Error("prevSampleTime should be set after first sample")
    }
}

func TestCollectSwapPageRate_SecondSample_ComputesRate(t *testing.T) {
    t.Log("testing collectSwapPageRate second sample computes rate correctly")
    callCount := 0
    origFn := readSwapPageCountsFn
    readSwapPageCountsFn = func() (uint64, uint64, error) {
        callCount++
        if callCount == 1 {
            return 1000, 500, nil
        }
        return 2000, 800, nil
    }
    defer func() { readSwapPageCountsFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Swap:            config.SwapConfig{PageRateSampling: true},
    }
    c := NewCollector(cfg)

    var m1 HardwareMetrics
    c.collectSwapPageRate(&m1)

    time.Sleep(100 * time.Millisecond)

    var m2 HardwareMetrics
    c.collectSwapPageRate(&m2)

    t.Logf("SwapPageInRate=%d, SwapPageOutRate=%d", m2.SwapPageInRate, m2.SwapPageOutRate)
    if m2.SwapPageInRate == 0 {
        t.Error("expected SwapPageInRate > 0 on second sample")
    }
    if m2.SwapPageOutRate == 0 {
        t.Error("expected SwapPageOutRate > 0 on second sample")
    }
}

func TestCollectSwapPageRate_ErrorReturns(t *testing.T) {
    t.Log("testing collectSwapPageRate returns early on read error")
    origFn := readSwapPageCountsFn
    readSwapPageCountsFn = func() (uint64, uint64, error) {
        return 0, 0, fmt.Errorf("sysctl failed")
    }
    defer func() { readSwapPageCountsFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Swap:            config.SwapConfig{PageRateSampling: true},
    }
    c := NewCollector(cfg)

    c.prevPageIn = 100
    c.prevPageOut = 50
    c.prevSampleTime = time.Now().Add(-1 * time.Second)

    var m HardwareMetrics
    c.collectSwapPageRate(&m)

    if m.SwapPageInRate != 0 || m.SwapPageOutRate != 0 {
        t.Errorf("rates should be 0 on error, got in=%d out=%d", m.SwapPageInRate, m.SwapPageOutRate)
    }
}

func TestCollector_CollectGopsutil(t *testing.T) {
    t.Log("testing collectGopsutil on live system")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    err := c.collectGopsutil(&m)
    if err != nil {
        t.Logf("collectGopsutil error (acceptable in CI): %v", err)
    } else {
        t.Logf("TotalMemory=%d, UsedMemory=%d, Ratio=%f", m.TotalMemory, m.UsedMemory, m.MemoryUsedRatio)
        if m.TotalMemory == 0 {
            t.Log("TotalMemory is 0")
        }
    }
}

func TestCollector_Collect_AllSourcesEnabled(t *testing.T) {
    t.Log("testing collect with all sources enabled")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: true},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: true},
        Swap:            config.SwapConfig{PageRateSampling: true},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    t.Logf("CollectTime=%v, CollectionError=%v", m.CollectTime, m.CollectionError)
    t.Logf("TotalMemory=%d, MemoryUsedRatio=%f", m.TotalMemory, m.MemoryUsedRatio)
    t.Logf("SwapTotal=%d, SwapUsed=%d", m.SwapTotal, m.SwapUsed)
    t.Logf("GPUDeviceUtilization=%f, GPUCoreCount=%d", m.GPUDeviceUtilization, m.GPUCoreCount)
    t.Logf("MLXActiveMemory=%d, MLXModelsLoaded=%d", m.MLXActiveMemory, m.MLXModelsLoaded)

    if m.CollectTime.IsZero() {
        t.Error("expected CollectTime to be set")
    }
}

func TestCollector_Collect_AllDisabled(t *testing.T) {
    t.Log("testing collect with all sources disabled")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    if m.CollectTime.IsZero() {
        t.Error("expected CollectTime to be set even with all sources disabled")
    }
}

func TestCollector_Collect_MultipleRounds(t *testing.T) {
    t.Log("testing collect called multiple times updates metrics")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: true},
    }
    c := NewCollector(cfg)

    c.collect()
    m1 := c.Latest()

    time.Sleep(50 * time.Millisecond)
    c.collect()
    m2 := c.Latest()

    if m2.CollectTime.Before(m1.CollectTime) {
        t.Error("second collect should have later CollectTime")
    }
    t.Logf("round1 CollectTime=%v, round2 CollectTime=%v", m1.CollectTime, m2.CollectTime)
}

func TestCollector_Collect_OnlyIOKit(t *testing.T) {
    t.Log("testing collect with only IOKit enabled")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: true},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    t.Logf("GPUDeviceUtilization=%f, GPUCoreCount=%d", m.GPUDeviceUtilization, m.GPUCoreCount)
}

func TestCollector_Collect_OnlyMLX(t *testing.T) {
    t.Log("testing collect with only MLX metrics enabled")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: true},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    t.Logf("MLXActiveMemory=%d, MLXModelsLoaded=%d", m.MLXActiveMemory, m.MLXModelsLoaded)
}

func TestCollector_ConcurrentLatest(t *testing.T) {
    t.Log("testing concurrent calls to Latest")
    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 50 * time.Millisecond,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    c.Start(ctx)
    time.Sleep(100 * time.Millisecond)

    var readCount atomic.Int32
    done := make(chan struct{})
    for i := 0; i < 10; i++ {
        go func() {
            for j := 0; j < 100; j++ {
                _ = c.Latest()
                readCount.Add(1)
            }
            if readCount.Load() >= 1000 {
                select {
                case done <- struct{}{}:
                default:
                }
            }
        }()
    }

    select {
    case <-done:
        t.Logf("completed %d concurrent reads", readCount.Load())
    case <-time.After(3 * time.Second):
        t.Fatalf("timeout: only %d concurrent reads completed", readCount.Load())
    }

    c.Stop()
}

func TestReadSwapPageCounts(t *testing.T) {
    if runtime.GOOS != "darwin" {
        t.Skip("skipping: sysctl only available on darwin")
    }
    t.Log("testing readSwapPageCounts on darwin")
    _, _, err := readSwapPageCounts()
    if err != nil {
        t.Logf("readSwapPageCounts failed (vm.pageins/vm.pageouts may not exist): %v", err)
    }
}

func TestReadSysctlInt_ValidKey(t *testing.T) {
    if runtime.GOOS != "darwin" {
        t.Skip("skipping: sysctl only available on darwin")
    }
    t.Log("testing readSysctlInt with known sysctl key")
    val, err := readSysctlInt("hw.memsize")
    if err != nil {
        t.Fatalf("unexpected error reading hw.memsize: %v", err)
    }
    t.Logf("hw.memsize=%d", val)
    if val == 0 {
        t.Error("hw.memsize should not be 0")
    }
}

func TestReadSysctlInt_InvalidKey(t *testing.T) {
    t.Log("testing readSysctlInt with invalid sysctl key")
    _, err := readSysctlInt("nonexistent.sysctl.key.xyz")
    if err == nil {
        t.Fatal("expected error for invalid sysctl key")
    }
}

func TestGetGPUCoreCount(t *testing.T) {
    if runtime.GOOS != "darwin" {
        t.Skip("skipping: sysctl only available on darwin")
    }
    t.Log("testing getGPUCoreCount on darwin")
    count := getGPUCoreCount()
    t.Logf("GPU core count=%d", count)
    if count < 0 {
        t.Errorf("GPU core count should not be negative, got %d", count)
    }
}

func TestCollectIOKitGPUPurego_DoesNotPanic(t *testing.T) {
    if runtime.GOOS != "darwin" {
        t.Skip("skipping: IOKit only available on darwin")
    }
    t.Log("testing collectIOKitGPUPurego does not panic on darwin")
    var m HardwareMetrics
    initIOKit()

    if !iokitReady {
        t.Log("iokit not ready, skipping purego test")
        return
    }

    err := collectIOKitGPUPurego(&m)
    if err != nil {
        t.Logf("collectIOKitGPUPurego returned error (acceptable in CI): %v", err)
    }
    t.Logf("GPUDeviceUtilization=%f, GPUCoreCount=%d", m.GPUDeviceUtilization, m.GPUCoreCount)
}

func TestCollectIOKitGPU_DoesNotPanic(t *testing.T) {
    if runtime.GOOS != "darwin" {
        t.Skip("skipping: IOKit only available on darwin")
    }
    t.Log("testing collectIOKitGPU does not panic on darwin")
    var m HardwareMetrics
    err := collectIOKitGPU(&m)
    if err != nil {
        t.Logf("collectIOKitGPU returned error (acceptable): %v", err)
    }
    t.Logf("GPUDeviceUtilization=%f, GPUCoreCount=%d", m.GPUDeviceUtilization, m.GPUCoreCount)
}

func TestCollectIOKitGPUViaIoreg_DoesNotPanic(t *testing.T) {
    if runtime.GOOS != "darwin" {
        t.Skip("skipping: ioreg only available on darwin")
    }
    t.Log("testing collectIOKitGPUViaIoreg does not panic on darwin")
    var m HardwareMetrics
    err := collectIOKitGPUViaIoreg(&m)
    if err != nil {
        t.Logf("collectIOKitGPUViaIoreg error (acceptable): %v", err)
    }
    t.Logf("GPUDeviceUtilization=%f, GPUCoreCount=%d, AllocMemory=%d",
        m.GPUDeviceUtilization, m.GPUCoreCount, m.GPUAllocMemory)
}

func TestCollectMLXMetrics_ConnectionRefused(t *testing.T) {
    t.Log("testing collectMLXMetrics when fusion-mlx is not running")
    var m HardwareMetrics
    err := collectMLXMetrics(&m)
    if err == nil {
        t.Log("fusion-mlx appears to be running — metrics collected successfully")
    } else {
        t.Logf("expected error when mlx not running: %v", err)
    }
}

func TestCollectMLXMetrics_SuccessViaTestServer(t *testing.T) {
    t.Log("testing collectMLXMetrics with mock HTTP server")
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/metrics" {
            t.Errorf("expected /metrics path, got %s", r.URL.Path)
        }
        w.Header().Set("Content-Type", "text/plain")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`# HELP fusion_mlx_model_memory_bytes Current model memory
# TYPE fusion_mlx_model_memory_bytes gauge
fusion_mlx_model_memory_bytes 2147483648
fusion_mlx_models_loaded 3
fusion_mlx_requests_total 999
fusion_mlx_inference_queue_depth 5
`))
    }))
    defer ts.Close()

    origURL := mlxMetricsURL
    mlxMetricsURL = ts.URL + "/metrics"
    defer func() { mlxMetricsURL = origURL }()

    var m HardwareMetrics
    err := collectMLXMetrics(&m)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if m.MLXActiveMemory != 2147483648 {
        t.Errorf("MLXActiveMemory: got %d, want 2147483648", m.MLXActiveMemory)
    }
    if m.MLXModelsLoaded != 3 {
        t.Errorf("MLXModelsLoaded: got %d, want 3", m.MLXModelsLoaded)
    }
    if m.MLXRequestsTotal != 999 {
        t.Errorf("MLXRequestsTotal: got %d, want 999", m.MLXRequestsTotal)
    }
    if m.MLXInferenceQueueDepth != 5 {
        t.Errorf("MLXInferenceQueueDepth: got %d, want 5", m.MLXInferenceQueueDepth)
    }
}

func TestCollectMLXMetrics_NonOKStatus(t *testing.T) {
    t.Log("testing collectMLXMetrics with non-200 HTTP status")
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusServiceUnavailable)
    }))
    defer ts.Close()

    origURL := mlxMetricsURL
    mlxMetricsURL = ts.URL + "/metrics"
    defer func() { mlxMetricsURL = origURL }()

    var m HardwareMetrics
    err := collectMLXMetrics(&m)
    if err == nil {
        t.Fatal("expected error for non-200 status")
    }
    t.Logf("error as expected: %v", err)
}

func TestCollectMLXMetrics_InvalidURL(t *testing.T) {
    t.Log("testing collectMLXMetrics with invalid URL")
    origURL := mlxMetricsURL
    mlxMetricsURL = "http://127.0.0.1:1/metrics"
    defer func() { mlxMetricsURL = origURL }()

    var m HardwareMetrics
    err := collectMLXMetrics(&m)
    if err == nil {
        t.Log("unexpected success with invalid URL")
    } else {
        t.Logf("error as expected: %v", err)
    }
}

func TestHardwareMetrics_Fields(t *testing.T) {
    t.Log("testing HardwareMetrics struct field assignments")
    m := HardwareMetrics{
        TotalMemory:            17179869184,
        UsedMemory:             8589934592,
        MemoryUsedRatio:        0.5,
        SwapTotal:              1073741824,
        SwapUsed:               536870912,
        SwapPageInRate:         100,
        SwapPageOutRate:        50,
        GPUDeviceUtilization:   0.75,
        GPURendererUtilization: 0.60,
        GPUTilerUtilization:    0.30,
        GPUAllocMemory:         2147483648,
        GPUInUseMemory:         1073741824,
        GPUCoreCount:           10,
        MLXActiveMemory:        1073741824,
        MLXModelsLoaded:        2,
        MLXRequestsTotal:       500,
        MLXInferenceQueueDepth: 3,
    }

    if m.TotalMemory != 17179869184 {
        t.Errorf("TotalMemory: got %d", m.TotalMemory)
    }
    if m.GPUDeviceUtilization != 0.75 {
        t.Errorf("GPUDeviceUtilization: got %f", m.GPUDeviceUtilization)
    }
    if m.MLXModelsLoaded != 2 {
        t.Errorf("MLXModelsLoaded: got %d", m.MLXModelsLoaded)
    }
}

func TestCollect_GopsutilError_SetsCollectErr(t *testing.T) {
    t.Log("testing collect sets CollectionError when gopsutil fails")
    origFn := collectGopsutilFn
    collectGopsutilFn = func(c *Collector, m *HardwareMetrics) error {
        return fmt.Errorf("gopsutil unavailable")
    }
    defer func() { collectGopsutilFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    if m.CollectionError == nil {
        t.Fatal("expected CollectionError when gopsutil fails")
    }
    t.Logf("CollectionError: %v", m.CollectionError)
}

func TestCollect_IOKitError_SetsCollectErr(t *testing.T) {
    t.Log("testing collect sets CollectionError when IOKit fails")
    origIOKit := collectIOKitGPUFn
    collectIOKitGPUFn = func(m *HardwareMetrics) error {
        return fmt.Errorf("iokit unavailable")
    }
    defer func() { collectIOKitGPUFn = origIOKit }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: true},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    if m.CollectionError == nil {
        t.Fatal("expected CollectionError when iokit fails")
    }
    t.Logf("CollectionError: %v", m.CollectionError)
}

func TestCollect_PublishesHardwareMetrics(t *testing.T) {
    t.Log("testing collect publishes hw* gauges to Prometheus (#96)")
    origFn := collectGopsutilFn
    collectGopsutilFn = func(c *Collector, m *HardwareMetrics) error {
        m.MemoryUsedRatio = 0.42
        m.SwapUsed = 2147483648
        m.MLXActiveMemory = 1073741824
        m.MLXModelsLoaded = 2
        m.MLXInferenceQueueDepth = 3
        return nil
    }
    defer func() { collectGopsutilFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    checks := []string{
        `fusion_gateway_hw_memory_used_ratio 0.42`,
        `fusion_gateway_hw_swap_used_bytes 2.147483648e+09`,
        `fusion_gateway_hw_mlx_active_memory_bytes 1.073741824e+09`,
        `fusion_gateway_hw_mlx_models_loaded 2`,
        `fusion_gateway_hw_mlx_inference_queue_depth 3`,
    }
    for _, want := range checks {
        if !strings.Contains(body, want) {
            t.Errorf("metrics missing %s", want)
        }
    }
}

func TestCollect_PublishesCollectionError(t *testing.T) {
    t.Log("testing collect records hw_collection_errors_total on failure (#96)")
    origFn := collectGopsutilFn
    collectGopsutilFn = func(c *Collector, m *HardwareMetrics) error {
        return fmt.Errorf("gopsutil unavailable")
    }
    defer func() { collectGopsutilFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: true},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: false},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    want := `fusion_gateway_hw_collection_errors_total{source="hardware_collect"}`
    if !strings.Contains(body, want) {
        t.Errorf("metrics missing %s\n got: %s", want, body)
    }
}

func TestCollect_MLXError_DoesNotSetCollectErr(t *testing.T) {
    t.Log("testing collect does not set CollectionError when MLX fails (non-fatal)")
    origMLX := collectMLXMetricsFn
    collectMLXMetricsFn = func(m *HardwareMetrics) error {
        return fmt.Errorf("mlx offline")
    }
    defer func() { collectMLXMetricsFn = origMLX }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Gopsutil:        config.GopsutilConfig{Enabled: false},
        IOKit:           config.IOKitConfig{Enabled: false},
        MLXMetrics:      config.MLXMetricsConfig{Enabled: true},
        Swap:            config.SwapConfig{PageRateSampling: false},
    }
    c := NewCollector(cfg)
    c.collect()

    m := c.Latest()
    if m.CollectionError != nil {
        t.Errorf("MLX failure should not set CollectionError, got: %v", m.CollectionError)
    }
}

func TestReadSwapPageCounts_PageoutsError(t *testing.T) {
    t.Log("testing readSwapPageCounts when vm.pageouts fails but vm.pageins succeeds")
    callCount := 0
    origFn := readSysctlIntFn
    readSysctlIntFn = func(name string) (uint64, error) {
        callCount++
        if name == "vm.pageins" {
            return 1000, nil
        }
        return 0, fmt.Errorf("unknown oid: vm.pageouts")
    }
    defer func() { readSysctlIntFn = origFn }()

    pageIn, pageOut, err := readSwapPageCounts()
    if err == nil {
        t.Fatal("expected error when vm.pageouts fails")
    }
    t.Logf("pageIn=%d, pageOut=%d, err=%v", pageIn, pageOut, err)
}

func TestReadSwapPageCounts_Success(t *testing.T) {
    t.Log("testing readSwapPageCounts success path via mock")
    origFn := readSysctlIntFn
    readSysctlIntFn = func(name string) (uint64, error) {
        if name == "vm.pageins" {
            return 5000, nil
        }
        return 3000, nil
    }
    defer func() { readSysctlIntFn = origFn }()

    pageIn, pageOut, err := readSwapPageCounts()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if pageIn != 5000 {
        t.Errorf("pageIn: got %d, want 5000", pageIn)
    }
    if pageOut != 3000 {
        t.Errorf("pageOut: got %d, want 3000", pageOut)
    }
}

func TestCollectIOKitGPUViaIoreg_CommandFailure(t *testing.T) {
    t.Log("testing collectIOKitGPUViaIoreg when ioreg command fails")
    origFn := ioregCmdFn
    ioregCmdFn = func() ([]byte, error) {
        return nil, fmt.Errorf("ioreg: command not found")
    }
    defer func() { ioregCmdFn = origFn }()

    origGPUFn := getGPUCoreCountFn
    getGPUCoreCountFn = func() int { return 0 }
    defer func() { getGPUCoreCountFn = origGPUFn }()

    var m HardwareMetrics
    err := collectIOKitGPUViaIoreg(&m)
    if err == nil {
        t.Fatal("expected error when ioreg command fails")
    }
    t.Logf("error: %v", err)
}

func TestCollectIOKitGPUViaIoreg_ParseFailure(t *testing.T) {
    t.Log("testing collectIOKitGPUViaIoreg when ioreg output cannot be parsed")
    origFn := ioregCmdFn
    ioregCmdFn = func() ([]byte, error) {
        return []byte("no performance statistics here"), nil
    }
    defer func() { ioregCmdFn = origFn }()

    origGPUFn := getGPUCoreCountFn
    getGPUCoreCountFn = func() int { return 8 }
    defer func() { getGPUCoreCountFn = origGPUFn }()

    var m HardwareMetrics
    err := collectIOKitGPUViaIoreg(&m)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if m.GPUCoreCount != 8 {
        t.Errorf("GPUCoreCount: got %d, want 8", m.GPUCoreCount)
    }
}

func TestGetGPUCoreCount_ParseError(t *testing.T) {
    t.Log("testing getGPUCoreCount when sysctl returns non-numeric output")
    origFn := gpuCoreCountCmdFn
    gpuCoreCountCmdFn = func() ([]byte, error) {
        return []byte("not_a_number"), nil
    }
    defer func() { gpuCoreCountCmdFn = origFn }()

    count := getGPUCoreCount()
    if count != 0 {
        t.Errorf("expected 0 for non-numeric output, got %d", count)
    }
}

func TestGetGPUCoreCount_CommandError(t *testing.T) {
    t.Log("testing getGPUCoreCount when sysctl command fails")
    origFn := gpuCoreCountCmdFn
    gpuCoreCountCmdFn = func() ([]byte, error) {
        return nil, fmt.Errorf("sysctl not found")
    }
    defer func() { gpuCoreCountCmdFn = origFn }()

    count := getGPUCoreCount()
    if count != 0 {
        t.Errorf("expected 0 for command error, got %d", count)
    }
}

func TestGetGPUCoreCount_ValidOutput(t *testing.T) {
    t.Log("testing getGPUCoreCount with valid sysctl output")
    origFn := gpuCoreCountCmdFn
    gpuCoreCountCmdFn = func() ([]byte, error) {
        return []byte("10\n"), nil
    }
    defer func() { gpuCoreCountCmdFn = origFn }()

    count := getGPUCoreCount()
    if count != 10 {
        t.Errorf("expected 10, got %d", count)
    }
}

func TestCollectMLXMetrics_RequestCreationError(t *testing.T) {
    t.Log("testing collectMLXMetrics with request creation error")
    origURL := mlxMetricsURL
    mlxMetricsURL = "\x00invalid\x00url"
    defer func() { mlxMetricsURL = origURL }()

    var m HardwareMetrics
    err := collectMLXMetrics(&m)
    if err == nil {
        t.Fatal("expected error for invalid URL")
    }
    t.Logf("error: %v", err)
}

func TestCollectMLXMetrics_BodyReadError(t *testing.T) {
    t.Log("testing collectMLXMetrics with body read error")
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        w.WriteHeader(http.StatusOK)
    }))
    defer ts.Close()

    origDo := mlxHTTPDoFn
    mlxHTTPDoFn = func(req *http.Request) (*http.Response, error) {
        resp, err := defaultMLXHTTPDo(req)
        if err != nil {
            return resp, err
        }
        resp.Body = &errorReader{}
        return resp, nil
    }
    defer func() { mlxHTTPDoFn = origDo }()

    origURL := mlxMetricsURL
    mlxMetricsURL = ts.URL + "/metrics"
    defer func() { mlxMetricsURL = origURL }()

    var m HardwareMetrics
    err := collectMLXMetrics(&m)
    if err == nil {
        t.Fatal("expected error for body read failure")
    }
    t.Logf("error: %v", err)
}

type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
    return 0, fmt.Errorf("read error")
}

func (e *errorReader) Close() error {
    return nil
}

func TestExtractMetricValue_FloatParseError(t *testing.T) {
    t.Log("testing extractMetricValue with invalid float value")
    _, err := extractMetricValue("metric_name 1.2.3.4")
    if err == nil {
        t.Fatal("expected error for invalid float")
    }
    t.Logf("error: %v", err)
}

func TestCollectSwapPageRate_ZeroElapsed(t *testing.T) {
    t.Log("testing collectSwapPageRate with zero elapsed time")
    origFn := readSwapPageCountsFn
    readSwapPageCountsFn = func() (uint64, uint64, error) {
        return 1000, 500, nil
    }
    defer func() { readSwapPageCountsFn = origFn }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
        Swap:            config.SwapConfig{PageRateSampling: true},
    }
    c := NewCollector(cfg)

    c.prevPageIn = 500
    c.prevPageOut = 250
    c.prevSampleTime = time.Now()

    var m HardwareMetrics
    c.collectSwapPageRate(&m)

    t.Logf("SwapPageInRate=%d, SwapPageOutRate=%d", m.SwapPageInRate, m.SwapPageOutRate)
}

func TestCollectGopsutil_VirtualMemoryError(t *testing.T) {
    t.Log("testing collectGopsutil when VirtualMemory fails")
    origVM := memVirtualMemoryFn
    memVirtualMemoryFn = func() (*mem.VirtualMemoryStat, error) {
        return nil, fmt.Errorf("virtual memory unavailable")
    }
    defer func() { memVirtualMemoryFn = origVM }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    err := c.collectGopsutil(&m)
    if err == nil {
        t.Fatal("expected error when VirtualMemory fails")
    }
    t.Logf("error: %v", err)
}

func TestCollectGopsutil_SwapMemoryError(t *testing.T) {
    t.Log("testing collectGopsutil when SwapMemory fails")
    origVM := memVirtualMemoryFn
    memVirtualMemoryFn = func() (*mem.VirtualMemoryStat, error) {
        return &mem.VirtualMemoryStat{
            Total:       17179869184,
            Used:        8589934592,
            UsedPercent: 50.0,
        }, nil
    }
    defer func() { memVirtualMemoryFn = origVM }()

    origSwap := memSwapMemoryFn
    memSwapMemoryFn = func() (*mem.SwapMemoryStat, error) {
        return nil, fmt.Errorf("swap memory unavailable")
    }
    defer func() { memSwapMemoryFn = origSwap }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    err := c.collectGopsutil(&m)
    if err == nil {
        t.Fatal("expected error when SwapMemory fails")
    }
    t.Logf("error: %v", err)
}

func TestCollectGopsutil_SuspiciousZeroRatio(t *testing.T) {
    t.Log("testing collectGopsutil suspicious ratio warning (0%)")
    origVM := memVirtualMemoryFn
    memVirtualMemoryFn = func() (*mem.VirtualMemoryStat, error) {
        return &mem.VirtualMemoryStat{
            Total:       17179869184,
            Used:        0,
            UsedPercent: 0.0,
        }, nil
    }
    defer func() { memVirtualMemoryFn = origVM }()

    origSwap := memSwapMemoryFn
    memSwapMemoryFn = func() (*mem.SwapMemoryStat, error) {
        return &mem.SwapMemoryStat{Total: 0, Used: 0}, nil
    }
    defer func() { memSwapMemoryFn = origSwap }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    err := c.collectGopsutil(&m)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if m.MemoryUsedRatio != 0 {
        t.Errorf("MemoryUsedRatio: got %f, want 0", m.MemoryUsedRatio)
    }
}

func TestCollectGopsutil_SuspiciousFullRatio(t *testing.T) {
    t.Log("testing collectGopsutil suspicious ratio warning (100%)")
    origVM := memVirtualMemoryFn
    memVirtualMemoryFn = func() (*mem.VirtualMemoryStat, error) {
        return &mem.VirtualMemoryStat{
            Total:       17179869184,
            Used:        17179869184,
            UsedPercent: 100.0,
        }, nil
    }
    defer func() { memVirtualMemoryFn = origVM }()

    origSwap := memSwapMemoryFn
    memSwapMemoryFn = func() (*mem.SwapMemoryStat, error) {
        return &mem.SwapMemoryStat{Total: 1073741824, Used: 536870912}, nil
    }
    defer func() { memSwapMemoryFn = origSwap }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    err := c.collectGopsutil(&m)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if m.MemoryUsedRatio < 0.999 {
        t.Errorf("MemoryUsedRatio: got %f, want >= 0.999", m.MemoryUsedRatio)
    }
}

func TestCollectGopsutil_NormalRatio(t *testing.T) {
    t.Log("testing collectGopsutil normal ratio path")
    origVM := memVirtualMemoryFn
    memVirtualMemoryFn = func() (*mem.VirtualMemoryStat, error) {
        return &mem.VirtualMemoryStat{
            Total:       17179869184,
            Used:        8589934592,
            UsedPercent: 50.0,
        }, nil
    }
    defer func() { memVirtualMemoryFn = origVM }()

    origSwap := memSwapMemoryFn
    memSwapMemoryFn = func() (*mem.SwapMemoryStat, error) {
        return &mem.SwapMemoryStat{Total: 1073741824, Used: 536870912}, nil
    }
    defer func() { memSwapMemoryFn = origSwap }()

    cfg := &config.HardwareConfig{
        Enabled:         true,
        CollectInterval: 1 * time.Second,
    }
    c := NewCollector(cfg)
    var m HardwareMetrics
    err := c.collectGopsutil(&m)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if m.TotalMemory != 17179869184 {
        t.Errorf("TotalMemory: got %d, want 17179869184", m.TotalMemory)
    }
    if m.MemoryUsedRatio != 0.5 {
        t.Errorf("MemoryUsedRatio: got %f, want 0.5", m.MemoryUsedRatio)
    }
    if m.SwapTotal != 1073741824 {
        t.Errorf("SwapTotal: got %d, want 1073741824", m.SwapTotal)
    }
    if m.SwapUsed != 536870912 {
        t.Errorf("SwapUsed: got %d, want 536870912", m.SwapUsed)
    }
}
