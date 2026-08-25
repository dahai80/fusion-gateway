package server

import (
    "context"
    "log/slog"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// taskIDFromContext returns the X-Request-ID carried in ctx (the cancel
// task-id for /v1/agent/tasks/{id}/cancel). Empty when no RequestID middleware
// ran — callers skip registration in that case.
func taskIDFromContext(ctx context.Context) string {
    return middleware.RequestIDFromContext(ctx)
}

// TaskRegistry maps an in-flight task id to its cancel function, so the
// POST /v1/agent/tasks/{id}/cancel endpoint can propagate cancellation to a
// running stream's ctx (#102 ADR-001 sub-task 4). The slot release happens on
// the stream goroutine's existing defer (the #97/v0.8.40 sole-release path) —
// the registry NEVER touches the in-flight counter, so cancel cannot
// double-release. Register on stream start, Release on stream exit (defer),
// Cancel on endpoint hit. Thread-safe.
type TaskRegistry struct {
    mu    sync.RWMutex
    tasks map[string]context.CancelFunc
}

// NewTaskRegistry builds an empty registry.
func NewTaskRegistry() *TaskRegistry {
    return &TaskRegistry{tasks: make(map[string]context.CancelFunc)}
}

// Register associates taskID with its cancel func. If taskID is empty or
// already registered, the prior entry is overwritten (logged) — the caller is
// expected to use a unique id (X-Request-ID from middleware).
func (r *TaskRegistry) Register(taskID string, cancel context.CancelFunc) {
    if taskID == "" {
        slog.Debug("task registry: skip register of empty task id")
        return
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.tasks[taskID]; exists {
        slog.Warn("task registry: overwriting existing task id", "task_id", taskID)
    }
    r.tasks[taskID] = cancel
    slog.Debug("task registry: registered in-flight task", "task_id", taskID, "active", len(r.tasks))
}

// Cancel invokes the cancel func for taskID and evicts the entry. Returns true
// if the task was found and canceled, false if not found (caller writes 404).
// Cancel is immediate — it signals the ctx; the stream goroutine observes
// ctx.Err() and exits on its own, releasing the slot via its defer.
func (r *TaskRegistry) Cancel(taskID string) bool {
    r.mu.Lock()
    cancel, ok := r.tasks[taskID]
    if ok {
        delete(r.tasks, taskID)
    }
    r.mu.Unlock()
    if !ok {
        slog.Info("task registry: cancel for unknown task id", "task_id", taskID)
        return false
    }
    cancel()
    slog.Info("task registry: canceled in-flight task", "task_id", taskID, "active", len(r.tasks))
    return true
}

// Release evicts a completed task's entry. Called from the stream goroutine's
// defer. Idempotent: a task already canceled (and thus evicted) is a no-op.
func (r *TaskRegistry) Release(taskID string) {
    if taskID == "" {
        return
    }
    r.mu.Lock()
    _, existed := r.tasks[taskID]
    if existed {
        delete(r.tasks, taskID)
    }
    r.mu.Unlock()
    if existed {
        slog.Debug("task registry: released completed task", "task_id", taskID, "active", len(r.tasks))
    }
}

// Len returns the number of currently in-flight registered tasks (for
// metrics/observability).
func (r *TaskRegistry) Len() int {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return len(r.tasks)
}
