package cache

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNew_Disabled(t *testing.T) {
    t.Parallel()
    cfg := config.CacheConfig{Enabled: false}
    c := New(cfg)
    if c != nil {
        t.Fatal("expected nil when cache disabled")
    }
}

func TestNew_Enabled(t *testing.T) {
    t.Parallel()
    cfg := config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        time.Minute,
    }
    c := New(cfg)
    if c == nil {
        t.Fatal("expected non-nil cache when enabled")
    }
}

func TestGet_MissingKey(t *testing.T) {
    t.Parallel()
    c := New(config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        time.Minute,
    })
    val, ok := c.Get("nonexistent")
    if ok {
        t.Fatal("expected false for missing key")
    }
    if val != nil {
        t.Fatal("expected nil value for missing key")
    }
}

func TestSetGet_RoundTrip(t *testing.T) {
    t.Parallel()
    c := New(config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        time.Minute,
    })
    c.Set("key1", []byte("value1"))
    val, ok := c.Get("key1")
    if !ok {
        t.Fatal("expected true for existing key")
    }
    if string(val) != "value1" {
        t.Fatalf("expected value1, got %s", val)
    }
}

func TestStats_HitMiss(t *testing.T) {
    t.Parallel()
    c := New(config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        time.Minute,
    })
    c.Set("k", []byte("v"))

    c.Get("k")
    c.Get("missing")

    hits, misses, size := c.Stats()
    if hits != 1 {
        t.Fatalf("expected 1 hit, got %d", hits)
    }
    if misses != 1 {
        t.Fatalf("expected 1 miss, got %d", misses)
    }
    if size != 1 {
        t.Fatalf("expected size 1, got %d", size)
    }
}

func TestTTLExpiry(t *testing.T) {
    c := New(config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        50 * time.Millisecond,
    })
    c.Set("expiring", []byte("data"))

    val, ok := c.Get("expiring")
    if !ok || string(val) != "data" {
        t.Fatal("expected value immediately after set")
    }

    time.Sleep(100 * time.Millisecond)

    val, ok = c.Get("expiring")
    if ok {
        t.Fatal("expected false after TTL expiry")
    }
    if val != nil {
        t.Fatal("expected nil value after TTL expiry")
    }
}

func TestLRUEviction(t *testing.T) {
    t.Parallel()
    c := New(config.CacheConfig{
        Enabled:    true,
        MaxEntries: 3,
        TTL:        time.Minute,
    })

    c.Set("a", []byte("1"))
    c.Set("b", []byte("2"))
    c.Set("c", []byte("3"))

    _, _, size := c.Stats()
    if size != 3 {
        t.Fatalf("expected size 3, got %d", size)
    }

    c.Set("d", []byte("4"))

    _, ok := c.Get("a")
    if ok {
        t.Fatal("expected oldest key 'a' to be evicted")
    }

    _, _, size = c.Stats()
    if size != 3 {
        t.Fatalf("expected size 3 after eviction, got %d", size)
    }

    for _, key := range []string{"b", "c", "d"} {
        val, ok := c.Get(key)
        if !ok {
            t.Fatalf("expected key %s to exist", key)
        }
        if len(val) == 0 {
            t.Fatalf("expected non-empty value for key %s", key)
        }
    }
}

func TestComputeCacheKey_Consistent(t *testing.T) {
    t.Parallel()
    msgs := []map[string]string{{"role": "user", "content": "hello"}}
    temp := float64(0.7)
    maxTok := 100
    topP := float64(0.9)

    key1 := ComputeCacheKey("model-a", msgs, &temp, &maxTok, &topP)
    key2 := ComputeCacheKey("model-a", msgs, &temp, &maxTok, &topP)

    if key1 != key2 {
        t.Fatal("expected same inputs to produce same key")
    }
}

func TestComputeCacheKey_DifferentModels(t *testing.T) {
    t.Parallel()
    msgs := []map[string]string{{"role": "user", "content": "hello"}}
    temp := float64(0.7)

    key1 := ComputeCacheKey("model-a", msgs, &temp, nil, nil)
    key2 := ComputeCacheKey("model-b", msgs, &temp, nil, nil)

    if key1 == key2 {
        t.Fatal("expected different models to produce different keys")
    }
}
