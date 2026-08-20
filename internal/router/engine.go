package router

import (
    "context"
    "fmt"
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

// defaultRatioMinInputTokens is the floor below which the output/input ratio
// check is skipped when config does not set output_input_ratio_min_input_tokens.
// Tiny prompts produce statistically meaningless ratios (issue #48).
const defaultRatioMinInputTokens = 32

type RouteDecision struct {
    Backend     Backend
    Reason      string
    NodeID      string
    CloudTarget string
    // Adapter names the LoRA adapter (e.g. "lora-code") that the server layer
    // must hot-mount on fusion-mlx via the per-request "adapters" field before
    // forwarding. Empty = no adapter swap (bare base model). Set only by the
    // intent:code path. See fusion-gateway UDS zero-copy + intent-routing.
    Adapter string
}

type RouteRequest struct {
    Model    string
    Messages interface{}
    // Text is the flattened request text (last user message for chat). Populated
    // by the server for the D4 semantic intent classifier (issue #22) so the
    // router need not depend on the adapter message type. Empty for non-text
    // requests (embeddings/rerank) where classification is not meaningful.
    Text    string
    Stream  bool
    Tools   interface{}
    Type    RequestType
    SpaceID string
}

type ClusterSelector interface {
    HealthyNodes() int
    SelectNode(strategy string) (nodeID string, err error)
    // HealthyNodesByPlatform returns healthy node count for a given platform
    // (D4 dispatch-by-platform, issue #23/#25). Empty platform = all nodes.
    HealthyNodesByPlatform(platform string) int
    // SelectNodeByPlatform selects a healthy node on the given platform.
    // Empty platform falls back to platform-agnostic selection.
    SelectNodeByPlatform(strategy, platform string) (nodeID string, err error)
}

type Engine struct {
    mu              sync.RWMutex
    cfg             *config.ConfigSnapshot
    hwCollector     *hardware.Collector
    breakers        map[string]*CircuitBreaker
    localReady      bool
    localInFlight   func() int64
    localModels     func() map[string]bool
    cluster         ClusterSelector
    sessionAffinity *SessionAffinity
    classifier      IntentClassifier
    // heuristicClassifier is the in-process sub-ms intent classifier that
    // replaces the sync LLM RouterLightClassifier on the code path (latency
    // lever for <20ms gateway overhead). Wired via SetHeuristicClassifier;
    // nil = disabled (fall through to classifier/rule chain). Same
    // IntentClassifier interface so the engine treats both uniformly.
    heuristicClassifier IntentClassifier
    // adapterLookup is a read-only view over the local LoRA adapter index
    // (Stream D). Wired via SetAdapterLookup; nil = no index available, so
    // code-adapter validation is skipped (best-effort, log-only on miss).
    adapterLookup AdapterLookup
}

func NewEngine(cfg *config.ConfigSnapshot, hwCollector *hardware.Collector) *Engine {
    breakers := map[string]*CircuitBreaker{
        "local":  NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cloud":  NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cluster": NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
    }

    return &Engine{
        cfg:             cfg,
        hwCollector:     hwCollector,
        breakers:        breakers,
        localReady:      false,
        localInFlight:   func() int64 { return 0 },
        localModels:     func() map[string]bool { return nil },
        sessionAffinity: NewSessionAffinity(30 * time.Minute),
        classifier:      NoopClassifier{},
    }
}

func (e *Engine) SetClusterSelector(cs ClusterSelector) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.cluster = cs
    slog.Info("cluster selector wired to router engine")
}

// SetIntentClassifier wires a D4 semantic intent classifier (issue #22).
// When nil or NoopClassifier, the semantic layer is a no-op and the existing
// P0-P7 rule chain decides routing unchanged.
func (e *Engine) SetIntentClassifier(c IntentClassifier) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.classifier = c
    slog.Info("intent classifier wired to router engine")
}

