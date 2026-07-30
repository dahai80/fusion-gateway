package router

import (
    "context"
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

type Backend string

const (
    LocalBackend   Backend = "local"
    CloudBackend   Backend = "cloud"
    ClusterBackend Backend = "cluster"
)

type RequestType string

const (
    RequestTypeChat      RequestType = "chat"
    RequestTypeEmbedding RequestType = "embedding"
    RequestTypeRerank    RequestType = "rerank"
)

type RouteDecision struct {
    Backend Backend
    Reason  string
    NodeID  string
}

type RouteRequest struct {
    Model    string
    Messages interface{}
    Stream   bool
    Tools    interface{}
    Type     RequestType
}

type ClusterSelector interface {
    HealthyNodes() int
    SelectNode(strategy string) (nodeID string, err error)
}

type Engine struct {
    cfg           *config.ConfigSnapshot
    hwCollector   *hardware.Collector
    breakers      map[string]*CircuitBreaker
    localReady    bool
    localInFlight func() int64
    localModels   func() map[string]bool
    cluster       ClusterSelector
}

func NewEngine(cfg *config.ConfigSnapshot, hwCollector *hardware.Collector) *Engine {
    breakers := map[string]*CircuitBreaker{
        "local":  NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cloud":  NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cluster": NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
    }

    return &Engine{
        cfg:           cfg,
        hwCollector:   hwCollector,
        breakers:      breakers,
        localReady:    false,
        localInFlight: func() int64 { return 0 },
        localModels:   func() map[string]bool { return nil },
    }
}

func (e *Engine) SetClusterSelector(cs ClusterSelector) {
    e.cluster = cs
    slog.Info("cluster selector wired to router engine")
}

func (e *Engine) UpdateConfig(cfg *config.ConfigSnapshot) {
    e.cfg = cfg
    slog.Info("router engine config updated", "version", cfg.Version)
}

func (e *Engine) DrainAndApply(cfg *config.ConfigSnapshot) {
    drainTimeout := cfg.Config.HotReload.BreakerDrainTimeout
    if drainTimeout == 0 {
        drainTimeout = 10 * time.Second
    }

    // Phase 1: Drain - wait for in-flight requests to drop
    deadline := time.Now().Add(drainTimeout)
    for time.Now().Before(deadline) {
        if e.localInFlight() == 0 {
            slog.Info("drain complete: no in-flight requests")
            break
        }
        slog.Info("draining: waiting for in-flight requests", "in_flight", e.localInFlight())
        time.Sleep(200 * time.Millisecond)
    }
    if e.localInFlight() > 0 {
        slog.Warn("drain timeout: in-flight requests remain", "in_flight", e.localInFlight())
    }

    // Phase 2: Apply - update config reference and rebuild breakers
    e.cfg = cfg
    e.breakers["local"] = NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker)
    e.breakers["cloud"] = NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker)
    e.breakers["cluster"] = NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker)
    slog.Info("config applied: circuit breakers rebuilt", "version", cfg.Version)

    // Phase 3: Warmup - set local breaker to half_open for gradual recovery
    warmupSuccess := cfg.Config.HotReload.BreakerWarmupSuccess
    if warmupSuccess <= 0 {
        warmupSuccess = 3
    }
    e.breakers["local"].ResetToHalfOpen()
    slog.Info("warmup started: local breaker set to half_open", "warmup_success_target", warmupSuccess)
}

func (e *Engine) SetLocalReady(ready bool) {
    e.localReady = ready
}

func (e *Engine) SetLocalInFlight(fn func() int64) {
    e.localInFlight = fn
}

func (e *Engine) SetLocalModels(fn func() map[string]bool) {
    e.localModels = fn
}

func (e *Engine) CircuitBreakerState(backend string) CircuitBreakerState {
    if b, ok := e.breakers[backend]; ok {
        return b.State()
    }
    return StateOpen
}

func (e *Engine) RecordSuccess(backend string) {
    if b, ok := e.breakers[backend]; ok {
        b.RecordSuccess()
    }
}

func (e *Engine) RecordFailure(backend string) {
    if b, ok := e.breakers[backend]; ok {
        b.RecordFailure()
    }
}

func (e *Engine) Trip(backend, reason string) {
    if b, ok := e.breakers[backend]; ok {
        b.Trip(reason)
    }
    slog.Warn("circuit breaker tripped", "backend", backend, "reason", reason)
}

