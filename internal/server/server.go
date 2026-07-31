package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "net/http/pprof"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cluster"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/realtime"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

type Server struct {
    cfg             *config.ConfigSnapshot
    hwCollector     *hardware.Collector
    router          *router.Engine
    pool            *adapter.Pool
    tokEngine       *tokenizer.Engine
    httpServer      *http.Server
    startTime       time.Time
    realtimeProxy   *realtime.Proxy
    clusterDiscovery interface {
        Status() []cluster.NodeStatus
        GetNode(id string) (*cluster.Node, bool)
    }
}

func (s *Server) SetClusterDiscovery(d interface {
    Status() []cluster.NodeStatus
    GetNode(id string) (*cluster.Node, bool)
}) {
    s.clusterDiscovery = d
}

func New(
    cfg *config.ConfigSnapshot,
    hwCollector *hardware.Collector,
    routerEngine *router.Engine,
    pool *adapter.Pool,
    tokEngine *tokenizer.Engine,
) *Server {
    var rp *realtime.Proxy
    if cfg.Config.Realtime.Enabled {
        rp = realtime.NewProxy(
            cfg.Config.Routing.Negotiation.RouteHeader,
            cfg.Config.Routing.Negotiation.RouteHeaderValue,
        )
        slog.Info("realtime proxy enabled", "backend_url", cfg.Config.Realtime.BackendURL)
    }
    return &Server{
        cfg:           cfg,
        hwCollector:   hwCollector,
        router:        routerEngine,
        pool:          pool,
        tokEngine:     tokEngine,
        startTime:     time.Now(),
        realtimeProxy: rp,
    }
}

func (s *Server) Start() error {
    mux := http.NewServeMux()

    mux.HandleFunc("/v1/chat/completions", s.withMiddleware(s.handleChatCompletions))
    mux.HandleFunc("/v1/embeddings", s.withMiddleware(s.handleEmbeddings))
    mux.HandleFunc("/v1/rerank", s.withMiddleware(s.handleRerank))
    mux.HandleFunc("/v1/realtime", s.withMiddleware(s.handleRealtime))
    mux.HandleFunc("/v1/models", s.withMiddleware(s.handleModels))

    mux.HandleFunc("/health", s.handleHealth)
    mux.HandleFunc("/healthz", s.handleHealthz)
    mux.HandleFunc("/readyz", s.handleReadyz)
    mux.HandleFunc("/livez", s.handleLivez)
    mux.HandleFunc("/v1/status", s.withMiddleware(s.handleStatus))

    mux.HandleFunc("/metrics", observability.Handler().ServeHTTP)

    mux.HandleFunc("/admin/gc", s.withMiddleware(s.handleAdminGC))
    mux.HandleFunc("/admin/config/reload", s.withMiddleware(s.handleConfigReload))

    // pprof endpoints for profiling — protected by auth
    mux.HandleFunc("/debug/pprof/", s.withMiddleware(pprof.Index))
    mux.HandleFunc("/debug/pprof/cmdline", s.withMiddleware(pprof.Cmdline))
    mux.HandleFunc("/debug/pprof/profile", s.withMiddleware(pprof.Profile))
    mux.HandleFunc("/debug/pprof/symbol", s.withMiddleware(pprof.Symbol))
    mux.HandleFunc("/debug/pprof/trace", s.withMiddleware(pprof.Trace))

    addr := fmt.Sprintf("%s:%d", s.cfg.Config.Server.Host, s.cfg.Config.Server.Port)
    slog.Info("server starting", "addr", addr)

    s.httpServer = &http.Server{
        Addr:              addr,
        Handler:           mux,
        ReadTimeout:       30 * time.Second,
        ReadHeaderTimeout: 10 * time.Second,
        WriteTimeout:      120 * time.Second,
        IdleTimeout:       120 * time.Second,
    }

    return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
    slog.Info("server shutting down")
    return s.httpServer.Shutdown(ctx)
}

