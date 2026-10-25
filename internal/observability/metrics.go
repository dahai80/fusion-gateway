package observability

import (
    "net/http"
    "sync/atomic"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
    registry *prometheus.Registry

    requestsTotal *prometheus.CounterVec
    requestDuration *prometheus.HistogramVec
    tokensTotal *prometheus.CounterVec
    routeDecisions *prometheus.CounterVec
    circuitBreakerState *prometheus.GaugeVec
    circuitBreakerTrips *prometheus.CounterVec
    hwMemoryUsedRatio prometheus.Gauge
    hwSwapUsedBytes prometheus.Gauge
    hwSwapPageInRate prometheus.Gauge
    hwSwapPageOutRate prometheus.Gauge
    hwGPUDeviceUtilization prometheus.Gauge
    hwGPURendererUtilization prometheus.Gauge
    hwGPUTilerUtilization prometheus.Gauge
    hwGPUMemoryInUseBytes prometheus.Gauge
    hwGPUMemoryAllocBytes prometheus.Gauge
    hwMLXActiveMemoryBytes prometheus.Gauge
    hwMLXModelsLoaded prometheus.Gauge
    hwMLXInferenceQueueDepth prometheus.Gauge
    hwCollectionErrors *prometheus.CounterVec
    configVersion prometheus.Gauge
    inFlightRequests *prometheus.GaugeVec

    // #204: classifier latency SLO. The <15ms SLO is asserted in mock tests
    // but was only logged at runtime — no metric to alert on. Expose a
    // histogram (full distribution) + a breach counter (alert trigger).
    classifierLatency *prometheus.HistogramVec
    classifierSLOBreaches *prometheus.CounterVec

    // #281: shell heartbeat denials. The WARN log line is throttled per
    // (id, reason); this counter records EVERY denial so a permanently
    // misconfigured module stays observable in /metrics.
    shellHeartbeatDenied *prometheus.CounterVec

    // #282: invalid-key rejections by hash prefix. A client stuck on a
    // rotated/stale key shows up as a recurring label value — spot it in
    // /metrics without log grepping.
    authInvalidKey *prometheus.CounterVec

    // #283: OTel internal errors (mostly failed export batches when no
    // collector listens). The log line is throttled; this counter is not.
    otelErrors prometheus.Counter

    // Plan §5.2: TTFT/TPOT latency histograms (see init for bucket rationale).
    requestTTFT *prometheus.HistogramVec
    requestTPOT *prometheus.HistogramVec

    localRequests  atomic.Int64
    cloudRequests  atomic.Int64
    totalRequests  atomic.Int64
    localSuccesses  atomic.Int64
    localFailures   atomic.Int64
    cloudSuccesses  atomic.Int64
    cloudFailures  atomic.Int64

    cloudInFlight  atomic.Int64
)

