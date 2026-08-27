package server

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/cluster"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

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

    ctx := adapter.WithFusionHeaders(r.Context(), r)

    // Issue #28: empty model would be passed through to fusion-mlx and 404.
    // Backfill from routing.default_model, or auto-discover the first loaded
    // local model when no default is configured.
    if strings.TrimSpace(req.Model) == "" {
        resolved, err := s.resolveDefaultModel(ctx)
        if err != nil {
            slog.Warn("default model resolution failed", "error", err)
        }
        if resolved != "" {
            slog.Info("backfilling empty model", "model", resolved)
            req.Model = resolved
        }
    }

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

    // #120 multimodal guard: an image_url content block is invisible to the
    // router's text-only signal (extractTextContent only keeps string Content,
    // so RouteRequest.Text drops image parts), so without this guard a
    // multimodal payload is routed to a text-only model (local text model or a
    // text-only cloud model like glm5.2) and rejected upstream with 400
    // multimodal_not_supported -> gateway 502 (same class the /v1/messages
    // guard in anthropic_messages.go prevents). Local-first when a vision model
    // is loaded; cloud VLM fallback (#120, e.g. fusion-browser Visual Grounding
    // where local fusion-mlx has no VLM) when configured; clear 400 otherwise.
    multimodalDecision := s.multimodalRouteDecision(&req)
    var decision *router.RouteDecision
    if multimodalDecision != nil {
        decision = multimodalDecision
    } else {
        routeReq := &router.RouteRequest{
            Model:   req.Model,
            Text:    textContent,
            Stream:  req.Stream,
            SpaceID: adapter.SpaceIDFromContext(ctx),
        }
        decision = s.router.Decide(ctx, routeReq)
    }
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_tokens", budget.InputTokens,
        "total_budget", budget.TotalBudget,
    )

    // #120: multimodal request but no local vision model and no cloud VLM
    // configured — reject with a clear 400 instead of forwarding to a
    // text-only model that rejects with 400 -> gateway 502.
    if multimodalUnconfigured(decision) {
        slog.Warn("chat multimodal request rejected: no routing.multimodal.local_model loaded and no cloud_backend/cloud_model configured", "model", req.Model)
        http.Error(w, `{"error":{"message":"multimodal requests require routing.multimodal.local_model (loaded) or routing.multimodal.cloud_backend + cloud_model to be configured; no vision model is available","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, ok := s.pool.Get("fusion-mlx")
        if !ok {
            http.Error(w, `{"error":{"message":"Local backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p
        // intent:code hot-swap: the routing engine detected a coding intent
        // and selected a LoRA adapter to mount on fusion-mlx. Inject it into
        // the per-request "adapters" field so fusion-mlx hot-loads the derived
        // engine (FUSION_LORA_INPLACE_SWAP=1 = ms-scale, no base reload).
        if decision.Adapter != "" {
            req.Adapters = decision.Adapter
            slog.Info("lora adapter hot-swap on local backend",
                "adapter", decision.Adapter,
                "model", req.Model,
                "reason", decision.Reason,
            )
        }

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
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            }
        }

    default:
        provider = s.resolveCloudProvider(decision, &req, w)
        if provider == nil {
            return
        }
    }

    if spaceID := adapter.SpaceIDFromContext(ctx); spaceID != "" {
        s.router.RecordAffinity(spaceID, provider.Name())
    }

    // #102 ADR-001 sub-task 3: opt-in local wait-queue. Engaged ONLY in
    // mode=local + queue_enabled — LocalQueue() returns nil otherwise, so the
    // default hybrid path is untouched. Gate BEFORE forwarding so the slot is
    // held for the whole inference; 429 on queue_timeout keeps the local box
    // from being overrun when cloud is off. Engine stays pure (no blocking).
    if decision.Backend == router.LocalBackend {
        if release, err := s.acquireLocalSlot(ctx); err != nil {
            slog.Warn("local slot queue rejected request", "reason", err.Error(), "model", req.Model)
            writeQueue429(w, err)
            return
        } else {
            defer release()
        }
    }

    // #102 ADR-001 sub-task 4: register the in-flight task so the
    // /v1/agent/tasks/{id}/cancel endpoint can propagate cancellation to this
    // forward's ctx. task-id = X-Request-ID (middleware-injected). Wrap ctx
    // with WithCancel so registry.Cancel() signals the stream goroutine; the
    // slot is released by the existing defer above, NOT here (no double-
    // release). Release the registry entry on return (idempotent vs Cancel).
    if taskID := taskIDFromContext(ctx); taskID != "" {
        streamCtx, cancel := context.WithCancel(ctx)
        // B12: bind the enqueuing auth-key name so the cancel endpoint can
        // refuse a cross-tenant cancel. Empty when no principal (auth off).
        owner := ""
        if kc := middleware.GetAuthKeyConfig(ctx); kc != nil {
            owner = kc.Name
        }
        s.taskRegistry.Register(taskID, owner, cancel)
        defer s.taskRegistry.Release(taskID)
        ctx = streamCtx
    }

    start := time.Now()
    tenant := "anonymous"
    if kc := middleware.GetAuthKeyConfig(r.Context()); kc != nil && kc.Name != "" {
        tenant = kc.Name
    }

    if req.Stream {
        s.handleStreamChat(ctx, w, provider, &req, decision, budget, start)
    } else {
        s.handleNonStreamChat(ctx, w, provider, &req, decision, budget, start, tenant)
    }
}