// SetHeuristicClassifier wires the in-process sub-ms heuristic intent
// classifier (latency lever for <20ms gateway overhead, replaces the sync LLM
// classifier on the code path). When nil, the heuristic layer is a no-op and
// routing falls through to the LLM classifier (if enabled) then the rule chain.
// Safe to call on hot-reload — guarded by the engine mutex like the LLM one.
func (e *Engine) SetHeuristicClassifier(c IntentClassifier) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.heuristicClassifier = c
    if c == nil {
        slog.Info("heuristic classifier disabled (nil), routing falls through to classifier/rule chain")
    } else {
        slog.Info("heuristic classifier wired to router engine")
    }
}

// SetAdapterLookup wires the read-only LoRA adapter index (Stream D) used for
// best-effort code_adapter validation on the heuristic code path. nil = no
// index, validation skipped (the index may legitimately be absent until the
// first refresh lands). Safe to call on hot-reload.
func (e *Engine) SetAdapterLookup(a AdapterLookup) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.adapterLookup = a
    if a == nil {
        slog.Info("adapter lookup disabled (nil), code_adapter validation skipped")
    } else {
        slog.Info("adapter lookup wired to router engine")
    }
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
    if b, ok := e.breakers["local"]; ok {
        b.ResetToHalfOpen()
    }
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

func (e *Engine) RecordAffinity(spaceID, providerName string) {
    if e.sessionAffinity != nil {
        e.sessionAffinity.Record(spaceID, providerName)
    }
}

func (e *Engine) RecordSuccess(backend string) {
    // L1 fix: RLock to find breaker, then call RecordSuccess outside RLock
    e.mu.RLock()
    b, ok := e.breakers[backend]
    e.mu.RUnlock()
    if ok {
        b.RecordSuccess()
    }
}

func (e *Engine) RecordFailure(backend string) {
    // L1 fix: RLock to find breaker, then call RecordFailure outside RLock
    e.mu.RLock()
    b, ok := e.breakers[backend]
    e.mu.RUnlock()
    if ok {
        b.RecordFailure()
    }
}

func (e *Engine) Trip(backend, reason string) {
    // L1 fix: RLock to find breaker, then call Trip outside RLock
    e.mu.RLock()
    b, ok := e.breakers[backend]
    e.mu.RUnlock()
    if ok {
        b.Trip(reason)
        slog.Warn("circuit breaker tripped", "backend", backend, "reason", reason)
    }
}

