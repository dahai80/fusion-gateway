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
