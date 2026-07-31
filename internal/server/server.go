package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "net/http/pprof"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/admin"
    adminui "github.com/fusion-gateway/fusion-gateway/internal/admin/ui"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/cluster"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/realtime"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
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
    rateLimiter     *middleware.RateLimiter
    cache           *cache.Cache
    costTracker     *cost.Tracker
    piiMiddleware   *middleware.PIIMiddleware
    cloudStrategy   *router.CloudStrategy
    latencyTracker  *router.LatencyTracker
    store           store.Store
    otelShutdown    func(context.Context) error
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

func (s *Server) GetStore() store.Store {
    return s.store
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
    lt := router.NewLatencyTracker(1000)
    cs := router.NewCloudStrategy(cfg.Config.CloudRouting, lt)

    logMaxLen := 10000
    if cfg.Config.Admin != nil && cfg.Config.Admin.LogMaxLen > 0 {
        logMaxLen = cfg.Config.Admin.LogMaxLen
    }
    memStore := memorystore.NewMemoryStore(logMaxLen)
    slog.Info("request log store initialized", "max_len", logMaxLen)

    otelShutdown, err := observability.InitTracing(context.Background(), observability.OTelConfig{
        Enabled:     cfg.Config.Observability.OtelEnabled,
        Endpoint:    cfg.Config.Observability.OtelEndpoint,
        Protocol:    cfg.Config.Observability.OtelProtocol,
        ServiceName: cfg.Config.Observability.OtelServiceName,
    })
    if err != nil {
        slog.Warn("otel tracing init failed, continuing without tracing", "error", err)
    }

    return &Server{
        cfg:            cfg,
        hwCollector:    hwCollector,
        router:         routerEngine,
        pool:           pool,
        tokEngine:      tokEngine,
        startTime:      time.Now(),
        realtimeProxy:  rp,
        rateLimiter:    middleware.NewRateLimiter(),
        cache:          cache.New(cfg.Config.Cache),
        costTracker:    cost.NewTracker(10000),
        piiMiddleware:  middleware.NewPIIMiddleware(cfg.Config.PII),
        cloudStrategy:  cs,
        latencyTracker: lt,
        store:          memStore,
        otelShutdown:   otelShutdown,
    }
}

func (s *Server) Start() error {
    mux := http.NewServeMux()

    mux.HandleFunc("/v1/chat/completions", s.withMiddleware(s.handleChatCompletions))
    mux.HandleFunc("/v1/completions", s.withMiddleware(s.handleCompletions))
    mux.HandleFunc("/v1/embeddings", s.withMiddleware(s.handleEmbeddings))
    mux.HandleFunc("/v1/rerank", s.withMiddleware(s.handleRerank))
    mux.HandleFunc("/v1/realtime", s.withMiddleware(s.handleRealtime))
    mux.HandleFunc("/v1/models", s.withMiddleware(s.handleModels))
    mux.HandleFunc("/v1/cost", s.withMiddleware(s.handleCost))
    mux.HandleFunc("/v1/images/generations", s.withMiddleware(s.handleImages))
    mux.HandleFunc("/v1/messages", s.withMiddleware(s.handleAnthropicMessages))
    mux.HandleFunc("/v1/audio/transcriptions", s.withMiddleware(s.handleTranscriptions))
    mux.HandleFunc("/v1/audio/speech", s.withMiddleware(s.handleSpeech))
    mux.HandleFunc("/v1/moderations", s.withMiddleware(s.handleModeration))

    mux.HandleFunc("/health", s.handleHealth)
    mux.HandleFunc("/healthz", s.handleHealthz)
    mux.HandleFunc("/readyz", s.handleReadyz)
    mux.HandleFunc("/livez", s.handleLivez)
    mux.HandleFunc("/v1/status", s.withMiddleware(s.handleStatus))

    mux.HandleFunc("/metrics", observability.Handler().ServeHTTP)

    mux.HandleFunc("/admin/gc", s.withMiddleware(s.handleAdminGC))
    mux.HandleFunc("/admin/config/reload", s.withMiddleware(s.handleConfigReload))

    // Admin API + Dashboard UI
    if s.cfg.Config.Admin != nil && s.cfg.Config.Admin.Enabled {
        admin.SetJWTSecret(s.cfg.Config.Admin.JWTSecret)
        adminHandler := admin.NewHandler(s.store)
        adminHandler.RegisterRoutes(mux)
        mux.HandleFunc("/admin/api/login", admin.HandleLogin)
        mux.Handle("/admin/", http.StripPrefix("/admin/", adminui.Handler()))
    }

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
    if s.otelShutdown != nil {
        if err := s.otelShutdown(ctx); err != nil {
            slog.Warn("otel shutdown error", "error", err)
        }
    }
    return s.httpServer.Shutdown(ctx)
}

