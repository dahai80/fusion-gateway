package router

import (
    "context"
    "log/slog"
    "strings"
    "sync"
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
    Backend     Backend
    Reason      string
    NodeID      string
    CloudTarget string
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
    mu            sync.RWMutex
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
    e.mu.Lock()
    defer e.mu.Unlock()
    e.cluster = cs
    slog.Info("cluster selector wired to router engine")
}

func (e *Engine) UpdateConfig(cfg *config.ConfigSnapshot) {
    e.mu.Lock()
    defer e.mu.Unlock()
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
    e.mu.Lock()
    e.cfg = cfg
    e.breakers = map[string]*CircuitBreaker{
        "local":   NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cloud":   NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cluster": NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
    }
    e.mu.Unlock()
    slog.Info("config applied: circuit breakers rebuilt", "version", cfg.Version)

    // Phase 3: Warmup - set local breaker to half_open for gradual recovery
    warmupSuccess := cfg.Config.HotReload.BreakerWarmupSuccess
    if warmupSuccess <= 0 {
        warmupSuccess = 3
    }
    e.mu.RLock()
    e.breakers["local"].ResetToHalfOpen()
    e.mu.RUnlock()
    slog.Info("warmup started: local breaker set to half_open", "warmup_success_target", warmupSuccess)
}

func (e *Engine) SetLocalReady(ready bool) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.localReady = ready
}

func (e *Engine) SetLocalInFlight(fn func() int64) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.localInFlight = fn
}

func (e *Engine) SetLocalModels(fn func() map[string]bool) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.localModels = fn
}

func (e *Engine) CircuitBreakerState(backend string) CircuitBreakerState {
    e.mu.RLock()
    defer e.mu.RUnlock()
    if b, ok := e.breakers[backend]; ok {
        return b.State()
    }
    return StateOpen
}

func (e *Engine) RecordSuccess(backend string) {
    e.mu.RLock()
    defer e.mu.RUnlock()
    if b, ok := e.breakers[backend]; ok {
        b.RecordSuccess()
    }
}

func (e *Engine) RecordFailure(backend string) {
    e.mu.RLock()
    defer e.mu.RUnlock()
    if b, ok := e.breakers[backend]; ok {
        b.RecordFailure()
    }
}

func (e *Engine) Trip(backend, reason string) {
    e.mu.RLock()
    defer e.mu.RUnlock()
    if b, ok := e.breakers[backend]; ok {
        b.Trip(reason)
    }
    slog.Warn("circuit breaker tripped", "backend", backend, "reason", reason)
}

func (e *Engine) Decide(ctx context.Context, req *RouteRequest) *RouteDecision {
    cfg := config.SnapshotFromContext(ctx)

    // L1 fix: collect trip reasons during RLock, apply Trip after unlock
    var trips []string
    e.mu.RLock()
    decision := e.decideLocked(ctx, cfg, req, &trips)
    e.mu.RUnlock()

    // Apply deferred trip calls outside read lock
    for _, reason := range trips {
        e.breakers["local"].Trip(reason)
        slog.Warn("circuit breaker tripped", "backend", "local", "reason", reason)
    }
    return decision
}

