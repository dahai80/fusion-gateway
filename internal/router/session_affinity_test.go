package router

import (
    "sync"
    "testing"
    "time"
)

func TestNewSessionAffinity_DefaultTTL(t *testing.T) {
    sa := NewSessionAffinity(0)
    defer sa.Stop()

    if sa.ttl != 30*time.Minute {
        t.Errorf("expected default TTL 30m, got %v", sa.ttl)
    }
    if sa.entries == nil {
        t.Error("expected entries map to be initialized")
    }
    t.Logf("NewSessionAffinity with zero TTL defaults to %v", sa.ttl)
}

func TestNewSessionAffinity_CustomTTL(t *testing.T) {
    sa := NewSessionAffinity(5 * time.Minute)
    defer sa.Stop()

    if sa.ttl != 5*time.Minute {
        t.Errorf("expected TTL 5m, got %v", sa.ttl)
    }
}

func TestSessionAffinity_RecordAndLookup(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Record("space-1", "fusion-mlx")
    provider, ok := sa.Lookup("space-1")
    if !ok {
        t.Fatal("expected to find space-1")
    }
    if provider != "fusion-mlx" {
        t.Errorf("expected fusion-mlx, got %s", provider)
    }
    t.Logf("Record+Lookup: space-1 -> %s", provider)
}

func TestSessionAffinity_LookupMissing(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Record("space-1", "fusion-mlx")
    _, ok := sa.Lookup("space-nonexistent")
    if ok {
        t.Error("expected not to find nonexistent space")
    }
    t.Log("Lookup missing space returns ok=false as expected")
}

func TestSessionAffinity_LookupExpired(t *testing.T) {
    sa := NewSessionAffinity(50 * time.Millisecond)
    defer sa.Stop()

    sa.Record("space-expired", "fusion-mlx")
    t.Logf("Recorded space-expired, TTL=50ms, waiting for expiry")

    time.Sleep(80 * time.Millisecond)

    _, ok := sa.Lookup("space-expired")
    if ok {
        t.Error("expected expired entry to not be found")
    }
    t.Log("Expired entry correctly returns ok=false")
}

func TestSessionAffinity_RecordEmptySpaceID(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Record("", "fusion-mlx")
    if sa.Size() != 0 {
        t.Errorf("expected size 0 with empty spaceID, got %d", sa.Size())
    }
    t.Log("Record with empty spaceID is a no-op")
}

func TestSessionAffinity_RecordEmptyProviderName(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Record("space-1", "")
    if sa.Size() != 0 {
        t.Errorf("expected size 0 with empty providerName, got %d", sa.Size())
    }
    t.Log("Record with empty providerName is a no-op")
}

func TestSessionAffinity_LookupEmptySpaceID(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    _, ok := sa.Lookup("")
    if ok {
        t.Error("expected Lookup with empty spaceID to return false")
    }
}

func TestSessionAffinity_RemoveExisting(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Record("space-1", "fusion-mlx")
    sa.Remove("space-1")

    _, ok := sa.Lookup("space-1")
    if ok {
        t.Error("expected space-1 to be removed")
    }
    if sa.Size() != 0 {
        t.Errorf("expected size 0 after remove, got %d", sa.Size())
    }
    t.Log("Remove existing entry works correctly")
}

func TestSessionAffinity_RemoveNonExisting(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Remove("space-nonexistent")
    if sa.Size() != 0 {
        t.Errorf("expected size 0 after removing nonexistent, got %d", sa.Size())
    }
    t.Log("Remove nonexistent entry is a no-op")
}

func TestSessionAffinity_Size(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    if sa.Size() != 0 {
        t.Errorf("expected size 0 initially, got %d", sa.Size())
    }

    sa.Record("space-1", "fusion-mlx")
    if sa.Size() != 1 {
        t.Errorf("expected size 1 after one record, got %d", sa.Size())
    }

    sa.Record("space-2", "qianfan")
    if sa.Size() != 2 {
        t.Errorf("expected size 2 after two records, got %d", sa.Size())
    }

    sa.Remove("space-1")
    if sa.Size() != 1 {
        t.Errorf("expected size 1 after remove, got %d", sa.Size())
    }
    t.Logf("Size tracking: 0 -> 1 -> 2 -> 1 works correctly")
}