func init() {
    registry = prometheus.NewRegistry()

    requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_requests_total",
        Help: "Total requests processed",
    }, []string{"backend", "model", "status"})

    requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "fusion_gateway_request_duration_seconds",
        Help:    "Request duration in seconds",
        Buckets: prometheus.DefBuckets,
    }, []string{"backend", "model"})

    tokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_request_tokens_total",
        Help: "Total tokens processed",
    }, []string{"direction", "backend"})

    routeDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_route_decisions_total",
        Help: "Route decision counts",
    }, []string{"backend", "reason"})

    circuitBreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "fusion_gateway_circuit_breaker_state",
        Help: "Circuit breaker state (0=closed, 1=open, 2=half_open)",
    }, []string{"backend"})

    circuitBreakerTrips = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_circuit_breaker_trips_total",
        Help: "Circuit breaker trip counts",
    }, []string{"backend", "reason"})

    hwMemoryUsedRatio = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_memory_used_ratio",
        Help: "System memory usage ratio",
    })

    hwSwapUsedBytes = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_swap_used_bytes",
        Help: "Swap used bytes",
    })

    hwSwapPageInRate = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_swap_page_in_rate",
        Help: "Swap page-in rate (pages/s)",
    })

    hwSwapPageOutRate = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_swap_page_out_rate",
        Help: "Swap page-out rate (pages/s)",
    })

    hwGPUDeviceUtilization = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_gpu_device_utilization",
        Help: "GPU Device utilization ratio (IOKit AGXAccelerator)",
    })

    hwGPURendererUtilization = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_gpu_renderer_utilization",
        Help: "GPU Renderer utilization ratio (IOKit AGXAccelerator)",
    })

    hwGPUTilerUtilization = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_gpu_tiler_utilization",
        Help: "GPU Tiler utilization ratio (IOKit AGXAccelerator)",
    })

    hwGPUMemoryInUseBytes = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_gpu_memory_in_use_bytes",
        Help: "GPU in-use memory bytes (IOKit AGXAccelerator)",
    })

    hwGPUMemoryAllocBytes = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_gpu_memory_alloc_bytes",
        Help: "GPU allocated memory bytes (IOKit AGXAccelerator)",
    })

    hwMLXActiveMemoryBytes = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_mlx_active_memory_bytes",
        Help: "MLX active memory bytes (fusion-mlx /metrics)",
    })

    hwMLXModelsLoaded = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_mlx_models_loaded",
        Help: "MLX models loaded count (fusion-mlx /metrics)",
    })

    hwMLXInferenceQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_hw_mlx_inference_queue_depth",
        Help: "MLX inference queue depth (fusion-mlx /metrics)",
    })

    hwCollectionErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_hw_collection_errors_total",
        Help: "Hardware metrics collection errors",
    }, []string{"source"})

    configVersion = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_config_version",
        Help: "Current config version number",
    })

    inFlightRequests = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "fusion_gateway_in_flight_requests",
        Help: "In-flight request count",
    }, []string{"backend"})

    // Plan §5.2 (archi doc fusion-gateway-seriazation-1015.md): TTFT/TPOT
    // histograms mirror the RequestLog columns into Prometheus so /metrics
    // gives an immediate latency read while the PG-backed reports carry the
    // long-term trend. ttft covers 50ms..4min; tpot covers 5ms..2.5s per
    // output token.
    requestTTFT = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "fusion_gateway_request_ttft_seconds",
        Help:    "Time to first token (stream: first content chunk; non-stream: full response), from request start",
        Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 16, 30, 60, 120, 240},
    }, []string{"backend", "model"})
    requestTPOT = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "fusion_gateway_request_tpot_seconds",
        Help:    "Mean time per output token (stream inter-chunk gap mean); absent for non-stream",
        Buckets: []float64{0.005, 0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56},
    }, []string{"backend", "model"})

    // #204: classifier latency SLO metrics.
    classifierLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "fusion_gateway_classifier_latency_seconds",
        Help:    "Intent classifier round-trip latency (classifyAndLog)",
        Buckets: []float64{0.005, 0.01, 0.015, 0.025, 0.05, 0.1, 0.25, 0.5},
    }, []string{"source"})
    classifierSLOBreaches = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_classifier_slo_breach_total",
        Help: "Intent classifier SLO breaches (latency > 15ms threshold)",
    }, []string{"source"})

    shellHeartbeatDenied = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_shell_heartbeat_denied_total",
        Help: "Shell heartbeat denials (log line throttled; counter is not)",
    }, []string{"module_id", "reason"})

    authInvalidKey = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "fusion_gateway_auth_invalid_key_total",
        Help: "Invalid API key rejections by key hash prefix (8 hex chars, non-secret)",
    }, []string{"hash_prefix"})

    otelErrors = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "fusion_gateway_otel_errors_total",
        Help: "OTel internal errors (export failures etc.; log throttled, counter not)",
    })

    // EI6: tokenizer_calibration_deviation removed — it was declared but never
    // had a write path, so /metrics exposed a permanent 0 that looked like a
    // real signal. Do NOT redeclare until the tokenizer-deviation pipeline
    // (AH2) actually computes and reports a value; a dead gauge is worse than
    // no gauge (audit EI6 "禁止声明了就当存在").

    registry.MustRegister(
        requestsTotal, requestDuration, tokensTotal,
        routeDecisions, circuitBreakerState, circuitBreakerTrips,
        hwMemoryUsedRatio, hwSwapUsedBytes, hwSwapPageInRate, hwSwapPageOutRate,
        hwGPUDeviceUtilization, hwGPURendererUtilization, hwGPUTilerUtilization,
        hwGPUMemoryInUseBytes, hwGPUMemoryAllocBytes,
        hwMLXActiveMemoryBytes, hwMLXModelsLoaded, hwMLXInferenceQueueDepth,
        hwCollectionErrors, configVersion, inFlightRequests,
        requestTTFT, requestTPOT,
        classifierLatency, classifierSLOBreaches,
        shellHeartbeatDenied,
        authInvalidKey,
        otelErrors,
    )
}

