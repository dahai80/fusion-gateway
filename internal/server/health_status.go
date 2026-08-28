package server

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    ctx := adapter.WithFusionHeaders(r.Context(), r)
    results := s.pool.HealthCheckAll(ctx)
    allHealthy := true
    for _, err := range results {
        if err != nil {
            allHealthy = false
            break
        }
    }

    backends := make(map[string]interface{}, len(results))
    for name, err := range results {
        if err != nil {
            backends[name] = map[string]interface{}{"healthy": false, "error": err.Error()}
        } else {
            backends[name] = map[string]interface{}{"healthy": true}
        }
    }

    // Fusion-mlx gets an authoritative model_loaded block from its /health
    // endpoint. A live process with no model loaded is process-healthy but
    // not servable — surface that explicitly so /health is no longer a false
    // green (#59).
    if mlxName, mlx := s.pool.FusionMLXName(); mlx != nil {
        detail := mlx.HealthDetail(ctx)
        entry := map[string]interface{}{
            "healthy":       detail.ProcessAlive,
            "model_loaded":  detail.ModelLoaded,
            "loaded_models": detail.LoadedModels,
        }
        if detail.FetchError != nil {
            entry["error"] = detail.FetchError.Error()
        }
        if backends[mlxName] == nil {
            backends[mlxName] = entry
        } else {
            // merge into the HealthCheckAll result (preserve healthy flag semantics)
            backends[mlxName] = entry
        }
        if !detail.ModelLoaded {
            allHealthy = false
            slog.Warn("fusion-mlx health: process alive but no model loaded", "loaded_models", detail.LoadedModels)
        }
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "status":   healthStatus(allHealthy),
        "backends": backends,
    })
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, "ok")
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
    ctx := adapter.WithFusionHeaders(r.Context(), r)
    localReady := s.router.CircuitBreakerState("local") != router.StateOpen
    localReasons := []string{}

    if localReady {
        if p, ok := s.pool.Get("fusion-mlx"); ok {
            if err := p.HealthCheck(ctx); err != nil {
                localReady = false
                localReasons = append(localReasons, "health_check_failed")
            }
        } else {
            localReady = false
            localReasons = append(localReasons, "no_local_provider")
        }
    }

    // Model-loaded probe: a live fusion-mlx with no resident model cannot
    // serve generate requests (502), so /readyz must report not_ready even
    // when the process responds 200 to /health (#59).
    if localReady {
        if mlx := s.pool.GetFusionMLX(); mlx != nil {
            detail := mlx.HealthDetail(ctx)
            if detail.FetchError != nil || !detail.ProcessAlive {
                localReady = false
                localReasons = append(localReasons, "health_check_failed")
            } else if !detail.ModelLoaded {
                localReady = false
                localReasons = append(localReasons, "model_not_loaded")
                slog.Warn("readyz: fusion-mlx process healthy but no model loaded")
            }
        }
    } else {
        localReasons = append(localReasons, "circuit_breaker_open")
    }

    // GPU memory availability check
    if localReady {
        hwMetrics := s.hwCollector.Latest()
        if hwMetrics.GPUAllocMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
            gpuAvail := hwMetrics.GPUAllocMemory - hwMetrics.GPUInUseMemory
            if gpuAvail < uint64(float64(hwMetrics.GPUAllocMemory)*0.1) {
                localReady = false
                localReasons = append(localReasons, "gpu_memory_critical")
            }
        }
        // Inference queue depth check
        if hwMetrics.MLXInferenceQueueDepth > 0 {
            maxQueue := s.cfg.Config.Routing.LocalPriority.MaxConcurrent
            if maxQueue > 0 && hwMetrics.MLXInferenceQueueDepth >= maxQueue {
                localReady = false
                localReasons = append(localReasons, "inference_queue_full")
            }
        }
        // Recent success rate check
        successRate := observability.SuccessRate("local")
        if successRate < 0.5 {
            localReady = false
            localReasons = append(localReasons, fmt.Sprintf("low_success_rate=%.2f", successRate))
        }
    }

    cloudReady := true
    if s.cfg.Config.Routing.Fallback.CloudDefault != "" {
        if p, ok := s.pool.Get(s.cfg.Config.Routing.Fallback.CloudDefault); ok {
            if err := p.HealthCheck(ctx); err != nil {
                cloudReady = false
            }
        }
    }

    if !localReady && !cloudReady {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusServiceUnavailable)
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "status":        "not_ready",
            "local_reasons": localReasons,
        })
        return
    }

    mode := "full"
    if !localReady {
        mode = "degraded"
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "status":        "ready",
        "mode":          mode,
        "local_reasons": localReasons,
    })
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, "alive")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
    hwMetrics := s.hwCollector.Latest()
    total, local, cloud := observability.Stats()

    localHitRate := 0.0
    if total > 0 {
        localHitRate = float64(local) / float64(total)
    }

    status := map[string]interface{}{
        "status":         "ok",
        "uptime_seconds": int(time.Since(s.startTime).Seconds()),
        "config_version": s.cfg.Version,
        // R4 (audit): surface the running binary version/commit (build-time
        // ldflags), not just config_version, so operators can confirm the
        // deployed artifact. Defaults to "dev"/"unknown" on a plain go build.
        "version":        s.version,
        "commit":         s.commit,
        "backends":       s.buildBackendStatus(r.Context()),
        "hardware":       s.buildHardwareStatus(hwMetrics),
        "circuit_breakers": map[string]string{
            "local":  s.router.CircuitBreakerState("local").String(),
            "cloud":  s.router.CircuitBreakerState("cloud").String(),
            "cluster": s.router.CircuitBreakerState("cluster").String(),
        },
        "stats": map[string]interface{}{
            "total_requests": total,
            "local_requests": local,
            "cloud_requests": cloud,
            "local_hit_rate": localHitRate,
        },
    }

    if s.clusterDiscovery != nil {
        status["cluster"] = s.clusterDiscovery.Status()
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleAdminGC(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    provider, ok := s.pool.Get("fusion-mlx")
    if !ok {
        http.Error(w, `{"error":{"message":"fusion-mlx not configured"}}`, http.StatusNotFound)
        return
    }

    mlxProvider, ok := provider.(*adapter.FusionMLXProvider)
    if !ok {
        http.Error(w, `{"error":{"message":"provider is not fusion-mlx"}}`, http.StatusInternalServerError)
        return
    }

    if mlxProvider.InFlight() != 0 {
        safego.Go("mlx_trigger_gc_when_idle", mlxProvider.TriggerGCWhenIdle)
        w.WriteHeader(http.StatusAccepted)
        fmt.Fprintf(w, `{"status":"gc_queued","in_flight":%d,"message":"GC will execute when in-flight requests reach zero"}`, mlxProvider.InFlight())
        return
    }

    safego.Go("mlx_safe_gc", mlxProvider.SafeGC)
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, `{"status":"gc_triggered"}`)
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    snap, err := config.Reload(s.cfgPath)
    if err != nil {
        slog.Error("admin config reload failed", "path", s.cfgPath, "error", err)
        http.Error(w, fmt.Sprintf(`{"error":{"message":"config reload failed: %s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
        return
    }
    slog.Info("admin config reload succeeded", "path", s.cfgPath, "version", snap.Version)
    writeJSON(w, http.StatusOK, map[string]any{
        "status":  "reloaded",
        "version": snap.Version,
        "path":    s.cfgPath,
    })
}

func (s *Server) buildBackendStatus(ctx context.Context) map[string]interface{} {
    results := s.pool.HealthCheckAll(ctx)
    status := make(map[string]interface{})

    for name, err := range results {
        entry := map[string]interface{}{
            "healthy": err == nil,
        }
        if err != nil {
            entry["error"] = err.Error()
        }
        if name == "fusion-mlx" {
            if p, ok := s.pool.Get(name); ok {
                if mlx, ok := p.(*adapter.FusionMLXProvider); ok {
                    entry["in_flight"] = mlx.InFlight()
                }
            }
        }
        status[name] = entry
    }

    return status
}

func (s *Server) buildHardwareStatus(m hardware.HardwareMetrics) map[string]interface{} {
    return map[string]interface{}{
        "memory_used_ratio":       m.MemoryUsedRatio,
        "gpu_device_utilization":  m.GPUDeviceUtilization,
        "gpu_in_use_memory_bytes": m.GPUInUseMemory,
        "mlx_active_memory_bytes": m.MLXActiveMemory,
        "swap_page_in_rate":       m.SwapPageInRate,
        "collection_error":        errorString(m.CollectionError),
    }
}

func (s *Server) handleCost(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    if s.costTracker == nil {
        http.Error(w, `{"error":{"message":"Cost tracking not enabled","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }

    keyName := r.URL.Query().Get("key")
    var summary *cost.CostSummary
    if keyName != "" {
        summary = s.costTracker.SummaryByKey(keyName)
    } else {
        summary = s.costTracker.Summary()
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(summary)
}

func healthStatus(healthy bool) string {
    if healthy {
        return "ok"
    }
    return "degraded"
}

func errorString(err error) string {
    if err == nil {
        return ""
    }
    return err.Error()
}
