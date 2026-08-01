package safego

import (
    "sync"
    "sync/atomic"
    "testing"
    "time"
)

func TestGo_RunsFunction(t *testing.T) {
    t.Log("testing that Go runs the provided function")
    var ran atomic.Bool
    Go("test-run", func() {
        ran.Store(true)
    })
    time.Sleep(50 * time.Millisecond)
    if !ran.Load() {
        t.Fatal("expected function to have been executed")
    }
}

func TestGo_RecoversFromPanic(t *testing.T) {
    t.Log("testing that Go recovers from panic without crashing")
    var ran atomic.Bool
    Go("test-panic", func() {
        panic("intentional test panic")
    })
    Go("test-after-panic", func() {
        ran.Store(true)
    })
    time.Sleep(50 * time.Millisecond)
    if !ran.Load() {
        t.Fatal("expected second goroutine to run after first one panicked")
    }
}

func TestGo_ConcurrentGoroutines(t *testing.T) {
    t.Log("testing that multiple goroutines work concurrently")
    const n = 100
    var wg sync.WaitGroup
    var counter atomic.Int32

    for i := 0; i < n; i++ {
        wg.Add(1)
        Go("concurrent-test", func() {
            counter.Add(1)
            wg.Done()
        })
    }

    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        if counter.Load() != n {
            t.Fatalf("expected counter=%d, got %d", n, counter.Load())
        }
    case <-time.After(2 * time.Second):
        t.Fatalf("timeout: only %d/%d goroutines completed", counter.Load(), n)
    }
}

func TestGo_PanicDoesNotBlockOthers(t *testing.T) {
    t.Log("testing that a panic in one goroutine does not affect others")
    const panics = 10
    const normals = 10
    var normalCount atomic.Int32

    var wg sync.WaitGroup
    for i := 0; i < panics; i++ {
        wg.Add(1)
        Go("panic-goroutine", func() {
            defer wg.Done()
            panic("bang")
        })
    }
    for i := 0; i < normals; i++ {
        wg.Add(1)
        Go("normal-goroutine", func() {
            defer wg.Done()
            normalCount.Add(1)
        })
    }

    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        if normalCount.Load() != normals {
            t.Fatalf("expected %d normal completions, got %d", normals, normalCount.Load())
        }
    case <-time.After(2 * time.Second):
        t.Fatalf("timeout: only %d/%d normal goroutines completed", normalCount.Load(), normals)
    }
}
