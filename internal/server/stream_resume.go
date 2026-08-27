package server

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// handleStreamChatResumable is the #116 local-MLX resumable variant of
// handleStreamChat, engaged when routing.stream.resume_enabled and the route
// decision picked the local backend. Differences from the plain path:
//
//  1. The upstream pump runs on a ctx DECOUPLED from the client request ctx
//     (liveCtx). A client disconnect no longer kills the pump — the buffer
//     keeps growing so a reconnect (Last-Event-ID) can resume. The local slot
//     is held for the whole generation (the pump goroutine's sole release),
//     which is the cost of true mid-stream resumability.
//  2. Every SSE event is written with an `id: <sid>:<seq>` line and appended
//     to the stream's rolling buffer (stream_id = the X-Request-ID).
//  3. On a client write failure the loop STOPS writing the client but keeps
//     draining the channel into the buffer until the pump closes — so the
//     buffered tail survives the disconnect.
//
// The idle watchdog still guards the pump via liveCtx (a stalled upstream is
// canceled so the buffer finalizes and the slot frees). Cloud/first-party
// paths never reach here — only LocalBackend.
func (s *Server) handleStreamChatResumable(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time) {
    sid := taskIDFromContext(ctx)
    if sid == "" || s.streamBuffers == nil {
        // No stream_id (RequestID middleware absent) or resume disabled at
        // runtime — fall back to the non-resumable path rather than losing
        // the stream. This should not happen given the New() gate, but guard.
        slog.Warn("resumable stream: no stream_id or buffer store, falling back to plain stream", "sid", sid)
        s.handleStreamChat(ctx, w, provider, req, decision, budget, start)
        return
    }

    // liveCtx is decoupled from the client ctx: the pump survives a client
    // disconnect. The idle watchdog cancels liveCtx (not ctx) on a stalled
    // upstream so the pump unblocks and the buffer finalizes. liveCancel is
    // called on pump-close below. liveCtx copies the inbound fusion-headers
    // map + X-Request-ID from ctx so the pump's outbound upstream request still
    // propagates X-Request-ID / auth headers as in the plain path.
    liveCtx := context.Background()
    if rid := middleware.RequestIDFromContext(ctx); rid != "" {
        liveCtx = middleware.InjectRequestID(liveCtx, rid)
    }
    if fh := adapter.FusionHeadersFromContext(ctx); fh != nil {
        liveCtx = adapter.WithFusionHeadersMap(liveCtx, fh)
    }
    liveCtx, liveCancel := context.WithCancel(liveCtx)
    defer liveCancel()

    buf := s.streamBuffers.Open(sid)
    ch, err := provider.StreamChat(liveCtx, req)
    if err != nil {
        // RR4 slot-full diversion mirrors the plain path.
        if errors.Is(err, adapter.ErrLocalSlotFull) && decision.Backend == router.LocalBackend {
            slog.Info("RR4 local slot full, diverting resumable stream to cloud",
                "model", req.Model, "reason", "max_concurrent reached")
            s.streamBuffers.Release(sid)
            cloudDecision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "rr4_slot_full_redirect"}
            cloudProvider := s.resolveCloudProvider(cloudDecision, req, nil)
            if cloudProvider != nil {
                // Cloud path is NOT resumable — plain stream.
                s.handleStreamChat(ctx, w, cloudProvider, req, cloudDecision, budget, start)
                return
            }
            slog.Error("RR4 resumable cloud diversion failed: no cloud provider", "model", req.Model)
            s.streamBuffers.Release(sid)
            writeChatFailedError(w, "Stream chat failed", err)
            return
        }
        slog.Error("resumable stream chat failed", "provider", provider.Name(), "error", err)
        s.streamBuffers.Release(sid)
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
    // Announce the resume cursor namespace so the client knows where to send
    // Last-Event-ID. The per-event id lines carry <sid>:<seq>.
    w.Header().Set("X-Fusion-Stream-ID", sid)
    flusher, canFlush := w.(http.Flusher)

    streamCfg := s.cfg.Config.Routing.Stream
    streamStart := time.Now()
    var firstChunkAt time.Time
    var lastChunkAt time.Time
    chunkCount := 0
    outputTokens := 0
    var lastChunkID string
    var lastChunkModel string
    var lastChunkCreated int64
    includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
    endReason := "ch_closed_no_done"
    clientGone := false

    // idleWatchCtx is liveCtx scoped to the watchdog: when idle exceeds
    // IdleTimeout, cancel liveCtx to unblock the pump's body.Read. Mirrors the
    // plain path's wdCancel but on the decoupled ctx.
    watchdogTripped := false
    var watchdogCancel context.CancelFunc
    if streamCfg.IdleTimeout > 0 {
        wc, wcCancel := context.WithCancel(context.Background())
        watchdogCancel = wcCancel
        defer watchdogCancel()
        tickInterval := streamCfg.KeepaliveInterval
        if tickInterval <= 0 {
            tickInterval = 5 * time.Second
        }
        safego.Go("resumable_stream_idle_watchdog", func() {
            ticker := time.NewTicker(tickInterval)
            defer ticker.Stop()
            lastEvent := time.Now()
            for {
                select {
                case <-wc.Done():
                    return
                case <-ticker.C:
                    // lastChunkAt is written by the single consumer goroutine
                    // below; the watchdog only reads. A stale read at most
                    // delays a trip by one tick.
                    idle := time.Since(lastEvent)
                    if chunkCount == 0 {
                        idle = time.Since(streamStart)
                    }
                    if !watchdogTripped && idle >= streamCfg.IdleTimeout {
                        slog.Warn("resumable stream idle watchdog tripped",
                            "sid", sid, "model", req.Model, "idle", idle, "threshold", streamCfg.IdleTimeout)
                        watchdogTripped = true
                        endReason = "watchdog_tripped"
                        liveCancel()
                        return
                    }
                    lastEvent = time.Now()
                }
            }
        })
    }

    // writeFrameClient writes one buffered frame to the client, flushing. The
    // frame already carries the `id: <sid>:<seq>` line (built by Append).
    // Returns false (and sets clientGone) on write failure — the caller keeps
    // buffering but stops calling this.
    writeFrameClient := func(frame []byte) bool {
        if clientGone {
            return false
        }
        if _, err := w.Write(frame); err != nil {
            slog.Warn("resumable stream client write failed", "sid", sid, "error", err)
            clientGone = true
            return false
        }
        if canFlush {
            flusher.Flush()
        }
        return true
    }

    // keepalivePing writes an SSE comment keepalive — client ignores, bytes keep
    // flowing so a slow-but-live upstream is not timed out. No id line (SSE
    // comments do not advance Last-Event-ID). Returns false on write failure.
    keepalivePing := func() bool {
        if clientGone {
            return true
        }
        if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
            slog.Warn("resumable stream keepalive write failed", "sid", sid, "error", err)
            clientGone = true
            return false
        }
        if canFlush {
            flusher.Flush()
        }
        return true
    }

    // Consumer loop: single reader of ch. Buffers every event (Append builds the
    // frame + assigns seq), then writes the same frame to the client. On a
    // client write failure the loop STOPS writing the client but keeps draining
    // the channel into the buffer until the pump closes — the buffered tail
    // survives the disconnect. Channel close finalizes the buffer.
    for chunk := range ch {
        chunkCount++
        lastChunkAt = time.Now()
        if firstChunkAt.IsZero() {
            firstChunkAt = lastChunkAt
        }
        if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
            outputTokens += chunk.Usage.CompletionTokens
        }
        lastChunkID = chunk.ID
        lastChunkModel = chunk.Model
        lastChunkCreated = chunk.Created

        data, err := json.Marshal(chunk)
        if err != nil {
            slog.Error("resumable stream marshal chunk failed", "sid", sid, "error", err)
            data = []byte(`{"error":"marshal_failed"}`)
        }
        _, frame := buf.Append(data)
        writeFrameClient(frame)
    }

    _ = keepalivePing

    // Pump closed: finalize the buffer so replay waiters drain and exit.
    buf.MarkFinalized()
    liveCancel()

    // Terminal: only if the client is still connected. A disconnected client
    // gets nothing — its reconnect will replay from the buffer.
    if !clientGone && ctx.Err() == nil {
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
            _, usageFrame := buf.Append(usageData)
            writeFrameClient(usageFrame)
        }
        _, doneFrame := buf.Append([]byte("[DONE]"))
        writeFrameClient(doneFrame)
    }

    // Summary + metrics (mirrors plain handleStreamChat). watchdog_tripped is a
    // stall (RecordFailure nudges breaker); otherwise success. A client_gone
    // exit is NOT recorded as a failure — the generation completed for the
    // buffer, the client just left.
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
        slog.Info("resumable stream summary",
            "sid", sid, "model", req.Model, "provider", provider.Name(),
            "duration", dur, "chunks", chunkCount, "first_event_ttfb", firstTTFB,
            "last_event_idle", lastIdle, "end_reason", endReason, "client_gone", clientGone)
    }()
    if endReason == "watchdog_tripped" {
        observability.RecordRequest(string(decision.Backend), req.Model, "stall")
        s.recordOutcome(decision.Backend, decision.NodeID, false)
    } else {
        observability.RecordRequest(string(decision.Backend), req.Model, "success")
        s.recordOutcome(decision.Backend, decision.NodeID, true)
    }
    duration := time.Since(start).Seconds()
    observability.RecordDuration(string(decision.Backend), req.Model, duration)
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = string(decision.Backend)
        logEntry.InputTokens = budget.InputTokens
        logEntry.OutputTokens = outputTokens
        logEntry.TotalTokens = budget.InputTokens + outputTokens
    }
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), time.Duration(duration*float64(time.Second)))
    }
    if s.costTracker != nil {
        keyName := "anonymous"
        if kc := middleware.GetAuthKeyConfig(ctx); kc != nil && kc.Name != "" {
            keyName = kc.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, budget.InputTokens, outputTokens)
    }
    // Buffer is retained for the TTL reconnect window — NOT released here.
    // reapExpiredStreamBuffers evicts past-TTL entries.
}