// acquireLocalSlot gates a local-backend forward on the opt-in wait-queue
// (#102 ADR-001 sub-task 3). Returns a release closure the caller MUST defer.
// When the queue is disabled (hybrid/cloud, or mode=local without
// queue_enabled) it returns a no-op release + nil error immediately — zero
// behavior change. On timeout returns ErrQueueTimeout (caller writes 429).
func (s *Server) acquireLocalSlot(ctx context.Context) (func(), error) {
    q := s.router.LocalQueue()
    if q == nil {
        return func() {}, nil
    }
    return q.Acquire(ctx, s.router.QueueTimeout())
}

// writeQueue429 emits the rate-limit error for a queue timeout. Mirrors the
// OpenAI error envelope shape used by the rest of /v1/chat/completions.
func writeQueue429(w http.ResponseWriter, err error) {
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Retry-After", "5")
    w.WriteHeader(http.StatusTooManyRequests)
    fmt.Fprintf(w, `{"error":{"message":"local inference queue full: %s","type":"rate_limit_error"}}`, err.Error())
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

    if req != nil {
        req.Model = s.applyCloudModelMapping(req.Model, cloudBackend)
    }

    p, ok := s.pool.Get(cloudBackend)
    if !ok {
        slog.Error("cloud backend not available", "backend", cloudBackend)
        if w != nil {
            http.Error(w, `{"error":{"message":"Cloud backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        }
        return nil
    }
    return p
}

// applyCloudModelMapping maps a client-supplied model name to the cloud
// backend's actual model id via routing.fallback.model_mapping. This is what
// lets SDK model aliases (e.g. claude code's claude-opus-4-7) reach a backend
// that only serves a different model id (e.g. glm5.2 via LiteLLM). Returns the
// input unchanged when mapping is disabled or no entry matches. s.cfg is
// swapped on hot-reload via RebuildMiddlewareChain (main.go OnReload), so newly
// added aliases take effect after /admin/config/reload (which calls
// config.Reload) — see issue #57.
func (s *Server) applyCloudModelMapping(model, cloudBackend string) string {
    if model == "" {
        return model
    }
    if !s.cfg.Config.Routing.Fallback.Enabled || s.cfg.Config.Routing.Fallback.ModelMapping == nil {
        return model
    }
    mapped, ok := s.cfg.Config.Routing.Fallback.ModelMapping[model]
    if !ok || strings.TrimSpace(mapped) == "" {
        return model
    }
    slog.Info("model mapped for cloud routing",
        "local_model", model,
        "cloud_model", mapped,
        "cloud_backend", cloudBackend,
        "config_version", s.cfg.Version,
    )
    return mapped
}

func (s *Server) handleStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time) {
    // F4: wdCtx is a child of the request ctx so the idle watchdog can cancel
    // ONLY the child to unblock an upstream body.Read (stalled stream, issue
    // #69) while the parent ctx stays clean — ctx.Err()==nil after a watchdog
    // trip distinguishes it from a real client cancel (which cancels parent).
    // StreamChat receives wdCtx so the upstream goroutine honors the cancel.
    wdCtx, wdCancel := context.WithCancel(ctx)
    defer wdCancel()
    ch, err := provider.StreamChat(wdCtx, req)
    if err != nil {
        // RR4: ErrLocalSlotFull is an expected diversion (local hard cap
        // reached), not a backend failure. Resolve a cloud provider, map the
        // model, and recurse into this same stream handler so the client gets
        // a real cloud SSE stream instead of a 502. No breaker failure record
        // (would trip local + cascade) and no ERROR log (normal load-shed).
        if errors.Is(err, adapter.ErrLocalSlotFull) && decision.Backend == router.LocalBackend {
            slog.Info("RR4 local slot full, diverting stream chat to cloud",
                "model", req.Model, "reason", "max_concurrent reached")
            cloudDecision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "rr4_slot_full_redirect"}
            cloudProvider := s.resolveCloudProvider(cloudDecision, req, nil)
            if cloudProvider != nil {
                s.handleStreamChat(ctx, w, cloudProvider, req, cloudDecision, budget, start)
                return
            }
            slog.Error("RR4 stream cloud diversion failed: no cloud provider available", "model", req.Model)
            writeChatFailedError(w, "Stream chat failed", err)
            return
        }
        slog.Error("stream chat failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.recordOutcome(decision.Backend, decision.NodeID, false)
        writeChatFailedError(w, "Stream chat failed", err)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
    w.Header().Set("X-Fusion-Degraded", "false")

    flusher, canFlush := w.(http.Flusher)
    var outputTokens int
    includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
    var lastChunkID string
    var lastChunkModel string
    var lastChunkCreated int64

    // F4 fix: idle watchdog + keepalive + timing summary, mirroring
    // handleStreamAnthropicMessages (issue #69/#81). The prior plain
    // `for chunk := range ch` blocked on body.Read forever when an upstream
    // stalled without closing the connection — the same mid-stream stall class
    // issue #69 fixed for /v1/messages but left absent on the OpenAI-compat
    // path, which carries the larger share of traffic (local + multi-cloud).
    // wdCtx is a child of the request ctx: the idle watchdog cancels only the
    // child to unblock body.Read, while the parent ctx distinguishes a real
    // client cancel (suppress terminal) from a watchdog trip (end honestly).
    // OpenAI-compat SSE has no native ping event, so keepalive emits an SSE
    // comment line (`: keepalive\n\n`) the client ignores but that keeps bytes
    // flowing so a slow-but-live upstream is not timed out.
    streamCfg := s.cfg.Config.Routing.Stream
    streamStart := time.Now()
    var firstChunkAt time.Time
    var lastChunkAt time.Time
    chunkCount := 0
    endReason := "ch_closed_no_done"

    writeChunk := func(chunk adapter.StreamChunk) bool {
        data, err := json.Marshal(chunk)
        if err != nil {
            slog.Error("marshal stream chunk failed", "error", err)
            return true
        }
        if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
            slog.Warn("stream chat client write failed", "provider", provider.Name(), "error", err)
            return false
        }
        if canFlush {
            flusher.Flush()
        }
        return true
    }

    if streamCfg.KeepaliveInterval <= 0 {
        // Backward-compat: no keepalive/watchdog configured, pure blocking
        // forward loop (original behavior, plus the F2 client-cancel check).
        for chunk := range ch {
            select {
            case <-ctx.Done():
                slog.Info("stream chat cancelled by client", "provider", provider.Name(), "error", ctx.Err())
                if canceller, ok := provider.(interface{ Cancel(string) }); ok {
                    canceller.Cancel(lastChunkID)
                }
                endReason = "client_canceled"
                goto streamDone
            default:
            }
            if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
                outputTokens += chunk.Usage.CompletionTokens
            }
            lastChunkID = chunk.ID
            lastChunkModel = chunk.Model
            lastChunkCreated = chunk.Created
            chunkCount++
            lastChunkAt = time.Now()
            if firstChunkAt.IsZero() {
                firstChunkAt = lastChunkAt
            }
            if !writeChunk(chunk) {
                endReason = "write_failed"
                goto streamDone
            }
        }
    } else {
        ticker := time.NewTicker(streamCfg.KeepaliveInterval)
        defer ticker.Stop()
        lastChunkAt = time.Now()
        watchdogTripped := false
        done := false
        for !done {
            select {
            case chunk, ok := <-ch:
                if !ok {
                    done = true
                    break
                }
                if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
                    outputTokens += chunk.Usage.CompletionTokens
                }
                lastChunkID = chunk.ID
                lastChunkModel = chunk.Model
                lastChunkCreated = chunk.Created
                chunkCount++
                lastChunkAt = time.Now()
                if firstChunkAt.IsZero() {
                    firstChunkAt = lastChunkAt
                }
                if !writeChunk(chunk) {
                    endReason = "write_failed"
                    done = true
                }
            case <-ticker.C:
                if !watchdogTripped && time.Since(lastChunkAt) >= streamCfg.IdleTimeout {
                    slog.Warn("stream chat idle watchdog tripped, cancelling upstream",
                        "provider", provider.Name(), "model", req.Model,
                        "idle", time.Since(lastChunkAt), "threshold", streamCfg.IdleTimeout)
                    watchdogTripped = true
                    endReason = "watchdog_tripped"
                    wdCancel()
                    done = true
                } else if !watchdogTripped {
                    // SSE comment keepalive — client ignores, bytes keep flowing.
                    if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
                        slog.Warn("stream chat keepalive write failed", "provider", provider.Name(), "error", err)
                        endReason = "write_failed"
                        done = true
                    } else if canFlush {
                        flusher.Flush()
                    }
                }
            case <-ctx.Done():
                slog.Info("stream chat cancelled by client", "provider", provider.Name(), "error", ctx.Err())
                if canceller, ok := provider.(interface{ Cancel(string) }); ok {
                    canceller.Cancel(lastChunkID)
                }
                endReason = "client_canceled"
                done = true
            }
        }
    }

streamDone:
    // F4 timing summary (mirrors /v1/messages #81): one INFO per stream so a
    // "response stopped arriving" recurrence is diagnosable as upstream stall
    // (watchdog_tripped / long last_chunk_idle) vs client cancel (H-A vs H-B).
    func() {
        dur := time.Since(streamStart)
        firstTTFB := time.Duration(0)
        if !firstChunkAt.IsZero() {
            firstTTFB = firstChunkAt.Sub(streamStart)
        }
        lastIdle := time.Duration(0)
        if !lastChunkAt.IsZero() {
            lastIdle = time.Since(lastChunkAt)
        }
        slog.Info("stream chat summary",
            "model", req.Model,
            "provider", provider.Name(),
            "duration", dur,
            "chunks", chunkCount,
            "first_event_ttfb", firstTTFB,
            "last_event_idle", lastIdle,
            "end_reason", endReason)
    }()

    // F4: terminal handling by exit reason. client_canceled (ctx.Err()!=nil on
    // the PARENT ctx) or write_failed means the client is gone — do NOT write a
    // terminal the client won't read and do NOT record success (mirrors the
    // /v1/messages cancel suppression, issue #46; broken pipe, issue #79).
    // watchdog_tripped means the upstream stalled: fall through to write [DONE]
    // so the client finalizes a short-but-complete response it can retry, but
    // record a stall + RecordFailure below so a repeatedly-dead local trips its
    // breaker. clean completion falls through to usage + [DONE] + success.
    if ctx.Err() != nil || endReason == "write_failed" {
        return
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
    // F4: record by exit reason. watchdog_tripped is a stall, not a success —
    // RecordFailure nudges the breaker so a dead local trips; the metric status
    // reflects the real outcome rather than masking every exit as "success".
    if endReason == "watchdog_tripped" {
        observability.RecordRequest(string(decision.Backend), req.Model, "stall")
        s.recordOutcome(decision.Backend, decision.NodeID, false)
    } else {
        observability.RecordRequest(string(decision.Backend), req.Model, "success")
        s.recordOutcome(decision.Backend, decision.NodeID, true)
    }
    observability.RecordDuration(string(decision.Backend), req.Model, duration)
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)

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

func (s *Server) handleNonStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time, tenantName string) {
    // Cache lookup
    var cacheKey string
    if s.cache != nil {
        cacheKey = cache.ComputeCacheKey(req.Model, req.Messages, req.Temperature, req.MaxTokens, req.TopP,
            "tenant", tenantName, "tools", req.Tools, "tool_choice", req.ToolChoice, "stop", req.Stop)
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
        // RR4: ErrLocalSlotFull from the adapter is an expected diversion
        // (local hard cap reached), NOT a backend failure. Skip the breaker
        // failure record + ERROR log that the generic failure path applies,
        // and fall through to the A4 cloud fallback below. Recording it as a
        // failure would trip the local breaker and cascade; logging ERROR
        // would poison observability with a normal load-shed event.
        slotFull := errors.Is(err, adapter.ErrLocalSlotFull) && decision.Backend == router.LocalBackend
        failErr := err
        if !slotFull {
            slog.Error("chat failed", "provider", provider.Name(), "error", err)
            observability.RecordRequest(string(decision.Backend), req.Model, "error")
            s.recordOutcome(decision.Backend, decision.NodeID, false)
        } else {
            slog.Info("RR4 local slot full, diverting chat to cloud",
                "model", req.Model, "reason", "max_concurrent reached")
        }

        // A4 fix: runtime backend switch fallback — if local fails, try cloud
        if decision.Backend == router.LocalBackend || decision.Backend == router.ClusterBackend {
            fallbackProvider := s.resolveCloudProvider(nil, req, nil)
            if fallbackProvider != nil {
                slog.Info("A4 fallback: switching to cloud after local/cluster failure",
                    "original_backend", string(decision.Backend),
                    "fallback_provider", fallbackProvider.Name(),
                )
                fallbackResp, fallbackErr := fallbackProvider.Chat(ctx, req)
                if fallbackErr == nil {
                    duration := time.Since(start)
                    observability.RecordRequest("cloud", req.Model, "success")
                    observability.RecordDuration("cloud", req.Model, duration.Seconds())
                    observability.RecordTokens("input", "cloud", budget.InputTokens)
                    if fallbackResp.Usage.CompletionTokens > 0 {
                        observability.RecordTokens("output", "cloud", fallbackResp.Usage.CompletionTokens)
                    }
                    s.router.RecordSuccess("cloud")

                    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
                        logEntry.Model = req.Model
                        logEntry.ChannelName = fallbackProvider.Name()
                        logEntry.ChannelType = "cloud"
                        logEntry.InputTokens = budget.InputTokens
                        logEntry.OutputTokens = fallbackResp.Usage.CompletionTokens
                        logEntry.TotalTokens = budget.InputTokens + fallbackResp.Usage.CompletionTokens
                    }
                    if s.latencyTracker != nil {
                        s.latencyTracker.Record(fallbackProvider.Name(), duration)
                    }
                    if s.costTracker != nil {
                        keyCfg := middleware.GetAuthKeyConfig(ctx)
                        keyName := "anonymous"
                        if keyCfg != nil && keyCfg.Name != "" {
                            keyName = keyCfg.Name
                        }
                        s.costTracker.Record(keyName, "cloud", req.Model, budget.InputTokens, fallbackResp.Usage.CompletionTokens)
                    }
                    if s.cache != nil && cacheKey != "" {
                        if respData, marshalErr := json.Marshal(fallbackResp); marshalErr == nil {
                            s.cache.Set(cacheKey, respData)
                        }
                    }
                    w.Header().Set("Content-Type", "application/json")
                    w.Header().Set("X-Route-Decision", fmt.Sprintf("cloud:fallback_from_%s", decision.Backend))
                    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
                    if s.cache != nil {
                        w.Header().Set("X-Cache", "MISS")
                    }
                    _ = json.NewEncoder(w).Encode(fallbackResp)
                    return
                }
                slog.Error("A4 fallback: cloud also failed", "error", fallbackErr)
                failErr = fallbackErr
            }
        }

        writeChatFailedError(w, "Chat failed", failErr)
        return
    }

    duration := time.Since(start)
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration.Seconds())
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    if resp.Usage.CompletionTokens > 0 {
        observability.RecordTokens("output", string(decision.Backend), resp.Usage.CompletionTokens)
    }
    s.recordOutcome(decision.Backend, decision.NodeID, true)

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
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLegacyBodySize)).Decode(&legacyReq); err != nil {
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
    ctx := adapter.WithFusionHeaders(r.Context(), r)

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
        Text:   textContent,
        Stream: chatReq.Stream,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

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

    tenant := "anonymous"
    if kc := middleware.GetAuthKeyConfig(r.Context()); kc != nil && kc.Name != "" {
        tenant = kc.Name
    }
    if chatReq.Stream {
        s.handleStreamChat(ctx, w, provider, &chatReq, decision, budget, start)
    } else {
        s.handleNonStreamChat(ctx, w, provider, &chatReq, decision, budget, start, tenant)
    }
}

// writeChatFailedError writes the OpenAI-path chat failure response with the
// upstream error detail surfaced (issue #91). Previously a generic "Chat
// failed" / "Stream chat failed" hid the upstream cause — e.g. a cloud 400
// "Invalid model name" left the client unable to self-diagnose a wrong model
// name. The /v1/messages path already surfaces upstream detail via
// writeMessagesError (#40); this mirrors that for /v1/chat/completions. The
// message is JSON-escaped via json.Marshal and capped so a large upstream
// body does not flood the client response. No API key material appears in
// upstream error strings (verified), so surfacing is safe.
func writeChatFailedError(w http.ResponseWriter, prefix string, err error) {
    detail := ""
    if err != nil {
        detail = err.Error()
        if len(detail) > 512 {
            detail = detail[:512]
        }
    }
    body, _ := json.Marshal(struct {
        Error struct {
            Message string `json:"message"`
            Type    string `json:"type"`
        } `json:"error"`
    }{
        Error: struct {
            Message string `json:"message"`
            Type    string `json:"type"`
        }{
            Message: prefix + ": " + detail,
            Type:    "server_error",
        },
    })
    http.Error(w, string(body), http.StatusBadGateway)
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

// chatRequestHasImage reports whether any /v1/chat/completions message carries
// an OpenAI multimodal content block. The OpenAI shape is Content as a []any of
// typed blocks: {"type":"text","text":...}, {"type":"image_url","image_url":...},
// {"type":"input_audio",...}. A plain-string Content (text-only) returns false.
// This mirrors anthropicRequestHasImage for the /v1/messages path so the
// router's text-only signal is not left blind to a multimodal payload (#120).
func chatRequestHasImage(req *adapter.ChatRequest) bool {
    if req == nil {
        return false
    }
    for _, msg := range req.Messages {
        blocks, ok := msg.Content.([]any)
        if !ok {
            continue
        }
        for _, b := range blocks {
            obj, ok := b.(map[string]any)
            if !ok {
                continue
            }
            switch obj["type"] {
            case "image_url", "input_audio", "input_audio_delta":
                return true
            }
        }
    }
    return false
}

// multimodalRouteDecision returns a forced RouteDecision for a multimodal
// /v1/chat/completions request, or nil when the request is text-only (fall
// through to the normal rule chain). Local-first: when routing.multimodal.
// local_model is set AND that model is loaded on the local fusion-mlx node,
// force LocalBackend with the request model rewritten to the vision model.
// Cloud fallback (#120): when local is unavailable but cloud_backend +
// cloud_model are set, force CloudBackend with CloudTarget=cloud_backend and
// the request model rewritten to the cloud VLM model. When neither path is
// available the request is rejected with a clear 400 naming the missing knob
// instead of a masked text-only 400-as-502. The caller (handleChatCompletions)
// writes the 400; this helper signals rejection via a sentinel decision with
// Reason=="multimodal_unconfigured" that the caller checks before forwarding.
func (s *Server) multimodalRouteDecision(req *adapter.ChatRequest) *router.RouteDecision {
    if !chatRequestHasImage(req) {
        return nil
    }
    mm := s.cfg.Config.Routing.Multimodal
    // Local-first: force the local vision model when it is actually loaded.
    if vlModel := strings.TrimSpace(mm.LocalModel); vlModel != "" {
        if s.localVisionModelLoaded(vlModel) {
            slog.Info("chat multimodal request forced to local vision model",
                "client_model", req.Model, "vision_model", vlModel)
            req.Model = vlModel
            return &router.RouteDecision{Backend: router.LocalBackend, Reason: "multimodal_local_vision"}
        }
        slog.Info("chat multimodal local vision model not loaded, trying cloud fallback",
            "vision_model", vlModel)
    }
    // Cloud fallback (#120): route to the configured cloud VLM backend+model.
    cloudBackend := strings.TrimSpace(mm.CloudBackend)
    cloudModel := strings.TrimSpace(mm.CloudModel)
    if cloudBackend != "" && cloudModel != "" {
        slog.Info("chat multimodal request routed to cloud VLM",
            "client_model", req.Model, "cloud_backend", cloudBackend, "cloud_model", cloudModel)
        req.Model = cloudModel
        return &router.RouteDecision{Backend: router.CloudBackend, Reason: "multimodal_cloud_vlm", CloudTarget: cloudBackend}
    }
    // Neither path available: signal rejection (caller writes the 400).
    return &router.RouteDecision{Backend: router.CloudBackend, Reason: "multimodal_unconfigured"}
}

// localVisionModelLoaded reports whether the given vision model id is loaded
// on the local fusion-mlx node. Returns false when no fusion-mlx provider is
// configured or the model is absent from its loaded model set. The model set
// is refreshed periodically (cmd/gateway/main.go safeGo loop) from fusion-mlx
// /v1/models, so this reflects the live loaded state, not just config.
func (s *Server) localVisionModelLoaded(model string) bool {
    mlx := s.pool.GetFusionMLX()
    if mlx == nil {
        return false
    }
    loaded := mlx.ModelSet()
    return loaded != nil && loaded[model]
}

// multimodalUnconfigured reports whether a decision is the #120 sentinel that
// means "multimodal request but no local vision model and no cloud VLM
// configured" — the caller must reject with a clear 400 rather than forward.
func multimodalUnconfigured(d *router.RouteDecision) bool {
    return d != nil && d.Reason == "multimodal_unconfigured"
}
