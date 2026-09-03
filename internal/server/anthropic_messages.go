package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    // R2 fix: use MaxBytesReader to limit request body size
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

    var antReq adapter.AnthropicRequest
    if err := json.Unmarshal(body, &antReq); err != nil {
        slog.Error("invalid json in anthropic messages request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)

    // Inject token budget so the router's P4 token-budget tier evaluates
    // instead of defaulting every /v1/messages request to cloud with
    // reason "token_budget_missing". Without this, short prompts waste cloud
    // quota and long prompts never trigger token_budget_exceeded (issue #62).
    textContent := extractAnthropicTextContent(antReq.Messages)

    // N4: PII scanning on the /v1/messages path (previously bypassed — only
    // /v1/chat/completions scanned). This is the path Claude Code uses, so a
    // deny policy MUST apply here too. Run before token counting + routing so
    // a denied request never reaches an upstream.
    if s.scanPIIOrDeny(w, textContent, antReq.Model, "/v1/messages") {
        return
    }

    inputTokens, err := s.tokEngine.CountTokens(ctx, textContent)
    if err != nil {
        slog.Error("token counting failed for anthropic messages", "error", err)
        inputTokens = len(textContent) / 4
    }
    maxTokens := antReq.MaxTokens
    budget := s.tokEngine.EstimateBudget(inputTokens, &maxTokens, antReq.Model, len(antReq.Tools) > 0, antReq.Stream)
    ctx = tokenizer.WithTokenBudget(ctx, budget)

    // RC3: reject oversized requests before the cloud call instead of letting the
    // cloud model 400 (ContextWindowExceededError) and masking it as a 502.
    if s.enforceModelContextLimit(w, r, antReq.Model, budget) {
        return
    }

    // RC1: strip built-in server-side tools (web_search/computer/bash/text_editor)
    // and malformed tool entries before forwarding. These carry a `type` field and
    // no input_schema; AnthropicTool now decodes `type` but the gateway is not a
    // server-side-tool host — forwarding a built-in entry + tool_choice makes
    // glm5.2/vLLM reject "tool_choice requires tools" -> 400 -> gateway 502 (88x in
    // the logs). A real client tool has no `type` and has input_schema; it is kept.
    // After this filter, the orphan-strip below fires naturally when ALL tools were
    // built-in, dropping the now-orphan tool_choice. Mixed (built-in + real) keeps
    // the real tools and their tool_choice.
    if len(antReq.Tools) > 0 {
        kept := antReq.Tools[:0]
        for _, t := range antReq.Tools {
            if t.Type != "" || len(t.InputSchema) == 0 {
                slog.Info("anthropic messages stripping built-in/malformed tool",
                    "type", t.Type, "name", t.Name, "model", antReq.Model)
                continue
            }
            kept = append(kept, t)
        }
        antReq.Tools = kept
    }

    // Strip an orphan tool_choice (present, but no tools). A client web-search
    // request (Claude Code) carries tool_choice:"auto" + web_search_options:{}
    // but no tools array — that is Anthropic's server-side-tool protocol.
    // AnthropicRequest has no web_search_options field so it is dropped, but
    // tool_choice is forwarded verbatim -> glm5.2/vLLM rejects "tool_choice
    // requires tools" -> 400 -> gateway 502 (issue #92). Stripping the orphan
    // lets the request degrade to plain generation instead of a hard 502. A
    // tool_choice WITH real tools (legitimate client tool-use) is preserved.
    if antReq.ToolChoice != nil && len(antReq.Tools) == 0 {
        slog.Info("anthropic messages stripping orphan tool_choice (no tools)", "model", antReq.Model)
        antReq.ToolChoice = nil
    }

    // F5 fix: /v1/messages bypassed the per-key model allowlist (every other
    // /v1/* inference handler gates on CheckModelAllowlist: 646/1227/1346/2024).
    // A key configured with allowed_models could send ANY model name through
    // /v1/messages and reach the upstream — the Anthropic endpoint is exactly
    // the path Claude Code uses. Check the ORIGINAL requested model (before any
    // multimodal rewrite) since that is the model the caller is authorized for;
    // the forced local vision model is an internal gateway override, not a
    // tenant-chosen model, and must not be filtered out.
    if !middleware.CheckModelAllowlist(r, antReq.Model) {
        slog.Warn("anthropic messages model not allowed for this key", "model", antReq.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    // Multimodal guard (RC4, unified with /v1/chat/completions): an image/audio
    // content block is invisible to the router's text-only signal (RouteRequest
    // carries Model/Text/Stream), so without this guard a multimodal payload is
    // mapped to the text-only cloud model (e.g. glm5.2) and rejected upstream
    // with 400 multimodal_not_supported -> gateway 502. The prior guard forced
    // local-only and 400'd when no local_model was set — even when a cloud VLM
    // fallback was configured, leaking image requests to a text-only cloud.
    // Now both endpoints share multimodalDecisionFor: local vision model if
    // loaded, else cloud VLM fallback, else a clear 400. Runs before Decide so
    // the rule chain cannot divert the payload to a text-only backend.
    var decision *router.RouteDecision
    if anthropicRequestHasImage(antReq.Messages) {
        mmDecision, resolved := s.multimodalDecisionFor(antReq.Model)
        if multimodalUnconfigured(mmDecision) {
            slog.Warn("anthropic multimodal request rejected: no local vision model loaded and no cloud VLM configured", "model", antReq.Model)
            http.Error(w, `{"error":{"message":"multimodal requests require routing.multimodal.local_model (loaded) or routing.multimodal.cloud_backend + cloud_model to be configured; no vision model is available","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if resolved != "" {
            antReq.Model = resolved
        }
        decision = mmDecision
    } else {
        decision = s.router.Decide(ctx, &router.RouteRequest{Model: antReq.Model, Text: textContent, Stream: antReq.Stream})
    }
    slog.Info("anthropic messages route decision", "model", antReq.Model, "backend", string(decision.Backend), "reason", decision.Reason, "input_tokens", inputTokens)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    var provider adapter.Provider
    if decision.Backend == router.LocalBackend {
        provider, _ = s.pool.Get("fusion-mlx")
    } else {
        provider = s.resolveCloudProvider(decision, nil, w)
        if provider == nil { return }
        // Apply model alias mapping (e.g. claude-opus-4-7 -> glm5.2) before
        // forwarding. Without this, SDK-supplied aliases are sent raw to the
        // cloud backend which rejects them with 400 -> 502 ("response stopped
        // arriving" in claude code). resolveCloudProvider cannot do this for
        // the anthropic path because it mutates *ChatRequest, not
        // *AnthropicRequest.
        if mapped := s.applyCloudModelMapping(antReq.Model, provider.Name()); mapped != antReq.Model {
            antReq.Model = mapped
        }
    }
    if provider == nil {
        http.Error(w, `{"error":{"message":"No provider available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    // #102 ADR-001 sub-task 3: opt-in local wait-queue, same gate as
    // /v1/chat/completions. LocalQueue() is nil unless mode=local +
    // queue_enabled, so hybrid/cloud is untouched. 429 on queue_timeout.
    if decision.Backend == router.LocalBackend {
        // #159: 3-tier admission class (tenant Tier tag + coarse intent).
        tier := router.TierForRequest(coarseIntent{stream: antReq.Stream, model: antReq.Model}, tenantTierFromContext(ctx))
        if release, err := s.acquireLocalSlot(ctx, tier); err != nil {
            slog.Warn("anthropic local slot queue rejected request", "reason", err.Error(), "model", antReq.Model)
            writeQueue429(w, err)
            return
        } else {
            defer release()
        }
    }

    // #102 ADR-001 sub-task 4: register in-flight task for cancel endpoint
    // (mirror /v1/chat/completions). task-id = X-Request-ID; WithCancel wraps
    // ctx so registry.Cancel() signals the stream; slot released by the
    // existing defer above (no double-release). Release entry on return.
    if taskID := taskIDFromContext(ctx); taskID != "" {
        streamCtx, cancel := context.WithCancel(ctx)
        // B12: bind the enqueuing auth-key name (mirror /v1/chat/completions).
        owner := ""
        if kc := middleware.GetAuthKeyConfig(ctx); kc != nil {
            owner = kc.Name
        }
        s.taskRegistry.Register(taskID, owner, cancel)
        defer s.taskRegistry.Release(taskID)
        ctx = streamCtx
    }

    // H2 fix: the cloud provider may be wrapped by cloudTrackingProvider
    // (decorator that does NOT redeclare MessagesProvider). Resolve the
    // MessagesProvider assertion through the Unwrap chain so bedrock/vertex/
    // foundry's native Anthropic passthrough is used, not the lossy OpenAI
    // conversion path. Without this, Claude tool_use/thinking events are
    // silently dropped on every cloud /v1/messages request (audit H2).
    msgProv := resolveMessagesProvider(provider)
    if msgProv != nil {
        if antReq.Stream {
            // R10 (audit): global concurrent-stream cap (429 when full).
            ok, releaseSlot := s.acquireStreamSlot()
            if !ok {
                slog.Warn("anthropic stream rejected, concurrent stream cap reached",
                    "model", antReq.Model, "max", s.cfg.Config.Routing.Stream.MaxConcurrentStreams)
                w.Header().Set("Content-Type", "application/json")
                w.Header().Set("Retry-After", "2")
                w.WriteHeader(http.StatusTooManyRequests)
                fmt.Fprintf(w, `{"error":{"message":"concurrent stream limit reached, retry shortly","type":"rate_limit_error"}}`)
                return
            }
            defer releaseSlot()
            // R9 (audit): per-request duration ceiling (default 600s).
            streamCtx, cancelDeadline := s.streamDeadline(ctx)
            defer cancelDeadline()
            s.handleStreamAnthropicMessages(streamCtx, w, msgProv, &antReq)
        } else {
            s.handleNonStreamAnthropicMessages(ctx, w, msgProv, &antReq)
        }
    } else {
        chatReq := adapter.AnthropicToOpenAIChatRequest(&antReq)
        chatReq.Stream = antReq.Stream
        start := time.Now()
        budget := tokenizer.TokenBudget{InputTokens: 0, TotalBudget: antReq.MaxTokens}
        tenant := "anonymous"
        if kc := middleware.GetAuthKeyConfig(r.Context()); kc != nil && kc.Name != "" {
            tenant = kc.Name
        }
        if antReq.Stream {
            // R10 (audit): global concurrent-stream cap (429 when full).
            ok, releaseSlot := s.acquireStreamSlot()
            if !ok {
                slog.Warn("anthropic stream rejected, concurrent stream cap reached",
                    "model", antReq.Model, "max", s.cfg.Config.Routing.Stream.MaxConcurrentStreams)
                w.Header().Set("Content-Type", "application/json")
                w.Header().Set("Retry-After", "2")
                w.WriteHeader(http.StatusTooManyRequests)
                fmt.Fprintf(w, `{"error":{"message":"concurrent stream limit reached, retry shortly","type":"rate_limit_error"}}`)
                return
            }
            defer releaseSlot()
            // R9 (audit): per-request duration ceiling (default 600s).
            streamCtx, cancelDeadline := s.streamDeadline(ctx)
            defer cancelDeadline()
            s.handleStreamChat(streamCtx, w, provider, chatReq, decision, budget, start)
        } else {
            s.handleNonStreamChat(ctx, w, provider, chatReq, decision, budget, start, tenant)
        }
    }
}

// writeMessagesError surfaces an upstream error to the client preserving the
// upstream HTTP status code and request-id (issue #40) so fusion-code's error
// bridge (isApiErrorLike) can identify failures. Non-MessagesHTTPError errors
// fall back to 502 as before.
func (s *Server) writeMessagesError(w http.ResponseWriter, err error) {
    status := http.StatusBadGateway
    body := fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error())
    if httpErr, ok := err.(*adapter.MessagesHTTPError); ok {
        status = httpErr.StatusCode
        if httpErr.Body != "" {
            body = httpErr.Body
        } else {
            body = fmt.Sprintf(`{"error":{"message":"upstream status %d","type":"api_error"}}`, httpErr.StatusCode)
        }
        if httpErr.RequestID != "" {
            w.Header().Set("x-request-id", httpErr.RequestID)
        }
    } else if isContextLengthError(err) {
        // RC3: a plain (non-MessagesHTTPError) upstream context-window-exceeded error
        // would default to 502. Surface it honestly as 400 so the client can shrink
        // the prompt instead of retrying a request that will always fail.
        status = http.StatusBadRequest
        body = fmt.Sprintf(`{"error":{"message":"upstream context length exceeded: %s","type":"context_length_exceeded"}}`, err.Error())
    }
    slog.Error("anthropic messages upstream error", "error", err, "status", status)
    http.Error(w, body, status)
}

// resolveMessagesProvider resolves a MessagesProvider through a chain of
// Unwrap()-style decorators (audit H2). cloudTrackingProvider wraps every cloud
// provider but does not redeclare MessagesProvider, so a direct type assertion
// on the wrapped value fails for bedrock/vertex/foundry and silently routes to
// the lossy OpenAI conversion path. Walk the Unwrap chain; the first provider
// that satisfies MessagesProvider wins. A guard against runaway chains bounds
// the walk (paranoia for a misbehaving decorator that unwraps to itself).
func resolveMessagesProvider(p adapter.Provider) adapter.MessagesProvider {
    const maxDepth = 8
    for i := 0; i < maxDepth; i++ {
        if mp, ok := p.(adapter.MessagesProvider); ok {
            return mp
        }
        unwrapper, ok := p.(interface{ Unwrap() adapter.Provider })
        if !ok {
            return nil
        }
        next := unwrapper.Unwrap()
        if next == nil || next == p {
            return nil
        }
        p = next
    }
    return nil
}

// nonStreamClientCanceled returns true and logs INFO when a non-stream
// /v1/messages error is a client cancel rather than an upstream fault (issue
// #94). The non-stream path has two error returns — the connection phase
// (msgFn/StreamMessages) and the aggregate (AggregateAnthropicStreamEvents) —
// and a client cancel can surface at either. The parent-ctx check (ctx.Err()
// != nil) is the deterministic disambiguator: a client cancel cancels the
// parent ctx; the idle watchdog cancels only the child wdCtx while the parent
// stays alive (ctx.Err()==nil → still writeMessagesError, a watchdog trip IS
// a fault). errors.Is(err, context.Canceled) is ambiguous because the
// watchdog and retry wrapper both wrap context.Canceled. phase labels the log
// so the two return sites are distinguishable in the logs. The handler must
// return immediately on true — headers are not committed yet on the non-stream
// path, so a silent return writes nothing to the (already-abandoned) client.
func (s *Server) nonStreamClientCanceled(ctx context.Context, w http.ResponseWriter, err error, phase string) bool {
    if ctx.Err() == nil {
        return false
    }
    slog.Info("anthropic messages non-stream client canceled", "phase", phase, "error", err)
    return true
}

func (s *Server) handleNonStreamAnthropicMessages(ctx context.Context, w http.ResponseWriter, p adapter.MessagesProvider, req *adapter.AnthropicRequest) {
    // Internal stream + aggregate: reasoning upstreams (glm5.2 via LiteLLM)
    // withhold non-stream response headers until full generation completes,
    // tripping Client.Timeout / client-cancel 502s. Stream path has a 2s TTFB,
    // so we stream upstream and aggregate events into a non-stream response.
    // We force stream=true on a local copy so the original request struct is
    // untouched (the same req may be inspected elsewhere).
    streamReq := *req
    streamReq.Stream = true
    // wdCtx is a child of the request ctx so the idle watchdog can cancel the
    // upstream read (unblocking body.Read via the Go transport context watcher)
    // without being mistaken for a real client cancel — the parent ctx stays
    // intact and writeMessagesError can still write a clean status (headers
    // are not committed yet on the non-stream path). See issue #69.
    wdCtx, wdCancel := context.WithCancel(ctx)
    defer wdCancel()
    msgFn := func(ctx context.Context, r *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        return p.StreamMessages(ctx, r)
    }
    msgFn = middleware.RetryStreamMessages(s.cfg.Config.Routing.Retry, msgFn)
    ch, err := msgFn(wdCtx, &streamReq)
    if err != nil {
        // A client cancel can surface here at the connection phase (msgFn
        // returns ctx.Err() via the retry wrapper's <-ctx.Done() branch)
        // before any event is aggregated. Same #94 treatment as the aggregate
        // error below: a cancel is a request-level signal, not an upstream
        // fault — don't log ERROR or write 502 to a dead pipe.
        if s.nonStreamClientCanceled(ctx, w, err, "connection phase") {
            return
        }
        s.writeMessagesError(w, err)
        return
    }
    streamCfg := s.cfg.Config.Routing.Stream
    resp, err := adapter.AggregateAnthropicStreamEvents(wdCtx, ch, streamCfg.IdleTimeout)
    if err != nil {
        // Client canceled the non-stream request (parent ctx gone, not a
        // watchdog trip — the idle watchdog cancels wdCtx only while the
        // parent stays alive, see issue #69). A cancel is a request-level
        // signal, not an upstream fault (issue #46/#90 class, non-stream
        // variant). Don't log ERROR or write 502 to a dead pipe; headers
        // are not committed yet on the non-stream path. errors.Is(err,
        // context.Canceled) is ambiguous here because the idle watchdog
        // branch (and the retry wrapper's <-ctx.Done() branch) also wrap
        // context.Canceled — the parent-ctx check is deterministic.
        if s.nonStreamClientCanceled(ctx, w, err, "aggregate") {
            return
        }
        s.writeMessagesError(w, err)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStreamAnthropicMessages(ctx context.Context, w http.ResponseWriter, p adapter.MessagesProvider, req *adapter.AnthropicRequest) {
    // wdCtx is a child of the request ctx. The idle watchdog cancels wdCtx to
    // unblock the upstream body.Read (via the Go transport context watcher)
    // when the upstream stalls mid-stream (issue #69: litellm/glm5.2 stops
    // pushing delta without closing the connection). Cancelling the child ctx
    // is distinct from a real client cancel (parent ctx), so the terminal
    // handling below can synthesize a clean message_stop for a stalled stream
    // while still suppressing it for a true client disconnect (issue #46).
    wdCtx, wdCancel := context.WithCancel(ctx)
    defer wdCancel()
    msgFn := func(ctx context.Context, r *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        return p.StreamMessages(ctx, r)
    }
    msgFn = middleware.RetryStreamMessages(s.cfg.Config.Routing.Retry, msgFn)
    ch, err := msgFn(wdCtx, req)
    if err != nil {
        s.writeMessagesError(w, err)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
    flusher, _ := w.(http.Flusher)

    // sawMessageStop tracks whether the upstream already emitted a
    // message_stop. We must not append a second one: the Anthropic SDK
    // finalizes the message on the first message_stop, and a duplicate
    // (especially a malformed data:{} with no "type") confuses the client
    // ("Content block not found" / stream-ended errors, issue #46).
    sawMessageStop := false
    // openBlocks tracks content-block indices that have started but not yet
    // received their matching content_block_stop. The Anthropic SDK requires
    // every content_block_start to be closed by content_block_stop before the
    // terminal message_stop; an open block at stream end yields "Content block
    // not found" (issue #71). Both forward loops maintain this set so the
    // synth path below can close any block the upstream left open.
    openBlocks := make(map[int]bool)
    streamCfg := s.cfg.Config.Routing.Stream
    // writeSSE emits one SSE frame to the client and flushes, returning the
    // write error. The forward loops used to discard fmt.Fprintf errors, so a
    // broken pipe (client gone mid-response) was silently dropped: the loop
    // kept spinning until the cancelled ctx fired the "client canceled"
    // branch, making a gateway-side write failure indistinguishable from a
    // CC-side disconnect in the "Connection lost mid-response" logs (issue
    // #79). Capturing the error lets the loop log "client write failed"
    // distinctly and stop writing instead of masking the real cause.
    writeSSE := func(format string, args ...any) error {
        if _, err := fmt.Fprintf(w, format, args...); err != nil {
            return err
        }
        if flusher != nil {
            flusher.Flush()
        }
        return nil
    }
    // emitAnthropicEvent (N3 audit): emit an upstream Anthropic event. When the
    // provider carried the verbatim upstream payload (Raw), write it directly —
    // skipping the per-frame json.Marshal the audit found burning serialization
    // at concurrency AND preserving fields the struct's omitempty drops.
    //
    // The re-marshal path was the root cause of "Content block not found" on
    // thinking streams (glm5.2 via LiteLLM). An upstream thinking
    // content_block_start carries empty "thinking":"" and "signature":"" keys;
    // AnthropicContentBlock tags both with omitempty, so json.Marshal dropped
    // them, emitting {"type":"thinking"} with neither key. The Anthropic SDK
    // requires a thinking block to carry both keys, so the malformed block
    // could not be finalized and Claude Code surfaced "Content block not found"
    // after the thinking deltas drained (the "Thought for N s" then crash
    // symptom). Raw passthrough preserves the empty keys verbatim.
    //
    // The re-marshal path's only purpose was injecting a missing "index":0
    // (issue #46); emitting raw bytes that omit index would regress that fix.
    // So block-scoped events still marshal ONLY when Raw is absent or does not
    // already carry an "index" field. Upstreams that send index (LiteLLM does)
    // pass through faithfully; upstreams that omit it still get the injection.
    // Events synthesized in-process (Raw nil) also marshal. Semantically
    // identical output for pass-through events; raw is more faithful (preserves
    // unknown upstream fields and empty-value keys the struct drops).
    emitAnthropicEvent := func(event adapter.AnthropicStreamEvent) error {
        data := selectAnthropicEventData(event)
        return writeSSE("event: %s\ndata: %s\n\n", event.Type, data)
    }
    writeFailed := false
    // Per-stream timing instrumentation (issue #81). "The response stopped
    // arriving" is a Claude Code internal judgment that an upstream stream
    // stopped producing deltas; the gateway previously logged only the
    // consequence (client canceled) when CC gave up, with no per-stream timing
    // to tell whether the upstream actually stalled or CC cancelled a live
    // stream. These counters feed one INFO summary line on every exit path so
    // the next recurrence is diagnosable: last_event_idle + pings + end_reason
    // pin whether the stall was upstream (H-B) or CC-side (H-A). Pure
    // observation — no behavior change.
    streamStart := time.Now()
    var firstEventAt time.Time
    var lastEventAt time.Time
    var lastEventType string
    eventCount := 0
    deltaCount := 0
    pingCount := 0
    endReason := "ch_closed_no_stop"
    streamSummary := func() {
        dur := time.Since(streamStart)
        firstTTFB := time.Duration(0)
        if !firstEventAt.IsZero() {
            firstTTFB = firstEventAt.Sub(streamStart)
        }
        lastIdle := time.Duration(0)
        if !lastEventAt.IsZero() {
            lastIdle = time.Since(lastEventAt)
        }
        slog.Info("anthropic stream summary",
            "model", req.Model,
            "duration", dur,
            "events", eventCount,
            "deltas", deltaCount,
            "pings", pingCount,
            "first_event_ttfb", firstTTFB,
            "last_event_idle", lastIdle,
            "last_event_type", lastEventType,
            "end_reason", endReason)
    }
    // closeOpenBlocks emits a content_block_stop for every index still open,
    // in ascending order, then flushes. Returns the closed indices (nil if
    // none). The Anthropic SDK requires every content_block_start to be
    // matched by content_block_stop before the terminal message_stop — an
    // open block at message_stop yields "Content block not found" (issue #71,
    // #75). Called both inline when an upstream message_stop arrives with
    // blocks still open (the upstream sent a malformed terminal without
    // closing its blocks) and on the post-loop synth path.
    closeOpenBlocks := func() []int {
        if len(openBlocks) == 0 {
            return nil
        }
        indices := make([]int, 0, len(openBlocks))
        for idx := range openBlocks {
            indices = append(indices, idx)
        }
        for i := 0; i < len(indices); i++ {
            for j := i + 1; j < len(indices); j++ {
                if indices[j] < indices[i] {
                    indices[i], indices[j] = indices[j], indices[i]
                }
            }
        }
        for _, idx := range indices {
            delete(openBlocks, idx)
            if err := writeSSE("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", idx); err != nil {
                writeFailed = true
                return indices
            }
        }
        return indices
    }
    if streamCfg.KeepaliveInterval <= 0 {
        // Backward-compat: no keepalive/watchdog configured, pure blocking
        // forward loop (original behavior). Upstream stalls will block until
        // the client times out — only use when the hardened path is disabled.
        for event := range ch {
            eventCount++
            lastEventType = event.Type
            lastEventAt = time.Now()
            if firstEventAt.IsZero() {
                firstEventAt = lastEventAt
            }
            if event.Type == "content_block_delta" {
                deltaCount++
            }
            if event.Type == "message_stop" {
                sawMessageStop = true
                if closed := closeOpenBlocks(); closed != nil {
                    slog.Warn("anthropic upstream message_stop with open content blocks, closing before terminal",
                        "open_blocks", closed)
                }
            } else if event.Type == "content_block_start" {
                openBlocks[event.Index] = true
            } else if event.Type == "content_block_stop" {
                delete(openBlocks, event.Index)
            }
            if err := emitAnthropicEvent(event); err != nil {
                slog.Warn("anthropic stream client write failed", "error", err)
                writeFailed = true
                endReason = "write_failed"
                break
            }
        }
    } else {
        // Hardened forward loop: a single ticker serves two roles. Every tick
        // it first checks the idle watchdog — if no upstream event arrived for
        // IdleTimeout, the upstream is dead (not just slow); cancel wdCtx to
        // unblock body.Read and end the loop so a clean message_stop is
        // synthesized below. Otherwise emit an Anthropic-native ping event so
        // the client keeps seeing bytes and does not time out a slow-but-live
        // upstream. Watchdog granularity equals the keepalive interval.
        ticker := time.NewTicker(streamCfg.KeepaliveInterval)
        defer ticker.Stop()
        // Assign (not declare) to the outer lastEventAt so streamSummary reads
        // the real last-event time. A `:=` here shadowed the outer var, leaving
        // it zero — every summary printed last_event_idle=0s and the #81
        // upstream-stall discriminator (H-A CC cancel vs H-B stall) was useless
        // (issue #88). Initialize to loop start; the first event overwrites it.
        lastEventAt = time.Now()
        watchdogTripped := false
        done := false
        for !done {
            select {
            case event, ok := <-ch:
                if !ok {
                    done = true
                    break
                }
                eventCount++
                lastEventType = event.Type
                lastEventAt = time.Now()
                if firstEventAt.IsZero() {
                    firstEventAt = lastEventAt
                }
                if event.Type == "content_block_delta" {
                    deltaCount++
                }
                if event.Type == "message_stop" {
                    sawMessageStop = true
                    if closed := closeOpenBlocks(); closed != nil {
                        slog.Warn("anthropic upstream message_stop with open content blocks, closing before terminal",
                            "open_blocks", closed)
                    }
                } else if event.Type == "content_block_start" {
                    openBlocks[event.Index] = true
                } else if event.Type == "content_block_stop" {
                    delete(openBlocks, event.Index)
                }
                if err := emitAnthropicEvent(event); err != nil {
                    slog.Warn("anthropic stream client write failed", "error", err)
                    writeFailed = true
                    endReason = "write_failed"
                    done = true
                }
            case <-ticker.C:
                if !watchdogTripped && time.Since(lastEventAt) >= streamCfg.IdleTimeout {
                    slog.Warn("anthropic stream idle watchdog tripped, cancelling upstream",
                        "idle", time.Since(lastEventAt), "threshold", streamCfg.IdleTimeout)
                    watchdogTripped = true
                    endReason = "watchdog_tripped"
                    wdCancel()
                    done = true
                } else if !watchdogTripped {
                    if err := writeSSE("event: ping\ndata: {\"type\":\"ping\"}\n\n"); err != nil {
                        slog.Warn("anthropic stream client write failed", "error", err)
                        writeFailed = true
                        endReason = "write_failed"
                        done = true
                    } else {
                        pingCount++
                    }
                }
            case <-ctx.Done():
                endReason = "client_canceled"
                done = true
            }
        }
    }
    // Client cancelled mid-stream (context canceled): the upstream goroutine
    // closed the channel early, possibly with content blocks still open.
    // Emitting a terminal message_stop now would hand the SDK an unmatched
    // block ("Content block not found"). The client already gave up, so just
    // stop writing — do NOT synthesize a closing event. We check the parent
    // ctx (not wdCtx): a watchdog trip cancels only the child and must still
    // synthesize; only a real client disconnect suppresses (issue #46).
    if ctx.Err() != nil {
        if writeFailed {
            // The loop already logged a client write failure — the pipe is
            // dead and the client is gone. Synthesizing a terminal to a dead
            // pipe is pointless and risks a second write error. The cancelled
            // ctx is just the downstream consequence (issue #79); do not also
            // log a client cancel (conflation blind spot).
            streamSummary()
            return
        }
        // Client canceled but the write pipe is still alive (writeFailed is
        // false): the cancel is a request-level signal (CC timeout/retry),
        // NOT a dead socket — the client may still drain its buffer to
        // finalize. Any content block the upstream left OPEN must be closed
        // FIRST, then a well-formed terminal emitted, so the Anthropic SDK
        // finalizes cleanly. The original #46 suppression assumed
        // cancel==dead-pipe and skipped ALL terminal events, leaving open
        // blocks the SDK could not finalize → "API Error: Content block not
        // found" (issue #90). stop_reason is max_tokens (truncation, #77):
        // cancel打断上游未完成, end_turn would falsely claim completion.
        if closed := closeOpenBlocks(); closed != nil {
            slog.Warn("anthropic stream client canceled with open content blocks, closing before terminal", "open_blocks", closed)
        }
        if !sawMessageStop {
            slog.Warn("anthropic stream client canceled before message_stop, synthesizing terminal", "stop_reason", "max_tokens", "error", ctx.Err())
            writeSSE("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{}}\n\n")
            writeSSE("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
        }
        streamSummary()
        return
    }
    if writeFailed {
        // The client pipe broke mid-stream (logged by the loop). The client is
        // already gone, so synthesizing a terminal to a dead pipe is pointless
        // and risks a second write error. Stop here (issue #79).
        streamSummary()
        return
    }
    // Upstream closed the channel without a message_stop (e.g. error/truncation
    // or the idle watchdog cancelled a stalled stream): synthesize a
    // well-formed terminal sequence so the SDK can finalize cleanly. Any
    // content block the upstream left open must be closed FIRST — the SDK
    // requires content_block_stop for every content_block_start before
    // message_stop, else "Content block not found" (issue #71). Emit
    // content_block_stop for each open index (ascending), then a message_delta
    // carrying stop_reason, then the terminal message_stop.
    if !sawMessageStop {
        if closed := closeOpenBlocks(); closed != nil {
            slog.Warn("anthropic stream ended with open content blocks, synthesizing content_block_stop before terminal event", "open_blocks", closed)
        }
        slog.Warn("anthropic stream ended without message_stop, synthesizing terminal event", "stop_reason", "max_tokens")
        // stop_reason is "max_tokens" (truncation), not "end_turn" (complete).
        // The upstream dropped mid-generation (EOF, no message_stop) so the
        // output is truncated. "end_turn" falsely claims the model finished;
        // clients that received partial text then see a false-complete signal
        // and surface "The response stopped arriving / incomplete". "max_tokens"
        // signals truncation so the client retries or continues (issue #77).
        writeSSE("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{}}\n\n")
        writeSSE("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
        // endReason stays the default "ch_closed_no_stop" (synth path).
    } else {
        endReason = "clean"
    }
    streamSummary()
}

// extractAnthropicTextContent concatenates text blocks from Anthropic-format
// messages for token counting. Mirrors extractTextContent for the /v1/messages
// path so the router receives a real token budget instead of routing every
// request to cloud with reason "token_budget_missing" (issue #62).
func extractAnthropicTextContent(messages []adapter.AnthropicMessage) string {
    var sb strings.Builder
    for _, msg := range messages {
        for _, block := range msg.Content {
            if block.Type == "text" && block.Text != "" {
                sb.WriteString(block.Text)
                sb.WriteByte(' ')
            }
        }
    }
    return sb.String()
}

// selectAnthropicEventData chooses the bytes to emit for one upstream Anthropic
// stream event (N3 audit + thinking-block fix).
//
// When the provider carried the verbatim upstream payload (Raw), return it
// directly — skipping the per-frame json.Marshal the audit found burning
// serialization at concurrency AND preserving fields the struct's omitempty
// drops. This is the root-cause fix for "Content block not found" on thinking
// streams (glm5.2 via LiteLLM): an upstream thinking content_block_start
// carries empty "thinking":"" and "signature":"" keys; AnthropicContentBlock
// tags both with omitempty, so json.Marshal dropped them, emitting
// {"type":"thinking"} with neither key. The Anthropic SDK requires a thinking
// block to carry both keys, so the malformed block could not be finalized and
// Claude Code surfaced "Content block not found" after the thinking deltas
// drained (the "Thought for N s" then crash symptom). Raw passthrough preserves
// the empty keys verbatim.
//
// The re-marshal path's only purpose was injecting a missing "index":0 (issue
// #46); emitting raw bytes that omit index would regress that fix. So
// block-scoped events still marshal ONLY when Raw is absent or does not already
// carry an "index" field. Upstreams that send index (LiteLLM does) pass through
// faithfully; upstreams that omit it still get the injection. Events
// synthesized in-process (Raw nil) also marshal. Semantically identical output
// for pass-through events; raw is more faithful (preserves unknown upstream
// fields and empty-value keys the struct drops).
func selectAnthropicEventData(event adapter.AnthropicStreamEvent) []byte {
    data := event.Raw
    blockScoped := event.Type == "content_block_start" ||
        event.Type == "content_block_delta" ||
        event.Type == "content_block_stop"
    if len(data) == 0 || (blockScoped && !strings.Contains(string(data), `"index"`)) {
        marshaled, err := json.Marshal(event)
        if err != nil {
            slog.Error("anthropic stream event marshal failed", "error", err, "type", event.Type)
            return nil
        }
        data = marshaled
    }
    return data
}

// anthropicRequestHasImage reports whether any message carries a non-text
// content block that the router's text-only signal cannot see (RouteRequest
// carries only Model/Text/Stream). A multimodal payload routed to a text-only
// cloud model (e.g. glm5.2 via model_mapping) is rejected upstream with 400
// multimodal_not_supported -> gateway 502 (Claude Code screenshot 502). The
// handler uses this to force the request to a local vision model before Decide.
// Only true multimodal block types count: "image", "audio", "document".
// "thinking"/"tool_use"/"tool_result"/"redacted_thinking" are NOT multimodal
// (they carry text/structured text), so a thinking-mode conversation history
// must not be misrouted to a vision model (issue #113 over-broad guard).
func anthropicRequestHasImage(messages []adapter.AnthropicMessage) bool {
    for _, msg := range messages {
        for _, block := range msg.Content {
            switch block.Type {
            case "image", "audio", "document":
                return true
            }
        }
    }
    return false
}
