package server

import (
    "context"
    "log/slog"
    "sync"
    "time"

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
//
// B12: each entry also records OwnerKey — the auth-key name that enqueued the
// task — so the cancel endpoint can refuse a cross-tenant cancel (key A
// guessing/replaying key B's X-Request-ID to kill a rival's long-run agent
// task). Empty OwnerKey (no auth principal, e.g. auth disabled) is treated as
// unowned and cancelable by anyone; the master key bypasses the check.
type taskEntry struct {
    cancel       context.CancelFunc
    ownerKey     string
    registeredAt time.Time
}

type TaskRegistry struct {
    mu    sync.RWMutex
    tasks map[string]*taskEntry
    // RR7 backstop: TTL force-cancels+evicts hung entries, maxEntries caps the
    // map. Set once via SetLimits before the reaper starts. ttl<=0 disables
    // reaping (cap-only). maxEntries<=0 disables the cap (unbounded, legacy).
    ttl        time.Duration
    maxEntries int
}

// NewTaskRegistry builds an empty registry with no limits (legacy callers /
// tests). Production wires limits via SetLimits from routing.agent_tasks.
func NewTaskRegistry() *TaskRegistry {
    return &TaskRegistry{tasks: make(map[string]*taskEntry)}
}

// SetLimits configures the RR7 backstop. ttl is the max age before a hung entry
// is force-canceled+evicted by the reaper (<=0 disables reaping). maxEntries
// caps the live map size (<=0 disables the cap). Must be called before the
// reaper goroutine starts and before any Register; not safe to call
// concurrently with Register.
func (r *TaskRegistry) SetLimits(ttl time.Duration, maxEntries int) {
    r.mu.Lock()
    r.ttl = ttl
    r.maxEntries = maxEntries
    r.mu.Unlock()
    slog.Info("task registry: limits set",
        "ttl", ttl.String(), "max_entries", maxEntries,
        "reaper_enabled", ttl > 0, "cap_enabled", maxEntries > 0)
}

// Register associates taskID with its cancel func and ownerKey (the enqueuing
// auth-key name, for B12 per-key ownership). If taskID is empty the call is a
// no-op. An already-registered id is overwritten (logged) — callers use a
// unique X-Request-ID.
//
// RR7: when the cap is set and the map is full, a NEW id is refused (logged
// WARN) and the task runs unregistered — it still executes, just cannot be
// canceled via the endpoint until an entry frees. Refusing (vs evicting the
// oldest) avoids killing an arbitrary in-flight task; the TTL reaper reclaims
// space over time. An OVERWRITE of an existing id is allowed regardless of the
// cap (it does not grow the map).
func (r *TaskRegistry) Register(taskID, ownerKey string, cancel context.CancelFunc) {
    if taskID == "" {
        slog.Debug("task registry: skip register of empty task id")
        return
    }
    r.mu.Lock()
    if _, exists := r.tasks[taskID]; exists {
        slog.Warn("task registry: overwriting existing task id", "task_id", taskID)
        r.tasks[taskID] = &taskEntry{cancel: cancel, ownerKey: ownerKey, registeredAt: time.Now()}
        active := len(r.tasks)
        r.mu.Unlock()
        slog.Debug("task registry: re-registered in-flight task", "task_id", taskID, "owner", ownerKey, "active", active)
        return
    }
    if r.maxEntries > 0 && len(r.tasks) >= r.maxEntries {
        active := len(r.tasks)
        r.mu.Unlock()
        slog.Warn("task registry: full, skipping register (task runs uncancelable via endpoint)",
            "task_id", taskID, "owner", ownerKey, "active", active, "max", r.maxEntries)
        return
    }
    r.tasks[taskID] = &taskEntry{cancel: cancel, ownerKey: ownerKey, registeredAt: time.Now()}
    active := len(r.tasks)
    r.mu.Unlock()
    slog.Debug("task registry: registered in-flight task", "task_id", taskID, "owner", ownerKey, "active", active)
}

// Cancel invokes the cancel func for taskID and evicts the entry. Returns true
// if the task was found and canceled, false if not found (caller writes 404).
// Cancel is immediate — it signals the ctx; the stream goroutine observes
// ctx.Err() and exits on its own, releasing the slot via its defer.
//
// B12 ownership: requester is the canceling auth-key name; isMaster bypasses
// the check. A task with no owner (ownerKey == "", e.g. enqueued with auth
// disabled) is cancelable by anyone. A mismatch returns false with
// denied=true so the caller can write 403 rather than 404 (a 404 would let an
// attacker probe whether a guessed task-id exists).
func (r *TaskRegistry) Cancel(taskID, requester string, isMaster bool) (canceled, denied bool) {
    r.mu.Lock()
    entry, ok := r.tasks[taskID]
    if ok {
        delete(r.tasks, taskID)
    }
    r.mu.Unlock()
    if !ok {
        slog.Info("task registry: cancel for unknown task id", "task_id", taskID)
        return false, false
    }
    if !isMaster && entry.ownerKey != "" && entry.ownerKey != requester {
        slog.Warn("task registry: cancel denied, owner mismatch",
            "task_id", taskID, "owner", entry.ownerKey, "requester", requester)
        // Re-insert: the task is still in-flight, only the cancel was refused.
        // Reacquire the lock so the write is serialized against concurrent
        // Register/Cancel/Release; without it the unlocked delete above + this
        // write race (caught by -race in TestTaskRegistry_Concurrent).
        r.mu.Lock()
        r.tasks[taskID] = entry
        active := len(r.tasks)
        r.mu.Unlock()
        slog.Debug("task registry: denied cancel kept entry in-flight", "task_id", taskID, "active", active)
        return false, true
    }
    entry.cancel()
    // Note: len(r.tasks) is NOT read here — the delete above dropped this
    // entry under the lock, and a concurrent Cancel could be mid-map-write.
    // Log without the active count to avoid an unlocked map read (-race).
    slog.Info("task registry: canceled in-flight task", "task_id", taskID, "owner", entry.ownerKey, "requester", requester)
    return true, false
}

// Release evicts a completed task's entry. Called from the stream goroutine's
// defer. Idempotent: a task already canceled (and thus evicted) is a no-op.
func (r *TaskRegistry) Release(taskID string) {
    if taskID == "" {
        return
    }
    r.mu.Lock()
    _, existed := r.tasks[taskID]
    var active int
    if existed {
        delete(r.tasks, taskID)
        active = len(r.tasks)
    }
    r.mu.Unlock()
    if existed {
        slog.Debug("task registry: released completed task", "task_id", taskID, "active", active)
    }
}

// Len returns the number of currently in-flight registered tasks (for
// metrics/observability).
func (r *TaskRegistry) Len() int {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return len(r.tasks)
}

// ReapExpired force-cancels and evicts entries older than the TTL, returning
// the count reaped. A no-op (returns 0) when ttl<=0. now is injected so tests
// are deterministic; the reaper goroutine passes time.Now(). Entries reaped
// here are hung tasks whose stream goroutine never called Release — canceling
// their ctx signals them to exit (releasing their local slot via the stream's
// own defer), which is exactly the recovery an explicit cancel endpoint hit
// would trigger.
func (r *TaskRegistry) ReapExpired(now time.Time) int {
    if r.ttl <= 0 {
        return 0
    }
    deadline := now.Add(-r.ttl)
    r.mu.Lock()
    reaped := 0
    for id, entry := range r.tasks {
        if entry.registeredAt.Before(deadline) || entry.registeredAt.Equal(deadline) {
            delete(r.tasks, id)
            reaped++
            // Cancel outside the lock would be cleaner, but the Cancel path
            // also deletes under the lock; canceling here is safe because the
            // entry is already removed — a concurrent Cancel will 404 and a
            // concurrent Release will no-op. CancelFunc is safe to call with
            // the mutex held (it never blocks on this registry).
            entry.cancel()
        }
    }
    r.mu.Unlock()
    if reaped > 0 {
        slog.Warn("task registry: reaped expired hung tasks",
            "reaped", reaped, "ttl", r.ttl.String())
    }
    return reaped
}