// handleStreamResume is the GET /v1/messages/{stream_id}/events replay endpoint
// (issue #116). Honors Last-Event-ID (header) or ?last_event_id= (query) as the
// cursor "<sid>:<seq>"; replays buffered frames after the cursor, then drains
// new frames live until the stream finalizes. 404 when resume disabled or the
// stream is unknown/evicted. Auth + rate-limit via withMiddleware (same as
// /v1/messages). The replayed frames carry their original id lines so a further
// reconnect re-cites the latest cursor.
func (s *Server) handleStreamResume(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    if s.streamBuffers == nil {
        http.NotFound(w, r)
        return
    }
    // Path: /v1/messages/{sid}/events
    path := strings.TrimPrefix(r.URL.Path, "/v1/messages/")
    parts := strings.Split(path, "/")
    if len(parts) != 2 || parts[1] != "events" || parts[0] == "" {
        slog.Debug("stream resume: malformed path", "path", r.URL.Path)
        http.NotFound(w, r)
        return
    }
    sid := parts[0]
    buf := s.streamBuffers.Get(sid)
    if buf == nil {
        slog.Info("stream resume: unknown or evicted stream_id", "sid", sid)
        http.NotFound(w, r)
        return
    }

    // Cursor: Last-Event-ID header takes precedence; ?last_event_id= fallback;
    // empty = replay from the start.
    cursor := r.Header.Get("Last-Event-ID")
    if cursor == "" {
        cursor = r.URL.Query().Get("last_event_id")
    }
    afterSeq := 0
    if cursor != "" {
        if seq, ok := parseStreamCursor(cursor); ok {
            afterSeq = seq
        } else {
            slog.Warn("stream resume: unparseable Last-Event-ID, replaying from start", "sid", sid, "cursor", cursor)
        }
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
    w.Header().Set("X-Fusion-Stream-ID", sid)
    flusher, canFlush := w.(http.Flusher)

    slog.Info("stream resume: replaying", "sid", sid, "after_seq", afterSeq, "finalized", buf.IsFinalized())

    // Phase 1: emit the buffered frames after the cursor immediately.
    frames := buf.FramesAfter(afterSeq)
    clientGone := false
    writeFrame := func(f streamEventFrame) bool {
        if _, err := w.Write(f.frame); err != nil {
            slog.Warn("stream resume: client write failed", "sid", sid, "seq", f.seq, "error", err)
            clientGone = true
            return false
        }
        if canFlush {
            flusher.Flush()
        }
        return true
    }
    for _, f := range frames {
        if !writeFrame(f) {
            break
        }
        afterSeq = f.seq
    }

    // Phase 2: live drain — wait for new frames until finalized, replaying each
    // batch. A finalized stream ends after the last buffered frame. Poll timeout
    // per round keeps the connection responsive to client disconnect (ctx).
    pollTimeout := 5 * time.Second
    for !clientGone && ctx.Err() == nil {
        newFrames, finalized := buf.WaitForNew(afterSeq, pollTimeout)
        for _, f := range newFrames {
            if !writeFrame(f) {
                clientGone = true
                break
            }
            afterSeq = f.seq
        }
        if finalized && len(newFrames) == 0 {
            break
        }
        if finalized {
            // Drain any straggler frames already captured, then end.
            continue
        }
    }
    slog.Info("stream resume: ended", "sid", sid, "last_seq", afterSeq, "client_gone", clientGone, "ctx_err", ctx.Err())
}

// parseStreamCursor parses "<sid>:<seq>" and returns the seq. Returns ok=false
// if the format is wrong (caller replays from start).
func parseStreamCursor(cursor string) (int, bool) {
    idx := strings.LastIndex(cursor, ":")
    if idx < 0 {
        return 0, false
    }
    seq, err := strconv.Atoi(cursor[idx+1:])
    if err != nil || seq < 0 {
        return 0, false
    }
    return seq, true
}