func (s *Server) withMiddleware(handler http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        snap := config.GetSnapshot()

        chain := []func(http.Handler) http.Handler{
            middleware.RequestID,
            middleware.CORS(&snap.Config.CORS),
            middleware.ConfigSnapshot(snap),
            middleware.APIKeyAuth(&snap.Config.Auth),
        }

        var final http.Handler = handler
        for i := len(chain) - 1; i >= 0; i-- {
            final = chain[i](final)
        }

        final.ServeHTTP(w, r)
    }
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20 // default 5MB
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req adapter.ChatRequest
    if err := json.Unmarshal(body, &req); err != nil {
        slog.Error("invalid json in chat request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := r.Context()

    textContent := extractTextContent(req.Messages)
    inputTokens, err := s.tokEngine.CountTokens(ctx, textContent)
    if err != nil {
        slog.Error("token counting failed", "error", err)
        inputTokens = len(textContent) / 4
    }

    budget := s.tokEngine.EstimateBudget(inputTokens, req.MaxTokens, req.Model, req.Tools != nil, req.Stream)
    ctx = tokenizer.WithTokenBudget(ctx, budget)

    routeReq := &router.RouteRequest{
        Model:  req.Model,
        Stream: req.Stream,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    slog.Info("route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_tokens", budget.InputTokens,
        "total_budget", budget.TotalBudget,
    )

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, ok := s.pool.Get("fusion-mlx")
        if !ok {
            http.Error(w, `{"error":{"message":"Local backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery == nil || decision.NodeID == "" {
            slog.Warn("cluster backend selected but no discovery or nodeID, falling back to cloud",
                "node_id", decision.NodeID,
            )
            provider = s.resolveCloudProvider(decision, &req, w)
            if provider == nil {
                return
            }
        } else {
            node, ok := s.clusterDiscovery.GetNode(decision.NodeID)
            if !ok {
                slog.Warn("cluster node not found, falling back to cloud",
                    "node_id", decision.NodeID,
                )
                provider = s.resolveCloudProvider(decision, &req, w)
                if provider == nil {
                    return
                }
            } else {
                slog.Info("routing to cluster node",
                    "node_id", node.ID,
                    "node_addr", node.Address,
                    "strategy", decision.Reason,
                )
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing)
            }
        }

    default:
        provider = s.resolveCloudProvider(decision, &req, w)
        if provider == nil {
            return
        }
    }

    start := time.Now()

    if req.Stream {
        s.handleStreamChat(ctx, w, provider, &req, decision, budget, start)
    } else {
        s.handleNonStreamChat(ctx, w, provider, &req, decision, budget, start)
    }
}

func (s *Server) resolveCloudProvider(decision *router.RouteDecision, req *adapter.ChatRequest, w http.ResponseWriter) adapter.Provider {
    cloudBackend := ""
    if decision != nil && decision.CloudTarget != "" {
        cloudBackend = decision.CloudTarget
        slog.Info("using token tier cloud target", "cloud_target", cloudBackend)
    }
    if cloudBackend == "" {
        cloudBackend = s.cfg.Config.Routing.Fallback.CloudDefault
    }
    if cloudBackend == "" {
        cloudBackend = "openai"
    }

    if req != nil && s.cfg.Config.Routing.Fallback.Enabled && s.cfg.Config.Routing.Fallback.ModelMapping != nil {
        if mapped, ok := s.cfg.Config.Routing.Fallback.ModelMapping[req.Model]; ok {
            slog.Info("model mapped for cloud routing",
                "local_model", req.Model,
                "cloud_model", mapped,
                "cloud_backend", cloudBackend,
            )
            req.Model = mapped
        }
    }

    p, ok := s.pool.Get(cloudBackend)
    if !ok {
        http.Error(w, `{"error":{"message":"Cloud backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return nil
    }
    return p
}

func (s *Server) handleStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time) {
    ch, err := provider.StreamChat(ctx, req)
    if err != nil {
        slog.Error("stream chat failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Stream chat failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
    w.Header().Set("X-Fusion-Degraded", "false")

    flusher, canFlush := w.(http.Flusher)
    var degraded bool

    for chunk := range ch {
        if chunk.Degraded {
            degraded = true //nolint:ineffassign
            w.Header().Set("X-Fusion-Degraded", "true")
            slog.Warn("stream degraded: backpressure triggered, falling back to non-streaming")

            warningEvt := map[string]string{"type": "warning", "message": "stream degraded, falling back to non-streaming"}
            warningData, _ := json.Marshal(warningEvt)
            fmt.Fprintf(w, "event: warning\ndata: %s\n\n", warningData)
            if canFlush {
                flusher.Flush()
            }

            nonStreamReq := *req
            nonStreamReq.Stream = false
            resp, fallbackErr := provider.Chat(ctx, &nonStreamReq)
            if fallbackErr != nil {
                slog.Error("non-streaming fallback failed", "error", fallbackErr)
                errEvt := map[string]string{"type": "error", "message": "non-streaming fallback also failed"}
                errData, _ := json.Marshal(errEvt)
                fmt.Fprintf(w, "event: error\ndata: %s\n\n", errData)
                if canFlush {
                    flusher.Flush()
                }
                return
            }

            finalChunk := adapter.StreamChunk{
                ID:      resp.ID,
                Object:  "chat.completion.chunk",
                Created: resp.Created,
                Model:   resp.Model,
                Choices: make([]adapter.ChoiceDelta, len(resp.Choices)),
            }
            for i, c := range resp.Choices {
                finishReason := c.FinishReason
                finalChunk.Choices[i] = adapter.ChoiceDelta{
                    Index:        c.Index,
                    Delta:        c.Message,
                    FinishReason: &finishReason,
                }
            }

            data, _ := json.Marshal(finalChunk)
            fmt.Fprintf(w, "data: %s\n\n", data)
            if canFlush {
                flusher.Flush()
            }
            return
        }

        data, err := json.Marshal(chunk)
        if err != nil {
            slog.Error("marshal stream chunk failed", "error", err)
            continue
        }

        fmt.Fprintf(w, "data: %s\n\n", data)
        if canFlush {
            flusher.Flush()
        }
    }

    fmt.Fprintf(w, "data: [DONE]\n\n")
    if canFlush {
        flusher.Flush()
    }

    duration := time.Since(start).Seconds()
    if degraded {
        observability.RecordRequest(string(decision.Backend), req.Model, "degraded")
    } else {
        observability.RecordRequest(string(decision.Backend), req.Model, "success")
    }
    observability.RecordDuration(string(decision.Backend), req.Model, duration)
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    s.router.RecordSuccess(string(decision.Backend))
}

func (s *Server) handleNonStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time) {
    resp, err := provider.Chat(ctx, req)
    if err != nil {
        slog.Error("chat failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Chat failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    duration := time.Since(start).Seconds()
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration)
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    if resp.Usage.CompletionTokens > 0 {
        observability.RecordTokens("output", string(decision.Backend), resp.Usage.CompletionTokens)
    }
    s.router.RecordSuccess(string(decision.Backend))

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    var req adapter.EmbeddingRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    ctx := r.Context()
    inputLen := len(req.Input)

    // Route through router engine for embedding request type
    routeReq := &router.RouteRequest{
        Model: req.Model,
        Type:  router.RequestTypeEmbedding,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    slog.Info("embedding route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_count", inputLen,
    )

    // Try cluster sharding for large batch + cluster route
    if decision.Backend == router.ClusterBackend && inputLen > 32 && s.clusterDiscovery != nil {
        if d, ok := s.clusterDiscovery.(*cluster.Discovery); ok {
            resp, err := cluster.ShardEmbedding(ctx, d, &req, s.cfg.Config.Routing)
            if err == nil {
                w.Header().Set("Content-Type", "application/json")
                w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
                _ = json.NewEncoder(w).Encode(resp)
                return
            }
            slog.Warn("cluster embedding sharding failed, falling back to single node",
                "error", err,
                "input_count", inputLen,
            )
        }
    }

    // Resolve provider based on routing decision
    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, err := s.pool.GetByBackend("fusion-mlx")
        if err != nil {
            http.Error(w, `{"error":{"message":"Local embedding backend not available"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery != nil && decision.NodeID != "" {
            if node, ok := s.clusterDiscovery.GetNode(decision.NodeID); ok {
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing)
            }
        }
        if provider == nil {
            p := s.resolveCloudProvider(decision, nil, w)
            if p == nil {
                return
            }
            provider = p
        }

    default:
        p := s.resolveCloudProvider(decision, nil, w)
        if p == nil {
            return
        }
        provider = p
    }

    resp, err := provider.Embedding(ctx, &req)
    if err != nil {
        slog.Error("embedding failed", "provider", provider.Name(), "error", err)
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Embedding failed"}}`, http.StatusBadGateway)
        return
    }

    s.router.RecordSuccess(string(decision.Backend))
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    var req adapter.RerankRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    ctx := r.Context()

    routeReq := &router.RouteRequest{
        Model: req.Model,
        Type:  router.RequestTypeRerank,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    slog.Info("rerank route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
    )

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, err := s.pool.GetByBackend("fusion-mlx")
        if err != nil {
            http.Error(w, `{"error":{"message":"Local rerank backend not available"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery != nil && decision.NodeID != "" {
            if node, ok := s.clusterDiscovery.GetNode(decision.NodeID); ok {
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing)
            }
        }
        if provider == nil {
            p := s.resolveCloudProvider(decision, nil, w)
            if p == nil {
                return
            }
            provider = p
        }

    default:
        p := s.resolveCloudProvider(decision, nil, w)
        if p == nil {
            return
        }
        provider = p
    }

    start := time.Now()
    resp, err := provider.Rerank(ctx, &req)
    if err != nil {
        slog.Error("rerank failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Rerank failed"}}`, http.StatusBadGateway)
        return
    }

    duration := time.Since(start).Seconds()
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration)
    s.router.RecordSuccess(string(decision.Backend))

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
    if !s.cfg.Config.Realtime.Enabled || s.realtimeProxy == nil {
        http.Error(w, `{"error":{"message":"Realtime API not enabled","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }

    backendURL := s.cfg.Config.Realtime.BackendURL
    if backendURL == "" {
        http.Error(w, `{"error":{"message":"Realtime backend not configured","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    apiKey := s.cfg.Config.Realtime.APIKey

    slog.Info("realtime: incoming connection",
        "client", r.RemoteAddr,
        "backend", backendURL,
    )

    observability.RecordRouteDecision("realtime", "websocket_proxy")
    s.realtimeProxy.UpgradeAndProxy(w, r, backendURL, apiKey)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    models := make([]adapter.ModelInfo, 0)

    for _, name := range s.pool.ListProviders() {
        provider, ok := s.pool.Get(name)
        if !ok {
            continue
        }
        providerModels, err := provider.ListModels(ctx)
        if err != nil {
            slog.Debug("list models failed for provider", "provider", name, "error", err)
            continue
        }
        for _, m := range providerModels {
            m.AvailableBackends = []string{name}
            models = append(models, m)
        }
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "object": "list",
        "data":   models,
    })
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    results := s.pool.HealthCheckAll(r.Context())
    allHealthy := true
    for _, err := range results {
        if err != nil {
            allHealthy = false
            break
        }
    }


    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "status":   healthStatus(allHealthy),
        "backends": results,
    })
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, "ok")
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
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
        go mlxProvider.TriggerGCWhenIdle()
        w.WriteHeader(http.StatusAccepted)
        fmt.Fprintf(w, `{"status":"gc_queued","in_flight":%d,"message":"GC will execute when in-flight requests reach zero"}`, mlxProvider.InFlight())
        return
    }

    go mlxProvider.SafeGC()
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, `{"status":"gc_triggered"}`)
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, `{"status":"config_reload_is_handled_by_file_watch"}`)
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

func extractTextContent(messages []adapter.ChatMessage) string {
    var sb string
    for _, msg := range messages {
        if str, ok := msg.Content.(string); ok {
            sb += str + " "
        }
    }
    return sb
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
