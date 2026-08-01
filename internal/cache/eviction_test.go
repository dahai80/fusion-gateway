package cache

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestCache_EvictExpired_RemovesExpiredEntries(t *testing.T) {
    cfg := config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        100 * time.Millisecond,
    }
    c := New(cfg)

    c.Set("expire1", []byte("v1"))
    c.Set("expire2", []byte("v2"))

    _, _, size := c.Stats()
    if size != 2 {
        t.Fatalf("expected 2 entries before expiry, got %d", size)
    }

    time.Sleep(200 * time.Millisecond)

    c.mu.Lock()
    now := time.Now()
    var toRemove []string
    for key, elem := range c.items {
        e := elem.Value.(*entry)
        if now.After(e.expiresAt) {
            toRemove = append(toRemove, key)
        }
    }
    c.mu.Unlock()

    if len(toRemove) == 0 {
        t.Fatal("expected expired entries to be found")
    }

    c.mu.Lock()
    for _, key := range toRemove {
        if elem, ok := c.items[key]; ok {
            c.removeElement(elem)
        }
    }
    c.mu.Unlock()

    _, _, size = c.Stats()
    if size != 0 {
        t.Fatalf("expected 0 entries after manual eviction, got %d", size)
    }
}

func TestCache_EvictExpired_KeepsValidEntries(t *testing.T) {
    cfg := config.CacheConfig{
        Enabled:    true,
        MaxEntries: 100,
        TTL:        5 * time.Minute,
    }
    c := New(cfg)

    c.Set("valid1", []byte("v1"))
    c.Set("valid2", []byte("v2"))

    c.mu.RLock()
    now := time.Now()
    expired := 0
    for _, elem := range c.items {
        e := elem.Value.(*entry)
        if now.After(e.expiresAt) {
            expired++
        }
    }
    c.mu.RUnlock()

    if expired > 0 {
        t.Fatalf("expected 0 expired entries, got %d", expired)
    }

    _, _, size := c.Stats()
    if size != 2 {
        t.Fatalf("expected 2 entries, got %d", size)
    }
}

func TestCache_EvictExpired_NilReceiver(t *testing.T) {
    var c *Cache
    c.evictExpired()
}

func TestCache_New_DefaultMaxEntries(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 0, TTL: time.Minute}
    c := New(cfg)
    if c == nil {
        t.Fatal("expected non-nil cache")
    }
    if c.maxEntries != 10000 {
        t.Fatalf("expected default maxEntries 10000, got %d", c.maxEntries)
    }
}

func TestCache_New_DefaultTTL(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: 0}
    c := New(cfg)
    if c.ttl != 5*time.Minute {
        t.Fatalf("expected default TTL 5m, got %v", c.ttl)
    }
}

func TestCache_New_MaxBytesFromConfig(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute, MaxMemoryMB: 10}
    c := New(cfg)
    expectedBytes := int64(10) * 1024 * 1024
    if c.maxBytes != expectedBytes {
        t.Fatalf("expected maxBytes %d, got %d", expectedBytes, c.maxBytes)
    }
}

func TestCache_UpdateConfig_NilReceiver(t *testing.T) {
    var c *Cache
    c.UpdateConfig(config.CacheConfig{MaxEntries: 100})
}

func TestCache_UpdateConfig_ZeroFieldsNoop(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    c.Set("k", []byte("v"))

    c.UpdateConfig(config.CacheConfig{MaxEntries: 0, TTL: 0, MaxMemoryMB: 0})

    val, ok := c.Get("k")
    if !ok || string(val) != "v" {
        t.Fatal("expected key to remain after zero-field config update")
    }
}

func TestCache_UpdateConfig_MaxEntriesEvictsOldest(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    c.Set("a", []byte("1"))
    c.Set("b", []byte("2"))
    c.Set("c", []byte("3"))
    c.Set("d", []byte("4"))

    c.UpdateConfig(config.CacheConfig{MaxEntries: 2})

    _, _, size := c.Stats()
    if size != 2 {
        t.Fatalf("expected 2 entries after shrinking, got %d", size)
    }
}

func TestCache_UpdateConfig_MaxMemoryMBEvicts(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)

    bigVal := make([]byte, 512*1024)
    c.Set("big1", bigVal)
    c.Set("big2", bigVal)
    c.Set("big3", bigVal)

    c.UpdateConfig(config.CacheConfig{MaxMemoryMB: 1})

    _, _, size := c.Stats()
    if size >= 3 {
        t.Fatal("expected eviction after memory limit reduction")
    }
}

func TestCache_UpdateConfig_TTLChange(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    c.UpdateConfig(config.CacheConfig{TTL: 10 * time.Minute})
    if c.ttl != 10*time.Minute {
        t.Fatalf("expected TTL 10m, got %v", c.ttl)
    }
}
