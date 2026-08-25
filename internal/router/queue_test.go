package router

import (
    "context"
    "errors"
    "sync"
    "testing"
    "time"
)

func TestSlotQueue_AcquireRelease(t *testing.T) {
    q := newSlotQueue(2)
    ctx := context.Background()

    r1, err := q.Acquire(ctx, time.Second)
    if err != nil {
        t.Fatalf("first acquire: %v", err)
    }
    r2, err := q.Acquire(ctx, time.Second)
    if err != nil {
        t.Fatalf("second acquire: %v", err)
    }
    if q.Occupied() != 2 {
        t.Fatalf("occupied = %d, want 2", q.Occupied())
    }

    r1()
    r2()
    if q.Occupied() != 0 {
        t.Fatalf("after release occupied = %d, want 0", q.Occupied())
    }

    // Slot free again after release.
    r3, err := q.Acquire(ctx, time.Second)
    if err != nil {
        t.Fatalf("acquire after release: %v", err)
    }
    r3()
}

func TestSlotQueue_Timeout429(t *testing.T) {
    q := newSlotQueue(1)
    ctx := context.Background()

    hold, err := q.Acquire(ctx, time.Second)
    if err != nil {
        t.Fatalf("first acquire: %v", err)
    }
    defer hold()

    // Cap full, short budget -> must time out with ErrQueueTimeout.
    _, err = q.Acquire(ctx, 20*time.Millisecond)
    if !errors.Is(err, ErrQueueTimeout) {
        t.Fatalf("expected ErrQueueTimeout, got %v", err)
    }
}

func TestSlotQueue_NoWaitBudgetFailsFast(t *testing.T) {
    q := newSlotQueue(1)
    ctx := context.Background()

    hold, err := q.Acquire(ctx, time.Second)
    if err != nil {
        t.Fatalf("first acquire: %v", err)
    }
    defer hold()

    _, err = q.Acquire(ctx, 0)
    if !errors.Is(err, ErrQueueTimeout) {
        t.Fatalf("expected ErrQueueTimeout on no-wait full queue, got %v", err)
    }
}

func TestSlotQueue_ConcurrentUpToCap(t *testing.T) {
    const cap = 4
    q := newSlotQueue(cap)
    ctx := context.Background()

    var wg sync.WaitGroup
    acquired := make(chan struct{}, cap*2)
    for i := 0; i < cap; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            r, err := q.Acquire(ctx, time.Second)
            if err != nil {
                t.Errorf("acquire within cap: %v", err)
                return
            }
            acquired <- struct{}{}
            time.Sleep(30 * time.Millisecond)
            r()
        }()
    }
    wg.Wait()

    // All `cap` goroutines acquired and released without error; queue empty.
    if got := len(acquired); got != cap {
        t.Fatalf("acquired count = %d, want %d", got, cap)
    }
    if q.Occupied() != 0 {
        t.Fatalf("occupied after all released = %d, want 0", q.Occupied())
    }
}

func TestSlotQueue_CtxCancelWhileWaiting(t *testing.T) {
    q := newSlotQueue(1)
    ctx := context.Background()

    hold, err := q.Acquire(ctx, time.Second)
    if err != nil {
        t.Fatalf("first acquire: %v", err)
    }
    defer hold()

    waitCtx, waitCancel := context.WithCancel(ctx)
    go func() {
        time.Sleep(20 * time.Millisecond)
        waitCancel()
    }()

    _, err = q.Acquire(waitCtx, 5*time.Second)
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("expected ctx.Canceled, got %v", err)
    }
}