func Handler() http.Handler {
    return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func RecordRequest(backend, model, status string) {
    requestsTotal.WithLabelValues(backend, model, status).Inc()
    totalRequests.Add(1)
    if backend == "local" {
        localRequests.Add(1)
        if status == "success" {
            localSuccesses.Add(1)
        } else if status == "error" {
            localFailures.Add(1)
        }
    } else {
        cloudRequests.Add(1)
        if status == "success" {
            cloudSuccesses.Add(1)
        } else if status == "error" {
            cloudFailures.Add(1)
        }
    }
}

func RecordDuration(backend, model string, seconds float64) {
    requestDuration.WithLabelValues(backend, model).Observe(seconds)
}

// RecordLatencyMetrics observes the TTFT/TPOT histograms (plan §5.2). tpot
// is 0 for non-stream requests and is skipped — an absent observation beats
// a misleading 0 in the histogram.
func RecordLatencyMetrics(backend, model string, ttftSec, tpotSec float64) {
    if ttftSec > 0 {
        requestTTFT.WithLabelValues(backend, model).Observe(ttftSec)
    }
    if tpotSec > 0 {
        requestTPOT.WithLabelValues(backend, model).Observe(tpotSec)
    }
}

func RecordTokens(direction, backend string, count int) {
    tokensTotal.WithLabelValues(direction, backend).Add(float64(count))
}

func RecordRouteDecision(backend, reason string) {
    routeDecisions.WithLabelValues(backend, reason).Inc()
}

func RecordCircuitBreakerTrip(backend, reason string) {
    circuitBreakerTrips.WithLabelValues(backend, reason).Inc()
}

// RecordShellHeartbeatDenied counts every heartbeat denial (#281). The WARN
// log is throttled per (id, reason) but this counter is not — a permanently
// misconfigured module stays visible in /metrics.
func RecordShellHeartbeatDenied(moduleID, reason string) {
    shellHeartbeatDenied.WithLabelValues(moduleID, reason).Inc()
}

// RecordAuthInvalidKey counts an invalid-key rejection by hash prefix (#282).
// The prefix (8 hex chars) identifies the key family, never the key itself —
// a recurring label value pinpoints a client stuck on a rotated key.
func RecordAuthInvalidKey(hashPrefix string) {
    authInvalidKey.WithLabelValues(hashPrefix).Inc()
}

// RecordOtelError counts an OTel internal error (mostly failed export
// batches when no collector listens, #283). The log line is throttled to
// 1 per 5m per distinct error; this counter is not throttled.
func RecordOtelError() {
    otelErrors.Inc()
}

func UpdateCircuitBreakerState(backend string, state int) {
    circuitBreakerState.WithLabelValues(backend).Set(float64(state))
}

func UpdateHardwareMetrics(
    memRatio float64,
    swapUsed, swapPageInRate, swapPageOutRate uint64,
    gpuDeviceUtil, gpuRendererUtil, gpuTilerUtil float64,
    gpuInUseMem, gpuAllocMem uint64,
    mlxActiveMem uint64,
    mlxModelsLoaded, mlxQueueDepth int,
) {
    hwMemoryUsedRatio.Set(memRatio)
    hwSwapUsedBytes.Set(float64(swapUsed))
    hwSwapPageInRate.Set(float64(swapPageInRate))
    hwSwapPageOutRate.Set(float64(swapPageOutRate))
    hwGPUDeviceUtilization.Set(gpuDeviceUtil)
    hwGPURendererUtilization.Set(gpuRendererUtil)
    hwGPUTilerUtilization.Set(gpuTilerUtil)
    hwGPUMemoryInUseBytes.Set(float64(gpuInUseMem))
    hwGPUMemoryAllocBytes.Set(float64(gpuAllocMem))
    hwMLXActiveMemoryBytes.Set(float64(mlxActiveMem))
    hwMLXModelsLoaded.Set(float64(mlxModelsLoaded))
    hwMLXInferenceQueueDepth.Set(float64(mlxQueueDepth))
}

func RecordCollectionError(source string) {
    hwCollectionErrors.WithLabelValues(source).Inc()
}

func UpdateConfigVersion(ver uint64) {
    configVersion.Set(float64(ver))
}

func UpdateInFlight(backend string, count int64) {
    inFlightRequests.WithLabelValues(backend).Set(float64(count))
}

// RecordClassifierLatency records the intent classifier round-trip latency
// (#204). source is the classifier tag ("laya", "laya_fallback",
// "laya_trivial", "llm", "router_light").
func RecordClassifierLatency(source string, seconds float64) {
    classifierLatency.WithLabelValues(source).Observe(seconds)
}

// RecordClassifierSLOBreach increments the SLO breach counter when the
// classifier latency exceeds the 15ms threshold (#204).
func RecordClassifierSLOBreach(source string) {
    classifierSLOBreaches.WithLabelValues(source).Inc()
}

// ClassifierSLOBreachValue returns the current SLO breach count for a source.
// Test helper — lets cross-package tests verify classifyAndLog recorded a
// breach without scraping the /metrics HTTP endpoint.
func ClassifierSLOBreachValue(source string) float64 {
    mfs, err := registry.Gather()
    if err != nil {
        return 0
    }
    for _, mf := range mfs {
        if mf.GetName() != "fusion_gateway_classifier_slo_breach_total" {
            continue
        }
        for _, m := range mf.GetMetric() {
            var match bool
            for _, l := range m.GetLabel() {
                if l.GetName() == "source" && l.GetValue() == source {
                    match = true
                    break
                }
            }
            if match {
                return m.GetCounter().GetValue()
            }
        }
    }
    return 0
}

func IncrCloudInFlight() {
    cur := cloudInFlight.Add(1)
    inFlightRequests.WithLabelValues("cloud").Set(float64(cur))
}

func DecrCloudInFlight() {
    cur := cloudInFlight.Add(-1)
    if cur < 0 {
        cur = 0
        cloudInFlight.Store(0)
    }
    inFlightRequests.WithLabelValues("cloud").Set(float64(cur))
}

func CloudInFlight() int64 {
    return cloudInFlight.Load()
}

func Stats() (total, local, cloud int64) {
    return totalRequests.Load(), localRequests.Load(), cloudRequests.Load()
}

func SuccessRate(backend string) float64 {
    if backend == "local" {
        s, f := localSuccesses.Load(), localFailures.Load()
        total := s + f
        if total == 0 {
            return 1.0
        }
        return float64(s) / float64(total)
    }
    s, f := cloudSuccesses.Load(), cloudFailures.Load()
    total := s + f
    if total == 0 {
        return 1.0
    }
    return float64(s) / float64(total)
}