func (e *Engine) decideLocked(ctx context.Context, cfg *config.ConfigSnapshot, req *RouteRequest, trips *[]string) *RouteDecision {

    // Fast path: embedding/rerank request type routing
    switch req.Type {
    case RequestTypeEmbedding:
        return e.decideEmbeddingLocked(ctx, cfg)
    case RequestTypeRerank:
        return e.decideRerankLocked(ctx, cfg)
    }

    // P0: Circuit breaker check — local
    if e.breakers["local"].State() == StateOpen {
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "circuit_breaker_open"}
    }

    // P0.5: Hardware metrics collection error
    hwMetrics := e.hwCollector.Latest()
    if hwMetrics.CollectionError != nil && cfg.Config.Hardware.CollectionErrorProtection {
        slog.Error("hardware metrics collection error, refusing local routing",
            "error", hwMetrics.CollectionError)
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "metrics_collection_error"}
    }

    // P1: System memory overload
    if hwMetrics.MemoryUsedRatio > cfg.Config.Routing.LocalPriority.MaxSystemMemoryRatio {
        *trips = append(*trips, "memory_overload")
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "memory_overload"}
    }

    // P1.5: MLX memory overload (independent check)
    if hwMetrics.MLXActiveMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
        mlxRatio := float64(hwMetrics.MLXActiveMemory) / float64(hwMetrics.GPUInUseMemory)
        if mlxRatio > cfg.Config.Routing.LocalPriority.MaxMLXMemoryRatio {
            if decision := e.tryClusterLocked(cfg); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "mlx_memory_overload"}
        }
    }

    // P2: Swap page rate
    swapThreshold := cfg.Config.Routing.LocalPriority.SwapPageRateThreshold
    if hwMetrics.SwapPageInRate > swapThreshold || hwMetrics.SwapPageOutRate > swapThreshold {
        *trips = append(*trips, "swap_thrashing")
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "swap_thrashing"}
    }

    // P2.5: GPU memory low
    if hwMetrics.GPUAllocMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
        gpuAvail := hwMetrics.GPUAllocMemory - hwMetrics.GPUInUseMemory
        if gpuAvail < uint64(float64(hwMetrics.GPUAllocMemory)*0.2) {
            if decision := e.tryClusterLocked(cfg); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "gpu_memory_low"}
        }
    }

    // P3: Local not ready
    if !e.localReady {
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "local_not_ready"}
    }

    // P4: Token budget exceeded
    budget, ok := tokenizer.BudgetFromContext(ctx)
    if !ok {
        slog.Warn("token budget not found in context, defaulting to cloud")
        return &RouteDecision{Backend: CloudBackend, Reason: "token_budget_missing"}
    }
    // L4 fix: zero-token (empty prompt) should route local, not waste cloud quota
    if budget.InputTokens == 0 {
        slog.Debug("empty prompt, routing to local")
        return &RouteDecision{Backend: LocalBackend, Reason: "empty_prompt_local"}
    }

    if budget.InputTokens > cfg.Config.Routing.TokenThreshold {
        decision := &RouteDecision{Backend: CloudBackend, Reason: "token_budget_exceeded"}
        if cfg.Config.Routing.TokenTiers.Enabled {
            decision.CloudTarget = resolveCloudByTier(budget, cfg.Config.Routing.TokenTiers)
            if decision.CloudTarget != "" {
                decision.Reason = "token_budget_exceeded:tier:" + decision.CloudTarget
                slog.Info("token tier matched",
                    "cloud_target", decision.CloudTarget,
                    "input_tokens", budget.InputTokens,
                    "total_budget", budget.TotalBudget,
                )
            }
        }
        return decision
    }

    // P5: Concurrent limit
    maxConcurrent := cfg.Config.Routing.LocalPriority.MaxConcurrent
    if maxConcurrent > 0 && e.localInFlight() >= int64(maxConcurrent) {
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "concurrent_limit"}
    }

    // P6: Model availability check
    if e.localModels() != nil {
        if !e.localModels()[req.Model] {
            // P6.5: Context window fallback — check if a larger model can serve this
            if fallback, ok := cfg.Config.Routing.Fallback.ContextWindowFallback[req.Model]; ok {
                if e.localModels()[fallback] {
                    slog.Info("context window fallback: using larger model",
                        "original", req.Model,
                        "fallback", fallback,
                    )
                    return &RouteDecision{Backend: LocalBackend, Reason: "context_window_fallback:" + fallback}
                }
            }
            if decision := e.tryClusterLocked(cfg); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "model_not_available_locally"}
        }
    }

    // P7: Local priority (default)
    return &RouteDecision{Backend: LocalBackend, Reason: "local_priority"}
}

func (e *Engine) decideEmbeddingLocked(ctx context.Context, cfg *config.ConfigSnapshot) *RouteDecision {
    // Embedding: local-first if breaker closed + local ready
    if e.breakers["local"].State() == StateOpen || !e.localReady {
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "embedding_local_unavailable"}
    }
    return &RouteDecision{Backend: LocalBackend, Reason: "embedding_local_priority"}
}

func (e *Engine) decideRerankLocked(ctx context.Context, cfg *config.ConfigSnapshot) *RouteDecision {
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

    if decision := e.tryClusterLocked(cfg); decision != nil {
        return decision
    }
    return &RouteDecision{Backend: CloudBackend, Reason: "rerank_cloud_default"}
}

func isRerankModel(model string) bool {
    for _, sub := range []string{"rerank", "reranker", "bge-rerank", "cohere-rerank"} {
        if strings.Contains(model, sub) {
            return true
        }
    }
    return false
}

func (e *Engine) tryClusterLocked(cfg *config.ConfigSnapshot) *RouteDecision {
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

func resolveCloudByTier(budget tokenizer.TokenBudget, tier config.TokenTierConfig) string {
    var tokenValue int
    switch tier.Metric {
    case "input":
        tokenValue = budget.InputTokens
    case "output":
        tokenValue = budget.PredictOutputTokens
    default:
        tokenValue = budget.TotalBudget
    }

    slog.Debug("resolving cloud by token tier",
        "metric", tier.Metric,
        "token_value", tokenValue,
        "rules_count", len(tier.Rules),
    )

    for _, rule := range tier.Rules {
        if rule.MaxTokens == 0 || tokenValue <= rule.MaxTokens {
            slog.Debug("token tier rule matched",
                "max_tokens", rule.MaxTokens,
                "backend", rule.Backend,
                "token_value", tokenValue,
            )
            return rule.Backend
        }
    }

    slog.Debug("no token tier rule matched", "token_value", tokenValue)
    return ""
}
