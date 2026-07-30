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
    tokenizerCalibrationDeviation prometheus.Gauge

    localRequests  atomic.Int64
    cloudRequests  atomic.Int64
    totalRequests  atomic.Int64
    localSuccesses  atomic.Int64
    localFailures   atomic.Int64
    cloudSuccesses  atomic.Int64
    cloudFailures   atomic.Int64
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

    tokenizerCalibrationDeviation = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "fusion_gateway_tokenizer_calibration_deviation",
        Help: "Tokenizer calibration deviation ratio",
    })

    registry.MustRegister(
        requestsTotal, requestDuration, tokensTotal,
        routeDecisions, circuitBreakerState, circuitBreakerTrips,
        hwMemoryUsedRatio, hwSwapUsedBytes, hwSwapPageInRate, hwSwapPageOutRate,
        hwGPUDeviceUtilization, hwGPURendererUtilization, hwGPUTilerUtilization,
        hwGPUMemoryInUseBytes, hwGPUMemoryAllocBytes,
        hwMLXActiveMemoryBytes, hwMLXModelsLoaded, hwMLXInferenceQueueDepth,
        hwCollectionErrors, configVersion, inFlightRequests,
        tokenizerCalibrationDeviation,
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

func RecordTokens(direction, backend string, count int) {
    tokensTotal.WithLabelValues(direction, backend).Add(float64(count))
}

func RecordRouteDecision(backend, reason string) {
    routeDecisions.WithLabelValues(backend, reason).Inc()
}

func RecordCircuitBreakerTrip(backend, reason string) {
    circuitBreakerTrips.WithLabelValues(backend, reason).Inc()
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