func (e *Engine) Decide(ctx context.Context, req *RouteRequest) *RouteDecision {
    cfg := config.SnapshotFromContext(ctx)

    // L1 fix: collect trip reasons during RLock, apply Trip after unlock
    var trips []string
    e.mu.RLock()
    decision := e.decideLocked(ctx, cfg, req, &trips)
    e.mu.RUnlock()

    // Apply deferred trip calls outside read lock (use e.Trip for proper locking)
    for _, reason := range trips {
        e.Trip("local", reason)
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

    // Mode fast-path: explicit routing mode overrides all other rules
    mode := cfg.Config.Routing.Mode
    switch mode {
    case "local":
        slog.Info("routing mode: local, forcing all requests to local backend")
        return &RouteDecision{Backend: LocalBackend, Reason: "mode_local"}
    case "cloud":
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        slog.Info("routing mode: cloud, forcing all requests to cloud backend")
        return &RouteDecision{Backend: CloudBackend, Reason: "mode_cloud"}
    case "hybrid":
        // fall through to existing priority-chain logic
    }

    // P-1: D4 semantic intent layer (issue #22/#23/#25). Runs before the
    // rule chain so semantic dispatch wins, with the rule chain as fallback.
    // Disabled by default (intent_classifier.enabled); NoopClassifier makes
    // this a no-op so existing P0-P7 behavior is unchanged.
    if decision := e.decideIntentLocked(ctx, cfg, req); decision != nil {
        return decision
    }

    // P0: Circuit breaker check — local
    if e.breakers["local"].State() == StateOpen {
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "circuit_breaker_open"}
    }

    // P0.3: Session affinity — same space_id routes to same provider
    if req.SpaceID != "" && e.sessionAffinity != nil {
        if providerName, ok := e.sessionAffinity.Lookup(req.SpaceID); ok {
            slog.Info("session affinity hit", "space_id", req.SpaceID, "provider", providerName)
            switch providerName {
            case "fusion-mlx":
                if e.breakers["local"].State() != StateOpen && e.localReady {
                    return &RouteDecision{Backend: LocalBackend, Reason: "session_affinity:local"}
                }
                slog.Warn("session affinity target unavailable (local breaker open or not ready), re-routing", "space_id", req.SpaceID)
            default:
                return &RouteDecision{Backend: CloudBackend, Reason: "session_affinity:" + providerName, CloudTarget: providerName}
            }
        }
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

    // P4.5: Output/Input ratio routing
    // Skip the ratio check for tiny requests: with very few input tokens the
    // predicted-output/input ratio is statistically meaningless (e.g. 5/4=1.25
    // for a 4-token "say pong" prompt) and would misroute local-eligible
    // requests to cloud (issue #48). Guard with a minimum input-token floor.
    minInputTokens := cfg.Config.Routing.OutputInputRatioMinInputTokens
    if minInputTokens <= 0 {
        minInputTokens = defaultRatioMinInputTokens
    }
    if budget.InputTokens > 0 && budget.InputTokens < minInputTokens {
        slog.Info("output/input ratio skipped: input tokens below floor",
            "input_tokens", budget.InputTokens,
            "min_input_tokens", minInputTokens,
            "predict_output_tokens", budget.PredictOutputTokens,
        )
    } else if budget.InputTokens > 0 {
        ratio := float64(budget.PredictOutputTokens) / float64(budget.InputTokens)

        // P4.5a: RatioTiers — segment routing by ratio to different backends
        if cfg.Config.Routing.RatioTiers.Enabled && len(cfg.Config.Routing.RatioTiers.Rules) > 0 {
            target := resolveCloudByRatio(ratio, cfg.Config.Routing.RatioTiers)
            if target != "" {
                slog.Info("ratio tier matched, routing to cloud",
                    "ratio", fmt.Sprintf("%.2f", ratio),
                    "cloud_target", target,
                    "input_tokens", budget.InputTokens,
                    "predict_output_tokens", budget.PredictOutputTokens,
                )
                return &RouteDecision{Backend: CloudBackend, Reason: "ratio_tier_matched", CloudTarget: target}
            }
        }

        // P4.5b: Fallback single threshold (when RatioTiers disabled)
        ratioThreshold := cfg.Config.Routing.OutputInputRatioThreshold
        if ratioThreshold > 0 && ratio > ratioThreshold {
            slog.Info("output/input ratio exceeded, routing to cloud",
                "ratio", fmt.Sprintf("%.2f", ratio),
                "threshold", fmt.Sprintf("%.2f", ratioThreshold),
                "input_tokens", budget.InputTokens,
                "predict_output_tokens", budget.PredictOutputTokens,
            )
            return &RouteDecision{Backend: CloudBackend, Reason: "output_input_ratio_exceeded"}
        }
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
    // Mode fast-path for embedding
    mode := cfg.Config.Routing.Mode
    switch mode {
    case "local":
        slog.Info("routing mode: local, forcing embedding to local backend")
        return &RouteDecision{Backend: LocalBackend, Reason: "mode_local:embedding"}
    case "cloud":
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        slog.Info("routing mode: cloud, forcing embedding to cloud backend")
        return &RouteDecision{Backend: CloudBackend, Reason: "mode_cloud:embedding"}
    case "hybrid":
        // fall through
    }

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
    // Mode fast-path for rerank
    mode := cfg.Config.Routing.Mode
    switch mode {
    case "local":
        slog.Info("routing mode: local, forcing rerank to local backend")
        return &RouteDecision{Backend: LocalBackend, Reason: "mode_local:rerank"}
    case "cloud":
        if decision := e.tryClusterLocked(cfg); decision != nil {
            return decision
        }
        slog.Info("routing mode: cloud, forcing rerank to cloud backend")
        return &RouteDecision{Backend: CloudBackend, Reason: "mode_cloud:rerank"}
    case "hybrid":
        // fall through
    }

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

// tryClusterByPlatformLocked attempts to route to a cluster node on a specific
// platform (D4 dispatch-by-platform, issue #23/#25). Returns nil if cluster is
// disabled, the breaker is open, or no healthy node on that platform — the
// caller then falls back to the rule chain or cloud.
func (e *Engine) tryClusterByPlatformLocked(cfg *config.ConfigSnapshot, platform string) *RouteDecision {
    if e.cluster == nil || !cfg.Config.Cluster.Enabled || platform == "" {
        return nil
    }
    if e.breakers["cluster"].State() == StateOpen {
        slog.Debug("cluster breaker open, skipping platform cluster dispatch", "platform", platform)
        return nil
    }
    if e.cluster.HealthyNodesByPlatform(platform) == 0 {
        slog.Debug("no healthy cluster nodes on platform", "platform", platform)
        return nil
    }
    strategy := cfg.Config.Cluster.LoadBalancer
    if strategy == "" {
        strategy = "least-connections"
    }
    nodeID, err := e.cluster.SelectNodeByPlatform(strategy, platform)
    if err != nil {
        slog.Warn("platform cluster node selection failed", "platform", platform, "error", err)
        return nil
    }
    slog.Info("routing to cluster node by platform", "node_id", nodeID, "platform", platform, "strategy", strategy)
    return &RouteDecision{Backend: ClusterBackend, Reason: "cluster_platform:" + platform, NodeID: nodeID}
}

// decideIntentLocked is the D4 semantic intent layer (issue #22/#23/#25).
// It runs before the P0-P7 rule chain. Returns nil to defer to the rule chain
// (intent unknown, low confidence, disabled, or no target platform available).
func (e *Engine) decideIntentLocked(ctx context.Context, cfg *config.ConfigSnapshot, req *RouteRequest) *RouteDecision {
    // Heuristic-first: the in-process sub-ms classifier runs before the LLM
    // classifier on every request (latency lever for <20ms gateway overhead).
    // When it returns IntentCode with sufficient confidence, dispatch straight
    // to LocalBackend + LoRA hot-swap, skipping the sync LLM classifier
    // entirely (the dominant latency killer on the code path).
    hc := cfg.Config.Routing.HeuristicClassifier
    if hc.Enabled && e.heuristicClassifier != nil {
        hRes := classifyAndLog(ctx, e.heuristicClassifier, req)
        if hRes.Intent == IntentCode && hRes.Confidence >= hc.MinConfidence {
            adapter := hRes.Params["code_adapter"]
            if adapter == "" {
                adapter = hc.CodeAdapter
            }
            if adapter == "" {
                slog.Info("heuristic: code intent but no code_adapter configured, deferring to rule chain",
                    "confidence", hRes.Confidence, "model", req.Model)
                return nil
            }
            // Best-effort adapter validation (Stream D): if an adapter index is
            // wired and does not list this adapter, log a warning but still
            // dispatch — the index may be stale or not yet refreshed, and
            // suppressing a valid code intent is worse than a possibly-missing
            // adapter (fusion-mlx will error on hot-swap if truly absent).
            if e.adapterLookup != nil && !e.adapterLookup.Has(adapter) {
                slog.Warn("heuristic: code_adapter not found in adapter index, dispatching anyway (index may be stale)",
                    "adapter", adapter, "model", req.Model)
            }
            slog.Info("heuristic: code intent routed to local + lora hot-swap",
                "adapter", adapter, "confidence", hRes.Confidence, "model", req.Model)
            return &RouteDecision{
                Backend: LocalBackend,
                Reason:  "intent:code:lora:" + adapter,
                Adapter: adapter,
            }
        }
        if hRes.Intent != IntentUnknown {
            slog.Debug("heuristic classified non-code intent, falling through to LLM classifier/rule chain",
                "intent", hRes.Intent, "confidence", hRes.Confidence, "model", req.Model)
        }
    }

    ic := cfg.Config.Routing.IntentClassifier
    if !ic.Enabled {
        return nil
    }
    classifier := e.classifier
    if classifier == nil {
        classifier = NoopClassifier{}
    }
    res := classifyAndLog(ctx, classifier, req)
    if res.Intent == IntentUnknown {
        return nil
    }
    if res.Confidence < ic.MinConfidence {
        slog.Info("intent confidence below threshold, deferring to rule chain",
            "intent", res.Intent, "confidence", res.Confidence, "min", ic.MinConfidence)
        return nil
    }

    // IntentLightweight: prefer Mac local. Don't force — let the rule chain
    // apply hardware/circuit-breaker protections; it already routes to local
    // when healthy. So defer (return nil) for lightweight.
    if res.Intent == IntentLightweight {
        return nil
    }

    // IntentCode (LLM-classifier path): the sync LLM classifier also recognized
    // a coding intent. Dispatch to LocalBackend + LoRA hot-swap using the
    // configured code_adapter, mirroring the heuristic-first path. The adapter
    // comes from the classifier Params (if it set one) or the heuristic config
    // fallback. With no adapter configured, defer to the rule chain (bare base).
    if res.Intent == IntentCode {
        adapter := res.Params["code_adapter"]
        if adapter == "" {
            adapter = cfg.Config.Routing.HeuristicClassifier.CodeAdapter
        }
        if adapter == "" {
            slog.Info("llm classifier: code intent but no code_adapter configured, deferring to rule chain",
                "confidence", res.Confidence, "model", req.Model)
            return nil
        }
        slog.Info("llm classifier: code intent routed to local + lora hot-swap",
            "adapter", adapter, "confidence", res.Confidence, "model", req.Model)
        return &RouteDecision{
            Backend: LocalBackend,
            Reason:  "intent:code:lora:" + adapter,
            Adapter: adapter,
        }
    }

    // Heavy model / diffusion: dispatch to the target platform's cluster node
    // (Windows CUDA by default). Fall back to cloud if no platform node.
    pr := cfg.Config.Cluster.PlatformRouting
    platform := PlatformForIntent(res.Intent, pr.HeavyModelPlatform, pr.DiffusionPlatform)
    if platform == "" {
        return nil
    }
    if decision := e.tryClusterByPlatformLocked(cfg, platform); decision != nil {
        decision.Reason = "intent:" + string(res.Intent) + ":" + decision.Reason
        return decision
    }
    // No healthy node on target platform — fall back to cloud, with a reason
    // that records the original intent for observability.
    slog.Info("no cluster node on target platform, falling back to cloud",
        "intent", res.Intent, "platform", platform)
    return &RouteDecision{Backend: CloudBackend, Reason: "intent:" + string(res.Intent) + ":no_platform_node"}
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

func resolveCloudByRatio(ratio float64, rt config.RatioTierConfig) string {
    slog.Debug("resolving cloud by ratio tier",
        "ratio", ratio,
        "rules_count", len(rt.Rules),
    )

    for _, rule := range rt.Rules {
        if ratio <= rule.MaxRatio {
            slog.Debug("ratio tier rule matched",
                "max_ratio", rule.MaxRatio,
                "backend", rule.Backend,
                "ratio", ratio,
            )
            return rule.Backend
        }
    }

    slog.Debug("no ratio tier rule matched, ratio exceeds all tiers", "ratio", ratio)
    return ""
}