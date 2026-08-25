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

    r.Register("task-1", cancel)
    if r.Len() != 1 {
        t.Fatalf("len after register = %d, want 1", r.Len())
    }

    if !r.Cancel("task-1") {
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
    if r.Cancel("nope") {
        t.Fatal("Cancel returned true for unknown task")
    }
    if r.Len() != 0 {
        t.Fatalf("len = %d, want 0", r.Len())
    }
}

func TestTaskRegistry_ReleaseIdempotent(t *testing.T) {
    r := NewTaskRegistry()
    ctx, cancel := context.WithCancel(context.Background())

    r.Register("task-2", cancel)
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

    r.Register("task-3", cancel)
    if !r.Cancel("task-3") {
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
    r.Register("", func() {})
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
            r.Register(id, cancel)
            time.Sleep(time.Millisecond)
            r.Cancel(id)
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
