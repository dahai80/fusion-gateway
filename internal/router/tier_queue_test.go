package router

import (
    "context"
    "errors"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestTierQueue_AcquireRelease(t *testing.T) {
    q := NewTierQueue(config.TierQueueConfig{
        Enabled: true, MaxConcurrent: 2, QueueTimeout: time.Second,
    })
    ctx := context.Background()
    r1, err := q.Acquire(ctx, TierHeavy)
    if err != nil {
        t.Fatalf("acquire 1: %v", err)
    }
    r2, err := q.Acquire(ctx, TierGeneral)
    if err != nil {
        t.Fatalf("acquire 2: %v", err)
    }
    if q.Occupied() != 2 {
        t.Fatalf("expected 2 occupied, got %d", q.Occupied())
    }
    r1()
    r2()
    if q.Occupied() != 0 {
        t.Fatalf("expected 0 occupied after release, got %d", q.Occupied())
    }
}

// #159: priority dispatch. With the queue full, a heavy waiter must be
// dispatched before an earlier-arrived light waiter when a slot frees —
// head-of-line by tier, not FIFO across tiers.
func TestTierQueue_PriorityDispatch(t *testing.T) {
    q := NewTierQueue(config.TierQueueConfig{
        Enabled: true, MaxConcurrent: 1, QueueTimeout: 2 * time.Second,
    })
    ctx := context.Background()
    holder, _ := q.Acquire(ctx, TierGeneral)

    var wg sync.WaitGroup
    var lightAcquired, heavyAcquired time.Time
    errs := make(chan error, 2)

    wg.Add(2)
    // light arrives first
    go func() {
        defer wg.Done()
        r, err := q.Acquire(ctx, TierLight)
        if err != nil {
            errs <- err
            return
        }
        lightAcquired = time.Now()
        r()
    }()
    time.Sleep(20 * time.Millisecond) // ensure light is queued first
    // heavy arrives second — must win the freed slot
    go func() {
        defer wg.Done()
        r, err := q.Acquire(ctx, TierHeavy)
        if err != nil {
            errs <- err
            return
        }
        heavyAcquired = time.Now()
        r()
    }()
    time.Sleep(20 * time.Millisecond) // ensure both are queued
    holder()                          // free the single slot

    wg.Wait()
    close(errs)
    if err := <-errs; err != nil {
        t.Fatalf("acquire error: %v", err)
    }
    if !heavyAcquired.Before(lightAcquired) {
        t.Fatalf("heavy should be dispatched before light: heavy=%v light=%v",
            heavyAcquired, lightAcquired)
    }
}

// #159: timeout returns ErrTierQueueTimeout, not a slot.
func TestTierQueue_Timeout(t *testing.T) {
    q := NewTierQueue(config.TierQueueConfig{
        Enabled: true, MaxConcurrent: 1, QueueTimeout: 50 * time.Millisecond,
    })
    ctx := context.Background()
    holder, _ := q.Acquire(ctx, TierGeneral)
    defer holder()

    _, err := q.Acquire(ctx, TierHeavy)
    if !errors.Is(err, ErrTierQueueTimeout) {
        t.Fatalf("expected ErrTierQueueTimeout, got %v", err)
    }
}

// #159: ctx cancel drops the waiter without acquiring.
func TestTierQueue_CancelCtx(t *testing.T) {
    q := NewTierQueue(config.TierQueueConfig{
        Enabled: true, MaxConcurrent: 1, QueueTimeout: 2 * time.Second,
    })
    ctx, cancel := context.WithCancel(context.Background())
    holder, _ := q.Acquire(context.Background(), TierGeneral)
    defer holder()

    errCh := make(chan error, 1)
    go func() {
        _, err := q.Acquire(ctx, TierHeavy)
        errCh <- err
    }()
    time.Sleep(20 * time.Millisecond)
    cancel()
    err := <-errCh
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("expected context.Canceled, got %v", err)
    }
}

// #159: TierForRequest honors the tenant Tier tag over the intent heuristic.
func TestTierForRequest_TenantWins(t *testing.T) {
    ci := coarseIntentLike{label: "diffusion"}
    if got := TierForRequest(ci, ""); got != TierHeavy {
        t.Fatalf("diffusion intent -> heavy, got %s", got)
    }
    // tenant tag overrides intent
    if got := TierForRequest(ci, "light"); got != TierLight {
        t.Fatalf("tenant light tag should override diffusion intent, got %s", got)
    }
    if got := TierForRequest(coarseIntentLike{label: "lightweight"}, ""); got != TierLight {
        t.Fatalf("lightweight intent -> light, got %s", got)
    }
    if got := TierForRequest(coarseIntentLike{label: "unknown"}, ""); got != TierGeneral {
        t.Fatalf("unknown intent -> general, got %s", got)
    }
}

// #159: buildTierQueue returns nil when disabled (default-off, no behavior
// change for existing deployments).
func TestBuildTierQueue_Disabled(t *testing.T) {
    if q := buildTierQueue(nil); q != nil {
        t.Fatal("nil cfg should return nil queue")
    }
}

// coarseIntentLike is a test double implementing the {String() string} iface.
type coarseIntentLike struct{ label string }

func (c coarseIntentLike) String() string { return c.label }
