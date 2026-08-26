package server

import (
    "context"
    "sync"
    "testing"
    "time"
)

func TestTaskRegistry_RegisterCancelRelease(t *testing.T) {
    r := NewTaskRegistry()
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    r.Register("task-1", "", cancel)
    if r.Len() != 1 {
        t.Fatalf("len after register = %d, want 1", r.Len())
    }

    if canceled, _ := r.Cancel("task-1", "", false); !canceled {
        t.Fatal("Cancel returned false for registered task")
    }
    if ctx.Err() == nil {
        t.Fatal("cancel func was not invoked")
    }
    if r.Len() != 0 {
        t.Fatalf("len after cancel = %d, want 0 (evicted)", r.Len())
    }
}

func TestTaskRegistry_CancelNotFound(t *testing.T) {
    r := NewTaskRegistry()
    if canceled, _ := r.Cancel("nope", "", false); canceled {
        t.Fatal("Cancel returned true for unknown task")
    }
    if r.Len() != 0 {
        t.Fatalf("len = %d, want 0", r.Len())
    }
}

func TestTaskRegistry_ReleaseIdempotent(t *testing.T) {
    r := NewTaskRegistry()
    ctx, cancel := context.WithCancel(context.Background())

    r.Register("task-2", "", cancel)
    r.Release("task-2")
    if r.Len() != 0 {
        t.Fatalf("len after release = %d, want 0", r.Len())
    }
    // Release again: no panic, still empty.
    r.Release("task-2")
    if r.Len() != 0 {
        t.Fatalf("len after double release = %d, want 0", r.Len())
    }
    // cancel never invoked by Release.
    if ctx.Err() != nil {
        t.Fatal("Release should not invoke cancel")
    }
    cancel()
}

func TestTaskRegistry_CancelThenReleaseNoDoubleEvict(t *testing.T) {
    r := NewTaskRegistry()
    cancel := context.CancelFunc(func() {})

    r.Register("task-3", "", cancel)
    if canceled, _ := r.Cancel("task-3", "", false); !canceled {
        t.Fatal("Cancel should hit")
    }
    // Release after Cancel already evicted: idempotent, no side effect.
    r.Release("task-3")
    if r.Len() != 0 {
        t.Fatalf("len = %d, want 0", r.Len())
    }
}

func TestTaskRegistry_RegisterEmptySkipped(t *testing.T) {
    r := NewTaskRegistry()
    r.Register("", "", func() {})
    if r.Len() != 0 {
        t.Fatalf("empty id should be skipped, len = %d", r.Len())
    }
}

func TestTaskRegistry_Concurrent(t *testing.T) {
    r := NewTaskRegistry()
    var wg sync.WaitGroup
    for i := 0; i < 50; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            _, cancel := context.WithCancel(context.Background())
            id := taskID(i)
            r.Register(id, "", cancel)
            time.Sleep(time.Millisecond)
            r.Cancel(id, "", false)
        }(i)
    }
    wg.Wait()
    if r.Len() != 0 {
        t.Fatalf("len after concurrent churn = %d, want 0", r.Len())
    }
}

func taskID(i int) string {
    return "t-" + string(rune('A'+i))
}

// TestTaskRegistry_B12_OwnershipDenial covers the B12 per-key ownership guard:
// a task enqueued by key-A must not be cancelable by key-B. The cancel is
// refused (denied=true → caller writes 403), the entry stays in-flight (not
// evicted), and the cancel func is never invoked. key-A cancels its own task;
// the master key bypasses the guard; an unowned task (ownerKey=="") is
// cancelable by anyone.
func TestTaskRegistry_B12_OwnershipDenial(t *testing.T) {
    r := NewTaskRegistry()

    // key-A enqueues a task.
    ctxA, cancelA := context.WithCancel(context.Background())
    r.Register("task-own-A", "key-A", cancelA)

    // key-B tries to cancel key-A's task → denied, entry kept, ctx untouched.
    canceled, denied := r.Cancel("task-own-A", "key-B", false)
    if canceled {
        t.Fatal("cross-tenant cancel must not succeed (canceled=true)")
    }
    if !denied {
        t.Fatal("cross-tenant cancel must be denied (denied=false)")
    }
    if ctxA.Err() != nil {
        t.Fatal("denied cancel must not invoke the cancel func")
    }
    if r.Len() != 1 {
        t.Fatalf("denied cancel must keep entry in-flight, len = %d, want 1", r.Len())
    }

    // key-A cancels its own task → succeeds.
    canceled, denied = r.Cancel("task-own-A", "key-A", false)
    if !canceled || denied {
        t.Fatalf("owner cancel must succeed, canceled=%v denied=%v", canceled, denied)
    }
    if ctxA.Err() == nil {
        t.Fatal("owner cancel must invoke the cancel func")
    }
    if r.Len() != 0 {
        t.Fatalf("len after owner cancel = %d, want 0", r.Len())
    }
}

