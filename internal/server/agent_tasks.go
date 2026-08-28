package server

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// handleAgentTask implements POST /v1/agent/tasks/{id}/cancel (#102 ADR-001
// sub-task 4). Path is prefix-registered under "/v1/agent/tasks/" and parsed
// manually (no Go 1.22 path-pattern dep): expected suffix is "{taskID}/cancel".
// On a known in-flight task the registry cancel propagates to the stream ctx;
// the slot is released by the stream goroutine's existing defer (no
// double-release — see #97/v0.8.40). 200 + {"status":"canceled"} on hit, 404
// when the task is unknown or already completed. Auth via withMiddleware.
func (s *Server) handleAgentTask(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    // Parse path: "/v1/agent/tasks/{taskID}/cancel". Trim the registered
    // prefix, then expect exactly "id/cancel". Reject malformed paths with 404
    // (not 400) so a probe sees no distinguishable shape.
    const prefix = "/v1/agent/tasks/"
    rest := strings.TrimPrefix(r.URL.Path, prefix)
    parts := strings.Split(rest, "/")
    if len(parts) != 2 || parts[1] != "cancel" || parts[0] == "" {
        slog.Info("agent task cancel: malformed path", "path", r.URL.Path)
        http.NotFound(w, r)
        return
    }
    taskID := parts[0]

    if s.taskRegistry == nil {
        slog.Warn("agent task cancel: registry not initialized")
        http.Error(w, `{"error":{"message":"task registry unavailable","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    // B12: per-key ownership. Resolve the canceling principal; the master key
    // bypasses the check, a regular key may only cancel tasks it enqueued.
    // A task enqueued with no owner (auth off) is cancelable by anyone. A
    // mismatch returns 403, not 404, so a guessed task-id does not leak
    // existence — but the 403 path intentionally does not echo the owner.
    requester := ""
    isMaster := false
    if p := middleware.PrincipalFromContext(r.Context()); p != nil {
        isMaster = p.IsMaster
        if p.KeyConfig != nil {
            requester = p.KeyConfig.Name
        }
    }

    canceled, denied := s.taskRegistry.Cancel(taskID, requester, isMaster)
    if denied {
        slog.Warn("agent task cancel: ownership denied", "task_id", taskID, "requester", requester, "is_master", isMaster)
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusForbidden)
        fmt.Fprintf(w, `{"error":{"message":"task not owned by this key","type":"forbidden","task_id":%q}}`, taskID)
        return
    }
    if canceled {
        slog.Info("agent task cancel: canceled in-flight task", "task_id", taskID, "requester", requester)
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        fmt.Fprintf(w, `{"status":"canceled","task_id":%q}`, taskID)
        return
    }

    slog.Info("agent task cancel: task not found (unknown or completed)", "task_id", taskID)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusNotFound)
    fmt.Fprintf(w, `{"error":{"message":"task not found","type":"not_found","task_id":%q}}`, taskID)
}

// reapExpiredTasks is the RR7 backstop reaper: force-cancels and evicts
// TaskRegistry entries older than routing.agent_tasks.ttl. Hung agent tasks
// (upstream half-open, model stuck) never Release on their own, so without
// this the entry + CancelFunc-held ctx + stream goroutine leak forever. A
// reaped cancel signals the hung stream to exit, releasing its local slot via
// the stream's own defer (same path as an explicit cancel-endpoint hit).
// Interval from routing.agent_tasks.reaper_interval; ttl<=0 makes ReapExpired a
// no-op so this loop runs harmlessly.
func (s *Server) reapExpiredTasks(ctx context.Context) {
    // cfgMu: read via snapshot() so the reload pointer swap (RebuildMiddlewareChain)
    // cannot race this background goroutine's read (race detector fix).
    interval := s.snapshot().Config.Routing.AgentTasks.ReaperInterval
    if interval <= 0 {
        interval = 5 * time.Minute
    }
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            // R1: honor shutdown so the reaper exits and Shutdown joins it.
            return
        case <-ticker.C:
        }
        s.taskRegistry.ReapExpired(time.Now())
    }
}

// reapExpiredStreamBuffers evicts resumable-stream buffers past their TTL
// (issue #116). The TTL is the reconnect window: a buffer past it can no longer
// be resumed and is dropped so memory does not grow unbounded under churn.
// No-op when resume is disabled (store is nil).
func (s *Server) reapExpiredStreamBuffers(ctx context.Context) {
    if s.streamBuffers == nil {
        return
    }
    // cfgMu: read via snapshot() — same race fix as reapExpiredTasks.
    ttl := s.snapshot().Config.Routing.Stream.ResumeTTL
    if ttl <= 0 {
        ttl = 10 * time.Minute
    }
    // Reap at a quarter of the TTL, clamped to >=10s so short TTLs still reap.
    interval := ttl / 4
    if interval < 10 * time.Second {
        interval = 10 * time.Second
    }
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            // R1: honor shutdown so the reaper exits and Shutdown joins it.
            return
        case <-ticker.C:
        }
        s.streamBuffers.ReapExpired(time.Now())
    }
}