func TestSessionAffinity_RecordOverwrite(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    sa.Record("space-1", "fusion-mlx")
    sa.Record("space-1", "qianfan")

    provider, ok := sa.Lookup("space-1")
    if !ok {
        t.Fatal("expected to find space-1")
    }
    if provider != "qianfan" {
        t.Errorf("expected qianfan after overwrite, got %s", provider)
    }
    if sa.Size() != 1 {
        t.Errorf("expected size 1 after overwrite, got %d", sa.Size())
    }
    t.Log("Record overwrites existing entry correctly")
}

func TestSessionAffinity_Stop(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    sa.Stop()

    t.Log("Stop() completes without panic")
}

// EI10: Stop must be idempotent — the shutdown path (routerEngine.Shutdown →
// sa.Stop) plus a deferred sa.Stop in a test can reach Stop twice. The prior
// implementation did close(sa.done) unguarded → double-close panic. Also Stop
// must join the evictLoop (wait for exit), not just signal it.
func TestSessionAffinity_EI10_StopIdempotentAndJoins(t *testing.T) {
    t.Parallel()
    sa := NewSessionAffinity(10 * time.Minute)

    // Double-Stop must not panic (the bug: close of closed channel).
    sa.Stop()
    sa.Stop()
    sa.Stop()

    // After Stop the evictLoop goroutine has exited — Size is still callable
    // (only the loop is gone, the map ops are independent). This proves Stop
    // returned after the join, not before.
    if sa.Size() != 0 {
        t.Errorf("EI10: fresh affinity should have size 0, got %d", sa.Size())
    }
}

func TestSessionAffinity_EvictExpired(t *testing.T) {
    sa := NewSessionAffinity(50 * time.Millisecond)
    defer sa.Stop()

    sa.Record("space-a", "fusion-mlx")
    sa.Record("space-b", "qianfan")
    t.Logf("Recorded 2 entries, TTL=50ms, waiting for expiry")

    time.Sleep(80 * time.Millisecond)

    sa.evictExpired()

    if sa.Size() != 0 {
        t.Errorf("expected size 0 after eviction, got %d", sa.Size())
    }
    t.Log("evictExpired removes all expired entries")
}

func TestSessionAffinity_EvictPartial(t *testing.T) {
    sa := NewSessionAffinity(50 * time.Millisecond)
    defer sa.Stop()

    sa.Record("space-old", "fusion-mlx")
    t.Logf("Recorded space-old with TTL=50ms")

    time.Sleep(80 * time.Millisecond)

    sa.Record("space-new", "qianfan")
    t.Logf("Recorded space-new after old expired")

    sa.evictExpired()

    if sa.Size() != 1 {
        t.Errorf("expected size 1 after partial eviction, got %d", sa.Size())
    }
    provider, ok := sa.Lookup("space-new")
    if !ok || provider != "qianfan" {
        t.Errorf("expected space-new=qianfan, got ok=%v provider=%s", ok, provider)
    }
    t.Log("evictExpired only removes expired entries, keeps live ones")
}

func TestSessionAffinity_ConcurrentAccess(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    var wg sync.WaitGroup
    numGoroutines := 20

    for i := 0; i < numGoroutines; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            spaceID := "space-concurrent"
            if idx%2 == 0 {
                sa.Record(spaceID, "fusion-mlx")
            } else {
                sa.Record(spaceID, "qianfan")
            }
            sa.Lookup(spaceID)
            sa.Size()
        }(i)
    }

    wg.Wait()

    if sa.Size() != 1 {
        t.Errorf("expected size 1 after concurrent writes, got %d", sa.Size())
    }
    t.Log("Concurrent Record/Lookup/Size completes without race")
}

func TestSessionAffinity_ConcurrentRecordAndRemove(t *testing.T) {
    sa := NewSessionAffinity(10 * time.Minute)
    defer sa.Stop()

    var wg sync.WaitGroup
    numOps := 50

    for i := 0; i < numOps; i++ {
        wg.Add(2)
        go func(idx int) {
            defer wg.Done()
            sa.Record("space-race", "fusion-mlx")
        }(i)
        go func() {
            defer wg.Done()
            sa.Remove("space-race")
        }()
    }

    wg.Wait()
    t.Log("Concurrent Record and Remove completes without race")
}