// TestTaskRegistry_B12_MasterBypass and unowned-task coverage.
func TestTaskRegistry_B12_MasterBypass(t *testing.T) {
    r := NewTaskRegistry()

    // key-A enqueues; master key cancels regardless of owner → succeeds.
    ctxM, cancelM := context.WithCancel(context.Background())
    r.Register("task-master", "key-A", cancelM)
    canceled, denied := r.Cancel("task-master", "anyone-else", true)
    if !canceled || denied {
        t.Fatalf("master bypass must succeed, canceled=%v denied=%v", canceled, denied)
    }
    if ctxM.Err() == nil {
        t.Fatal("master cancel must invoke the cancel func")
    }

    // Unowned task (ownerKey=="", e.g. auth disabled) cancelable by anyone.
    ctxU, cancelU := context.WithCancel(context.Background())
    r.Register("task-unowned", "", cancelU)
    canceled, denied = r.Cancel("task-unowned", "key-B", false)
    if !canceled || denied {
        t.Fatalf("unowned task must be cancelable by anyone, canceled=%v denied=%v", canceled, denied)
    }
    if ctxU.Err() == nil {
        t.Fatal("unowned cancel must invoke the cancel func")
    }
}

// TestTaskRegistry_RR7_CapRefusesNew verifies the MaxEntries cap: a full
// registry refuses a NEW id (task runs unregistered, no map growth) but still
// allows OVERWRITING an existing id.
func TestTaskRegistry_RR7_CapRefusesNew(t *testing.T) {
    r := NewTaskRegistry()
    r.SetLimits(0, 2) // cap=2, no reaping

    r.Register("t1", "", func() {})
    r.Register("t2", "", func() {})
    if got := r.Len(); got != 2 {
        t.Fatalf("len = %d, want 2", got)
    }

    // New id refused (map full) — task runs uncancelable via endpoint.
    r.Register("t3", "", func() {})
    if got := r.Len(); got != 2 {
        t.Fatalf("len after refused new = %d, want 2 (no growth)", got)
    }

    // Overwrite existing id allowed (does not grow the map).
    r.Register("t1", "owner-updated", func() {})
    if got := r.Len(); got != 2 {
        t.Fatalf("len after overwrite = %d, want 2 (overwrite ok)", got)
    }
}

// TestTaskRegistry_RR7_ReapExpired verifies the TTL reaper force-cancels and
// evicts entries older than ttl, leaves younger entries untouched, and is a
// no-op when ttl<=0.
func TestTaskRegistry_RR7_ReapExpired(t *testing.T) {
    r := NewTaskRegistry()
    r.SetLimits(5*time.Second, 0) // ttl=5s, no cap

    // old task: registered 10s ago → reaped.
    oldCtx, oldCancel := context.WithCancel(context.Background())
    r.Register("old", "", oldCancel)
    // Manually backdate registeredAt to simulate a hung entry.
    r.mu.Lock()
    r.tasks["old"].registeredAt = time.Now().Add(-10 * time.Second)
    r.mu.Unlock()

    // young task: registered now → kept.
    youngCtx, youngCancel := context.WithCancel(context.Background())
    r.Register("young", "", youngCancel)

    reaped := r.ReapExpired(time.Now())
    if reaped != 1 {
        t.Fatalf("reaped = %d, want 1", reaped)
    }
    if oldCtx.Err() == nil {
        t.Fatal("reaped task's cancel func must be invoked")
    }
    if youngCtx.Err() != nil {
        t.Fatal("young task must NOT be canceled")
    }
    if got := r.Len(); got != 1 {
        t.Fatalf("len after reap = %d, want 1", got)
    }

    // Cancel endpoint now 404s the reaped task (entry already evicted).
    canceled, _ := r.Cancel("old", "", false)
    if canceled {
        t.Fatal("Cancel of reaped task must return false (404)")
    }
    // Cleanup young.
    youngCancel()
}

// TestTaskRegistry_RR7_ReapExpiredDisabled verifies ttl<=0 makes ReapExpired a
// no-op even with old entries present.
func TestTaskRegistry_RR7_ReapExpiredDisabled(t *testing.T) {
    r := NewTaskRegistry()
    r.SetLimits(0, 0) // reaping + cap disabled (legacy)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    r.Register("hung", "", cancel)
    r.mu.Lock()
    r.tasks["hung"].registeredAt = time.Now().Add(-1 * time.Hour)
    r.mu.Unlock()

    if reaped := r.ReapExpired(time.Now()); reaped != 0 {
        t.Fatalf("reaped = %d, want 0 (reaping disabled)", reaped)
    }
    if ctx.Err() != nil {
        t.Fatal("cancel must NOT be invoked when reaping disabled")
    }
    if got := r.Len(); got != 1 {
        t.Fatalf("len = %d, want 1 (entry not reaped)", got)
    }
}

// TestTaskRegistry_RR7_ReapExactBoundary verifies an entry exactly at the ttl
// deadline is reaped (Before || Equal).
func TestTaskRegistry_RR7_ReapExactBoundary(t *testing.T) {
    r := NewTaskRegistry()
    r.SetLimits(5*time.Second, 0)

    ctx, cancel := context.WithCancel(context.Background())
    r.Register("boundary", "", cancel)
    // registeredAt = now - ttl exactly.
    r.mu.Lock()
    r.tasks["boundary"].registeredAt = time.Now().Add(-5 * time.Second)
    r.mu.Unlock()

    if reaped := r.ReapExpired(time.Now()); reaped != 1 {
        t.Fatalf("reaped = %d, want 1 at exact boundary", reaped)
    }
    if ctx.Err() == nil {
        t.Fatal("boundary entry cancel must be invoked")
    }
}
