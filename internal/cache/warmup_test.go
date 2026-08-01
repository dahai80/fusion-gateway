package cache

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestWarmupFromFile_EmptyPath(t *testing.T) {
    loaded := WarmupFromFile(nil, "")
    if loaded != 0 {
        t.Fatalf("expected 0, got %d", loaded)
    }
}

func TestWarmupFromFile_NilBackend(t *testing.T) {
    loaded := WarmupFromFile(nil, "somefile.json")
    if loaded != 0 {
        t.Fatalf("expected 0, got %d", loaded)
    }
}

func TestWarmupFromFile_NonExistentFile(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    loaded := WarmupFromFile(b, "/nonexistent/path/warmup.json")
    if loaded != 0 {
        t.Fatalf("expected 0 for missing file, got %d", loaded)
    }
}

func TestWarmupFromFile_InvalidJSON(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "bad.json")
    _ = os.WriteFile(f, []byte("not json"), 0644)

    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    loaded := WarmupFromFile(b, f)
    if loaded != 0 {
        t.Fatalf("expected 0 for invalid json, got %d", loaded)
    }
}

func TestWarmupFromFile_Valid(t *testing.T) {
    dir := t.TempDir()
    entries := []WarmupEntry{
        {Key: "warm1", Value: "val1"},
        {Key: "warm2", Value: "val2", TTL: "10m"},
    }
    data, _ := json.Marshal(entries)
    f := filepath.Join(dir, "warmup.json")
    _ = os.WriteFile(f, data, 0644)

    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    loaded := WarmupFromFile(b, f)
    if loaded != 2 {
        t.Fatalf("expected 2, got %d", loaded)
    }

    val, ok := b.Get("warm1")
    if !ok || string(val) != "val1" {
        t.Fatalf("expected val1, got %s ok=%v", val, ok)
    }
}

func TestWarmupFromFile_InvalidTTL(t *testing.T) {
    dir := t.TempDir()
    entries := []WarmupEntry{
        {Key: "k1", Value: "v1", TTL: "not-a-duration"},
    }
    data, _ := json.Marshal(entries)
    f := filepath.Join(dir, "warmup.json")
    _ = os.WriteFile(f, data, 0644)

    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    loaded := WarmupFromFile(b, f)
    if loaded != 1 {
        t.Fatalf("expected 1 (with default TTL), got %d", loaded)
    }
}