func (s *Server) withMiddleware(handler http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        snap := config.GetSnapshot()

        chain := []func(http.Handler) http.Handler{
            middleware.RequestID,
            observability.HTTPMiddleware,
            middleware.CORS(&snap.Config.CORS),
            middleware.ConfigSnapshot(snap),
            middleware.APIKeyAuth(&snap.Config.Auth),
            middleware.BudgetBlock(s.store),
            middleware.RateLimit(&snap.Config.Routing.RateLimit, s.rateLimiter, nil),
        }

        var final http.Handler = handler
        for i := len(chain) - 1; i >= 0; i-- {
            final = chain[i](final)
        }

        start := time.Now()
        entry := middleware.InitRequestLog(r)
        r = middleware.WithRequestLogContext(r, entry)
        rec := middleware.NewResponseRecorder(w)

        final.ServeHTTP(rec, r)

        keyCfg := middleware.GetAuthKeyConfig(r.Context())
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        entry.StatusCode = rec.StatusCode
        middleware.FinalizeAndAppendLog(entry, s.store, start, keyName)
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

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    textContent := extractTextContent(req.Messages)

    // PII scanning
    if s.piiMiddleware != nil {
        deny, detected := s.piiMiddleware.ScanText(textContent)
        if deny {
            slog.Warn("request denied: PII detected", "types", detected, "model", req.Model)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"Request contains PII (%s)","type":"pii_error"}}`, strings.Join(detected, ",")), http.StatusBadRequest)
            return
        }
    }

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

    // Cloud strategy: if multiple cloud backends available, let strategy decide
    if s.cloudStrategy != nil && cloudBackend == s.cfg.Config.Routing.Fallback.CloudDefault {
        var availableBackends []string
        for _, name := range s.pool.ListProviders() {
            if p, ok := s.pool.Get(name); ok {
                if p.Name() != "fusion-mlx" {
                    availableBackends = append(availableBackends, name)
                }
            }
        }
        if len(availableBackends) > 1 {
            selected := s.cloudStrategy.Select(availableBackends)
            if selected != "" {
                slog.Info("cloud strategy selected backend", "strategy", s.cfg.Config.CloudRouting.Strategy, "backend", selected)
                cloudBackend = selected
            }
        }
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
    var outputTokens int
    includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
    var lastChunkID string
    var lastChunkModel string
    var lastChunkCreated int64

    for chunk := range ch {
        if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
            outputTokens += chunk.Usage.CompletionTokens
        }
        lastChunkID = chunk.ID
        lastChunkModel = chunk.Model
        lastChunkCreated = chunk.Created

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

    // Send usage chunk if stream_options.include_usage was requested
    if includeUsage {
        usageChunk := adapter.StreamChunk{
            ID:      lastChunkID,
            Object:  "chat.completion.chunk",
            Created: lastChunkCreated,
            Model:   lastChunkModel,
            Choices: []adapter.ChoiceDelta{},
            Usage: &adapter.UsageResponse{
                PromptTokens:     budget.InputTokens,
                CompletionTokens: outputTokens,
                TotalTokens:      budget.InputTokens + outputTokens,
            },
        }
        usageData, _ := json.Marshal(usageChunk)
        fmt.Fprintf(w, "data: %s\n\n", usageData)
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

    // Update request log with model/channel/token details
    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = string(decision.Backend)
        logEntry.InputTokens = budget.InputTokens
        logEntry.OutputTokens = outputTokens
        logEntry.TotalTokens = budget.InputTokens + outputTokens
    }

    // Latency tracking for stream
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), time.Duration(duration*float64(time.Second)))
    }

    // Cost tracking for stream
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, budget.InputTokens, outputTokens)
    }
}

func (s *Server) handleNonStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time) {
    // Cache lookup
    var cacheKey string
    if s.cache != nil {
        cacheKey = cache.ComputeCacheKey(req.Model, req.Messages, req.Temperature, req.MaxTokens, req.TopP)
        if cached, ok := s.cache.Get(cacheKey); ok {
            slog.Debug("cache hit for non-stream chat", "model", req.Model)
            w.Header().Set("Content-Type", "application/json")
            w.Header().Set("X-Cache", "HIT")
            w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
            w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
            _, _ = w.Write(cached)
            return
        }
    }

    // Retry wrapper
    chatFn := func(ctx context.Context, r *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        return provider.Chat(ctx, r)
    }
    chatFn = middleware.RetryChat(s.cfg.Config.Routing.Retry, chatFn)

    resp, err := chatFn(ctx, req)
    if err != nil {
        slog.Error("chat failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Chat failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    duration := time.Since(start)
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration.Seconds())
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    if resp.Usage.CompletionTokens > 0 {
        observability.RecordTokens("output", string(decision.Backend), resp.Usage.CompletionTokens)
    }
    s.router.RecordSuccess(string(decision.Backend))

    // Update request log with model/channel/token details
    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = string(decision.Backend)
        logEntry.InputTokens = budget.InputTokens
        logEntry.OutputTokens = resp.Usage.CompletionTokens
        logEntry.TotalTokens = budget.InputTokens + resp.Usage.CompletionTokens
    }

    // Latency tracking
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), duration)
    }

    // Cost tracking
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, budget.InputTokens, resp.Usage.CompletionTokens)
    }

    // Cache store
    if s.cache != nil && cacheKey != "" {
        if respData, marshalErr := json.Marshal(resp); marshalErr == nil {
            s.cache.Set(cacheKey, respData)
        }
    }

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
    if s.cache != nil {
        w.Header().Set("X-Cache", "MISS")
    }
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

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

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

    embStart := time.Now()
    resp, err := provider.Embedding(ctx, &req)
    if err != nil {
        slog.Error("embedding failed", "provider", provider.Name(), "error", err)
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Embedding failed"}}`, http.StatusBadGateway)
        return
    }

    embDuration := time.Since(embStart)
    s.router.RecordSuccess(string(decision.Backend))

    // Latency + cost tracking for embedding
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), embDuration)
    }
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, inputLen, 0)
    }

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

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

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

    duration := time.Since(start)
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration.Seconds())
    s.router.RecordSuccess(string(decision.Backend))

    // Latency + cost tracking for rerank
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), duration)
    }
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, len(req.Documents), 0)
    }

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

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req adapter.ImageRequest
    if err := json.Unmarshal(body, &req); err != nil {
        slog.Error("invalid json in image request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    if req.N <= 0 {
        req.N = 1
    }

    cloudBackend := s.cfg.Config.Routing.Fallback.CloudDefault
    if cloudBackend == "" {
        cloudBackend = "openai"
    }

    provider, ok := s.pool.Get(cloudBackend)
    if !ok {
        http.Error(w, `{"error":{"message":"Image generation backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    imgProvider, ok := provider.(interface {
        Images(ctx context.Context, req *adapter.ImageRequest) (*adapter.ImageResponse, error)
    })
    if !ok {
        http.Error(w, `{"error":{"message":"Selected backend does not support image generation","type":"server_error"}}`, http.StatusBadRequest)
        return
    }

    start := time.Now()
    resp, err := imgProvider.Images(r.Context(), &req)
    if err != nil {
        slog.Error("image generation failed", "provider", provider.Name(), "error", err)
        http.Error(w, `{"error":{"message":"Image generation failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    if logEntry := middleware.GetRequestLog(r.Context()); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = "cloud"
        logEntry.StatusCode = http.StatusOK
    }

    slog.Info("image generation completed",
        "model", req.Model,
        "provider", provider.Name(),
        "latency_ms", time.Since(start).Milliseconds(),
    )

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
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

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    // Legacy /v1/completions: convert to chat format and re-use chat handler
    var legacyReq struct {
        Model       string   `json:"model"`
        Prompt      string   `json:"prompt"`
        Temperature *float64 `json:"temperature,omitempty"`
        MaxTokens   *int     `json:"max_tokens,omitempty"`
        Stream      bool     `json:"stream"`
        Stop        []string `json:"stop,omitempty"`
        TopP        *float64 `json:"top_p,omitempty"`
    }
    if err := json.NewDecoder(r.Body).Decode(&legacyReq); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    slog.Info("legacy completions request, converting to chat format", "model", legacyReq.Model)

    // Convert prompt to single user message
    chatReq := adapter.ChatRequest{
        Model: legacyReq.Model,
        Messages: []adapter.ChatMessage{
            {Role: "user", Content: legacyReq.Prompt},
        },
        Temperature: legacyReq.Temperature,
        MaxTokens:   legacyReq.MaxTokens,
        Stream:      legacyReq.Stream,
        Stop:        legacyReq.Stop,
        TopP:        legacyReq.TopP,
    }

    // Re-encode and forward to chat completions handler via internal call
    ctx := r.Context()

    if !middleware.CheckModelAllowlist(r, chatReq.Model) {
        slog.Warn("model not allowed for this key", "model", chatReq.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    textContent := legacyReq.Prompt
    inputTokens, err := s.tokEngine.CountTokens(ctx, textContent)
    if err != nil {
        slog.Error("token counting failed", "error", err)
        inputTokens = len(textContent) / 4
    }

    budget := s.tokEngine.EstimateBudget(inputTokens, chatReq.MaxTokens, chatReq.Model, chatReq.Tools != nil, chatReq.Stream)
    ctx = tokenizer.WithTokenBudget(ctx, budget)

    routeReq := &router.RouteRequest{
        Model:  chatReq.Model,
        Stream: chatReq.Stream,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    slog.Info("route decision (completions)",
        "model", chatReq.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_tokens", budget.InputTokens,
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
    default:
        provider = s.resolveCloudProvider(decision, &chatReq, w)
        if provider == nil {
            return
        }
    }

    start := time.Now()

    if chatReq.Stream {
        s.handleStreamChat(ctx, w, provider, &chatReq, decision, budget, start)
    } else {
        s.handleNonStreamChat(ctx, w, provider, &chatReq, decision, budget, start)
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

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var antReq adapter.AnthropicRequest
    if err := json.Unmarshal(body, &antReq); err != nil {
        slog.Error("invalid json in anthropic messages request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := r.Context()
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: antReq.Model, Stream: antReq.Stream})
    slog.Info("anthropic messages route decision", "model", antReq.Model, "backend", string(decision.Backend), "reason", decision.Reason)

    var provider adapter.Provider
    if decision.Backend == router.LocalBackend {
        provider, _ = s.pool.Get("fusion-mlx")
    } else {
        provider = s.resolveCloudProvider(decision, nil, w)
        if provider == nil { return }
    }
    if provider == nil {
        http.Error(w, `{"error":{"message":"No provider available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    if antProv, ok := provider.(*adapter.AnthropicProvider); ok {
        if antReq.Stream {
            s.handleStreamAnthropicMessages(ctx, w, antProv, &antReq)
        } else {
            s.handleNonStreamAnthropicMessages(ctx, w, antProv, &antReq)
        }
    } else {
        chatReq := adapter.AnthropicToOpenAIChatRequest(&antReq)
        chatReq.Stream = antReq.Stream
        start := time.Now()
        budget := tokenizer.TokenBudget{InputTokens: 0, TotalBudget: antReq.MaxTokens}
        if antReq.Stream {
            s.handleStreamChat(ctx, w, provider, chatReq, decision, budget, start)
        } else {
            s.handleNonStreamChat(ctx, w, provider, chatReq, decision, budget, start)
        }
    }
}

func (s *Server) handleNonStreamAnthropicMessages(ctx context.Context, w http.ResponseWriter, p *adapter.AnthropicProvider, req *adapter.AnthropicRequest) {
    resp, err := p.Messages(ctx, req)
    if err != nil {
        slog.Error("anthropic messages failed", "error", err)
        http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStreamAnthropicMessages(ctx context.Context, w http.ResponseWriter, p *adapter.AnthropicProvider, req *adapter.AnthropicRequest) {
    ch, err := p.StreamMessages(ctx, req)
    if err != nil {
        slog.Error("anthropic stream messages failed", "error", err)
        http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
    flusher, _ := w.(http.Flusher)

    for event := range ch {
        data, _ := json.Marshal(event)
        fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
        if flusher != nil { flusher.Flush() }
    }
    fmt.Fprintf(w, "event: message_stop\ndata: {}\n\n")
    if flusher != nil { flusher.Flush() }
}

func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    if err := r.ParseMultipartForm(32 << 20); err != nil {
        slog.Error("failed to parse multipart form", "error", err)
        http.Error(w, `{"error":{"message":"Failed to parse multipart form","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.MultipartForm.RemoveAll()

    model := r.FormValue("model")
    if model == "" { model = "whisper-1" }

    ctx := r.Context()
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: model})
    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type TranscriptionProvider interface {
        Transcription(ctx context.Context, r *http.Request) (json.RawMessage, error)
    }
    if tp, ok := provider.(TranscriptionProvider); ok {
        result, err := tp.Transcription(ctx, r)
        if err != nil {
            slog.Error("transcription failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.Write(result)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support transcription","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req struct {
        Model string `json:"model"`
        Input string `json:"input"`
        Voice string `json:"voice"`
    }
    if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := r.Context()
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})
    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type SpeechProvider interface {
        Speech(ctx context.Context, reqBody []byte) ([]byte, string, error)
    }
    if sp, ok := provider.(SpeechProvider); ok {
        audioData, contentType, err := sp.Speech(ctx, body)
        if err != nil {
            slog.Error("speech synthesis failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", contentType)
        w.Write(audioData)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support speech","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) handleModeration(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req struct {
        Model string      `json:"model,omitempty"`
        Input interface{} `json:"input"`
    }
    if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := r.Context()
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})
    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type ModerationProvider interface {
        Moderation(ctx context.Context, reqBody []byte) (json.RawMessage, error)
    }
    if mp, ok := provider.(ModerationProvider); ok {
        result, err := mp.Moderation(ctx, body)
        if err != nil {
            slog.Error("moderation failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.Write(result)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support moderation","type":"invalid_request"}}`, http.StatusBadRequest)
}
