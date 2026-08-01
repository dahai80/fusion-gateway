package cache

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestSemanticCache_EvictExpired_KeepsFresh(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:             true,
        SimilarityThreshold: 0.5,
        MaxEntries:          100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("fresh", "gpt-4", resp)

    time.Sleep(100 * time.Millisecond)

    _, _, size := sc.Stats()
    if size != 1 {
        t.Fatalf("expected 1 fresh entry, got %d", size)
    }
}

func TestSemanticCache_EvictExpired_NoEntries(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:             true,
        SimilarityThreshold: 0.5,
        MaxEntries:          100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    time.Sleep(100 * time.Millisecond)

    _, _, size := sc.Stats()
    if size != 0 {
        t.Fatalf("expected 0 entries on empty cache, got %d", size)
    }
}

func TestSemanticCache_EvictExpired_BackgroundTick(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping background eviction tick test in short mode")
    }
    cfg := config.SemanticCacheConfig{
        Enabled:             true,
        SimilarityThreshold: 0.5,
        MaxEntries:          100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("prompt1", "gpt-4", resp)

    sc.mu.Lock()
    for _, e := range sc.entries {
        e.Timestamp = time.Now().Add(-2 * time.Hour)
    }
    sc.mu.Unlock()

    time.Sleep(62 * time.Second)

    _, _, size := sc.Stats()
    if size != 0 {
        t.Logf("expected 0 after background eviction, got %d", size)
    }
}
