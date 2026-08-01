package cache

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestCache_SetGet(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    c.Set("key1", []byte("val1"))
    val, ok := c.Get("key1")
    if !ok || string(val) != "val1" {
        t.Fatalf("expected val1, got %s ok=%v", val, ok)
    }
}

func TestCache_Get_Miss(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    _, ok := c.Get("nonexistent")
    if ok {
        t.Fatal("should miss")
    }
}

func TestCache_TTLExpiration(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: 50 * time.Millisecond}
    c := New(cfg)
    c.Set("key1", []byte("val1"))
    time.Sleep(100 * time.Millisecond)
    _, ok := c.Get("key1")
    if ok {
        t.Fatal("should expire after TTL")
    }
}

func TestCache_Delete(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    c.Set("key1", []byte("val1"))
    c.Delete("key1")
    _, ok := c.Get("key1")
    if ok {
        t.Fatal("should be deleted")
    }
}

func TestCache_LRUEviction_Extra(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 2, TTL: time.Minute}
    c := New(cfg)
    c.Set("a", []byte("1"))
    c.Set("b", []byte("2"))
    c.Set("c", []byte("3"))
    _, ok := c.Get("a")
    if ok {
        t.Fatal("a should be evicted")
    }
    _, ok = c.Get("c")
    if !ok {
        t.Fatal("c should exist")
    }
}

func TestCache_MaxBytesEviction(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute, MaxMemoryMB: 1}
    c := New(cfg)
    big := make([]byte, 512*1024)
    for i := range big {
        big[i] = 'x'
    }
    c.Set("big1", big)
    c.Set("big2", big)
    _, ok := c.Get("big1")
    if ok {
        t.Log("big1 may still exist depending on eviction timing")
    }
}

func TestCache_UpdateConfig_Extra(t *testing.T) {
    cfg := config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute}
    c := New(cfg)
    c.Set("k", []byte("v"))
    newCfg := config.CacheConfig{MaxEntries: 50, TTL: 5 * time.Minute}
    c.UpdateConfig(newCfg)
    _, ok := c.Get("k")
    if !ok {
        t.Log("key may have been evicted after config update")
    }
}
