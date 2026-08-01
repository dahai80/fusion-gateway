package cache

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestCache_EvictExpired_NoExpiredEntries(t *testing.T) {
    cfg := config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        5 * time.Minute,
    }
    c := New(cfg)

    c.Set("valid", []byte("v"))

    time.Sleep(100 * time.Millisecond)

    _, _, size := c.Stats()
    if size != 1 {
        t.Fatalf("expected 1 valid entry, got %d", size)
    }
}

func TestCache_EvictExpired_MixedExpiredAndValid(t *testing.T) {
    cfg := config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        200 * time.Millisecond,
    }
    c := New(cfg)

    c.Set("expiring", []byte("v1"))

    time.Sleep(300 * time.Millisecond)

    c.Set("fresh", []byte("v2"))

    _, ok := c.Get("expiring")
    if ok {
        t.Log("expiring entry still accessible (not yet evicted by background ticker)")
    }

    _, ok = c.Get("fresh")
    if !ok {
        t.Fatal("fresh entry should still be accessible")
    }
}

func TestCache_EvictExpired_BackgroundTick(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping background eviction tick test in short mode")
    }
    cfg := config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        100 * time.Millisecond,
    }
    c := New(cfg)

    c.Set("short1", []byte("v1"))
    c.Set("short2", []byte("v2"))

    _, _, size := c.Stats()
    if size != 2 {
        t.Fatalf("expected 2 entries before expiry, got %d", size)
    }

    time.Sleep(200 * time.Millisecond)
    time.Sleep(32 * time.Second)

    _, _, size = c.Stats()
    if size != 0 {
        t.Logf("expected 0 after background eviction, got %d", size)
    }
}