func (e *Engine) Decide(ctx context.Context, req *RouteRequest) *RouteDecision {
    cfg := config.SnapshotFromContext(ctx)

    // Fast path: embedding/rerank request type routing
    switch req.Type {
    case RequestTypeEmbedding:
        return e.decideEmbedding(ctx, cfg)
    case RequestTypeRerank:
        return e.decideRerank(ctx, cfg)
    }

    // P0: Circuit breaker check — local
    if e.breakers["local"].State() == StateOpen {
        // Try cluster before cloud
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "circuit_breaker_open"}
    }

    // P0.5: Hardware metrics collection error
    hwMetrics := e.hwCollector.Latest()
    if hwMetrics.CollectionError != nil && cfg.Config.Hardware.CollectionErrorProtection {
        slog.Error("hardware metrics collection error, refusing local routing",
            "error", hwMetrics.CollectionError)
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "metrics_collection_error"}
    }

    // P1: System memory overload
    if hwMetrics.MemoryUsedRatio > cfg.Config.Routing.LocalPriority.MaxSystemMemoryRatio {
        e.Trip("local", "memory_overload")
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "memory_overload"}
    }

    // P1.5: MLX memory overload (independent check)
    if hwMetrics.MLXActiveMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
        mlxRatio := float64(hwMetrics.MLXActiveMemory) / float64(hwMetrics.GPUInUseMemory)
        if mlxRatio > cfg.Config.Routing.LocalPriority.MaxMLXMemoryRatio {
            if decision := e.tryCluster(cfg); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "mlx_memory_overload"}
        }
    }

    // P2: Swap page rate
    swapThreshold := cfg.Config.Routing.LocalPriority.SwapPageRateThreshold
    if hwMetrics.SwapPageInRate > swapThreshold || hwMetrics.SwapPageOutRate > swapThreshold {
        e.Trip("local", "swap_thrashing")
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "swap_thrashing"}
    }

    // P2.5: GPU memory low
    if hwMetrics.GPUAllocMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
        gpuAvail := hwMetrics.GPUAllocMemory - hwMetrics.GPUInUseMemory
        if gpuAvail < uint64(float64(hwMetrics.GPUAllocMemory)*0.2) {
            if decision := e.tryCluster(cfg); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "gpu_memory_low"}
        }
    }

    // P3: Local not ready
    if !e.localReady {
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "local_not_ready"}
    }

    // P4: Token budget exceeded
    budget, ok := tokenizer.BudgetFromContext(ctx)
    if !ok || budget.InputTokens == 0 {
        slog.Warn("token budget not found in context, defaulting to cloud")
        return &RouteDecision{Backend: CloudBackend, Reason: "token_budget_missing"}
    }

    if budget.InputTokens > cfg.Config.Routing.TokenThreshold {
        return &RouteDecision{Backend: CloudBackend, Reason: "token_budget_exceeded"}
    }

    // P5: Concurrent limit
    maxConcurrent := cfg.Config.Routing.LocalPriority.MaxConcurrent
    if maxConcurrent > 0 && e.localInFlight() >= int64(maxConcurrent) {
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "concurrent_limit"}
    }

    // P6: Model availability check
    if e.localModels() != nil {
        if !e.localModels()[req.Model] {
            if decision := e.tryCluster(cfg); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "model_not_available_locally"}
        }
    }

    // P7: Local priority (default)
    return &RouteDecision{Backend: LocalBackend, Reason: "local_priority"}
}

func (e *Engine) decideEmbedding(ctx context.Context, cfg *config.ConfigSnapshot) *RouteDecision {
    // Embedding: local-first if breaker closed + local ready
    if e.breakers["local"].State() == StateOpen || !e.localReady {
        if decision := e.tryCluster(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "embedding_local_unavailable"}
    }
    return &RouteDecision{Backend: LocalBackend, Reason: "embedding_local_priority"}
}

func (e *Engine) decideRerank(ctx context.Context, cfg *config.ConfigSnapshot) *RouteDecision {
    // Rerank: typically cloud-only unless local model available
    if e.localReady && e.localModels() != nil {
        for model := range e.localModels() {
            if isRerankModel(model) {
                if e.breakers["local"].State() != StateOpen {
                    return &RouteDecision{Backend: LocalBackend, Reason: "rerank_local_available"}
                }
            }
        }
    }

    if decision := e.tryCluster(cfg); decision != nil {
        return decision
    }
    return &RouteDecision{Backend: CloudBackend, Reason: "rerank_cloud_default"}
}

func isRerankModel(model string) bool {
    return containsAny(model, "rerank", "reranker", "bge-rerank", "cohere-rerank")
}

func containsAny(s string, substrs ...string) bool {
    for _, sub := range substrs {
        if len(s) >= len(sub) {
            for i := 0; i <= len(s)-len(sub); i++ {
                if s[i:i+len(sub)] == sub {
                    return true
                }
            }
        }
    }
    return false
}

func (e *Engine) tryCluster(cfg *config.ConfigSnapshot) *RouteDecision {
    if e.cluster == nil || !cfg.Config.Cluster.Enabled {
        return nil
    }

    if e.breakers["cluster"].State() == StateOpen {
        slog.Debug("cluster breaker open, skipping cluster fallback")
        return nil
    }

    if e.cluster.HealthyNodes() == 0 {
        slog.Debug("no healthy cluster nodes available")
        return nil
    }

    strategy := cfg.Config.Cluster.LoadBalancer
    if strategy == "" {
        strategy = "least-connections"
    }

    nodeID, err := e.cluster.SelectNode(strategy)
    if err != nil {
        slog.Warn("cluster node selection failed", "error", err)
        return nil
    }

    slog.Info("routing to cluster node", "node_id", nodeID, "strategy", strategy)
    return &RouteDecision{Backend: ClusterBackend, Reason: "cluster_fallback", NodeID: nodeID}
}
