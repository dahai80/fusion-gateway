package cache

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewBackend_Noop(t *testing.T) {
    b := NewBackend(nil, nil)
    if b == nil {
        t.Fatal("expected non-nil noop backend")
    }
    _, ok := b.Get("any")
    if ok {
        t.Fatal("noop should always miss")
    }
    b.Set("k", []byte("v"), time.Minute)
    b.Delete("k")
    hits, misses, size := b.Stats()
    if hits != 0 || misses != 0 || size != 0 {
        t.Fatalf("noop stats should be zero: %d/%d/%d", hits, misses, size)
    }
    b.Close()
}

func TestNewBackend_Local(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    b.Set("key1", []byte("val1"), time.Minute)
    val, ok := b.Get("key1")
    if !ok || string(val) != "val1" {
        t.Fatalf("expected val1, got %s ok=%v", val, ok)
    }
    b.Delete("key1")
    _, ok = b.Get("key1")
    if ok {
        t.Fatal("expected miss after delete")
    }
    b.Close()
}

func TestNewBackend_Tiered(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    mockL2 := &mockCacheBackend{}
    b := NewBackend(c, mockL2)

    b.Set("k1", []byte("v1"), time.Minute)
    if mockL2.lastSetKey != "" {
        t.Logf("L2 set called with key: %s", mockL2.lastSetKey)
    }

    val, ok := b.Get("k1")
    if !ok || string(val) != "v1" {
        t.Fatalf("expected v1 from L1, got %s ok=%v", val, ok)
    }

    b.Delete("k1")
    if mockL2.lastDeleteKey != "k1" {
        t.Fatalf("expected L2 delete k1, got %s", mockL2.lastDeleteKey)
    }

    b.Close()
    if !mockL2.closed {
        t.Fatal("expected L2 to be closed")
    }
}

func TestNewBackend_Tiered_L2Fallback(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    mockL2 := &mockCacheBackend{
        data: map[string][]byte{"l2key": []byte("l2val")},
    }
    b := NewBackend(c, mockL2)

    val, ok := b.Get("l2key")
    if !ok || string(val) != "l2val" {
        t.Fatalf("expected l2val from L2 fallback, got %s ok=%v", val, ok)
    }

    val2, ok2 := c.Get("l2key")
    if !ok2 || string(val2) != "l2val" {
        t.Fatal("expected L1 to be populated from L2")
    }
}

type mockCacheBackend struct {
    data           map[string][]byte
    lastSetKey     string
    lastDeleteKey  string
    closed         bool
}

func (m *mockCacheBackend) Get(key string) ([]byte, bool) {
    v, ok := m.data[key]
    return v, ok
}

func (m *mockCacheBackend) Set(key string, value []byte, ttl time.Duration) {
    m.lastSetKey = key
    if m.data == nil {
        m.data = make(map[string][]byte)
    }
    m.data[key] = value
}

func (m *mockCacheBackend) Delete(key string) {
    m.lastDeleteKey = key
    delete(m.data, key)
}

func (m *mockCacheBackend) Stats() (hits, misses int64, size int) {
    return 0, 0, len(m.data)
}

func (m *mockCacheBackend) Close() {
    m.closed = true
}

func TestCache_UpdateConfig_MaxEntries(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 10, TTL: time.Minute})
    for i := 0; i < 10; i++ {
        c.Set(string(rune('a'+i)), []byte("v"))
    }
    _, _, size := c.Stats()
    if size != 10 {
        t.Fatalf("expected 10, got %d", size)
    }
    c.UpdateConfig(config.CacheConfig{MaxEntries: 5})
    _, _, size = c.Stats()
    if size != 5 {
        t.Fatalf("expected 5 after eviction, got %d", size)
    }
}

func TestCache_UpdateConfig_MaxMemoryMB(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    c.Set("big", make([]byte, 1024*1024))
    c.UpdateConfig(config.CacheConfig{MaxMemoryMB: 0})
    c.UpdateConfig(config.CacheConfig{MaxMemoryMB: 1})
}

func TestCache_UpdateConfig_TTL(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    c.UpdateConfig(config.CacheConfig{TTL: 2 * time.Minute})
}

func TestCache_Delete_NonExistent(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    c.Delete("nonexistent")
}

func TestCache_NilReceiver(t *testing.T) {
    var c *Cache
    _, ok := c.Get("k")
    if ok {
        t.Fatal("nil cache should miss")
    }
    c.Set("k", []byte("v"))
    c.Delete("k")
    hits, misses, size := c.Stats()
    if hits != 0 || misses != 0 || size != 0 {
        t.Fatalf("nil cache stats should be zero")
    }
    c.UpdateConfig(config.CacheConfig{})
}

func TestCache_MaxMemoryEviction(t *testing.T) {
    c := New(config.CacheConfig{
        Enabled:     true,
        MaxEntries:  100,
        TTL:         time.Minute,
        MaxMemoryMB: 1,
    })
    c.Set("big1", make([]byte, 512*1024))
    c.Set("big2", make([]byte, 512*1024))
    c.Set("big3", make([]byte, 512*1024))
    _, _, size := c.Stats()
    if size >= 3 {
        t.Fatal("expected some eviction due to memory limit")
    }
}

func TestCache_SetOverwrite(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    c.Set("k", []byte("v1"))
    c.Set("k", []byte("v2"))
    val, ok := c.Get("k")
    if !ok || string(val) != "v2" {
        t.Fatalf("expected v2 after overwrite, got %s", val)
    }
    _, _, size := c.Stats()
    if size != 1 {
        t.Fatalf("expected size 1, got %d", size)
    }
}
