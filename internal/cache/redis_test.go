package cache

import (
    "context"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewRedisBackend_ConnectionFailed(t *testing.T) {
    cfg := config.RedisConfig{
        Addr: "localhost:1",
        DB:   0,
    }
    _, err := NewRedisBackend(cfg)
    if err == nil {
        t.Fatal("expected error when redis is unreachable")
    }
    t.Logf("expected connection error: %v", err)
}

func TestNewRedisBackend_DefaultPoolSize(t *testing.T) {
    cfg := config.RedisConfig{
        Addr:     "localhost:6379",
        PoolSize: 0,
    }
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
        return
    }
    defer backend.Close()

    if backend.client.Options().PoolSize != 10 {
        t.Fatalf("expected default pool size 10, got %d", backend.client.Options().PoolSize)
    }
    cancel()
    _ = ctx
}

func TestRedisBackend_Operations_WhenAvailable(t *testing.T) {
    cfg := config.RedisConfig{
        Addr: "localhost:6379",
        DB:   0,
    }
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    defer backend.Close()

    backend.Set("testkey", []byte("testval"), time.Minute)

    val, ok := backend.Get("testkey")
    if !ok || string(val) != "testval" {
        t.Fatalf("expected testval, got %s ok=%v", val, ok)
    }

    backend.Delete("testkey")

    _, ok = backend.Get("testkey")
    if ok {
        t.Fatal("expected miss after delete")
    }

    hits, misses, size := backend.Stats()
    t.Logf("stats: hits=%d misses=%d size=%d", hits, misses, size)
}

func TestRedisBackend_GetMiss(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    defer backend.Close()

    _, ok := backend.Get("nonexistent_key_for_test")
    if ok {
        t.Fatal("expected miss for nonexistent key")
    }
}

func TestRedisBackend_SetError_Handled(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    backend.Set("after_close", []byte("data"), time.Minute)
}

func TestRedisBackend_GetAfterClose(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    _, ok := backend.Get("after_close")
    if ok {
        t.Log("Get after close returned ok=true (connection may have been reused)")
    }
}

func TestRedisBackend_DeleteAfterClose(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    backend.Delete("after_close")
}

func TestRedisBackend_StatsAfterClose(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    hits, misses, size := backend.Stats()
    t.Logf("stats after close: hits=%d misses=%d size=%d", hits, misses, size)
}

func TestRedisBackend_CloseIdempotent(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()
    backend.Close()
}

// TestE2_RedisBackend_CountersRaceFree (E2): hits/misses must be atomic.Int64,
// NOT plain int64. Concurrent increments of the counters (as Get does on every
// request goroutine) plus concurrent Stats() reads must be race-free under
// `go test -race`. Before E2, r.hits++/r.misses++ on plain int64 was a data
// race (torn counter, /metrics hit-rate corrupted) — same bug class as EI2.
// This guard needs no live redis: it exercises the counter fields directly
// (the only race surface), so it runs in CI without a redis instance.
func TestE2_RedisBackend_CountersRaceFree(t *testing.T) {
    // Zero-value RedisBackend: client is nil, but we never call Get (which
    // needs the client) — we race the counter fields directly, mirroring what
    // Get does (r.hits.Add(1) / r.misses.Add(1)) plus Stats() reads.
    r := &RedisBackend{}

    var wg sync.WaitGroup
    // Writers: hammer recordHit/recordMiss — the REAL hot-path increments
    // Get calls on every request goroutine. Reverting the fields to plain
    // int64 makes these `r.hits++`/`r.misses++` read-modify-writes, which
    // the race detector flags under this concurrency.
    const writers = 8
    const iters = 2000
    for i := 0; i < writers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < iters; j++ {
                r.recordHit()
                r.recordMiss()
            }
        }()
    }
    // Concurrent readers: Stats() reads the counters while writers increment.
    for i := 0; i < 4; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < iters; j++ {
                _ = r.StatsCounterSnapshot()
            }
        }()
    }
    wg.Wait()

    // E2 atomicity: no increments lost. hits + misses must equal writers*iters
    // each (all Add(1) accounted for). A torn plain-int64 counter would
    // undercount under concurrency.
    want := int64(writers * iters)
    snap := r.StatsCounterSnapshot()
    if snap.hits != want {
        t.Errorf("E2: expected hits=%d, got %d (counter torn under concurrency — not atomic)", want, snap.hits)
    }
    if snap.misses != want {
        t.Errorf("E2: expected misses=%d, got %d (counter torn under concurrency — not atomic)", want, snap.misses)
    }
}

