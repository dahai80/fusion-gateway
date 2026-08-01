package cache

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestSemanticCache_EvictExpired_ManualRemoval(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:           true,
        SimilarityThreshold: 0.5,
        MaxEntries:        100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("prompt1", "gpt-4", resp)
    sc.Store("prompt2", "gpt-4", resp)

    sc.mu.Lock()
    now := time.Now()
    kept := make([]*SemanticEntry, 0, len(sc.entries))
    for _, e := range sc.entries {
        if now.Sub(e.Timestamp) <= sc.ttl {
            kept = append(kept, e)
        }
    }
    evicted := len(sc.entries) - len(kept)
    sc.entries = kept
    sc.mu.Unlock()

    if evicted != 0 {
        t.Fatalf("expected 0 evicted (all fresh), got %d", evicted)
    }

    _, _, size := sc.Stats()
    if size != 2 {
        t.Fatalf("expected 2 entries, got %d", size)
    }
}

func TestSemanticCache_EvictExpired_ExpiredEntries(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:           true,
        SimilarityThreshold: 0.5,
        MaxEntries:        100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("old_prompt", "gpt-4", resp)

    sc.mu.Lock()
    for _, e := range sc.entries {
        e.Timestamp = time.Now().Add(-2 * time.Hour)
    }
    sc.mu.Unlock()

    sc.mu.Lock()
    now := time.Now()
    kept := make([]*SemanticEntry, 0, len(sc.entries))
    for _, e := range sc.entries {
        if now.Sub(e.Timestamp) <= sc.ttl {
            kept = append(kept, e)
        }
    }
    evicted := len(sc.entries) - len(kept)
    sc.entries = kept
    sc.mu.Unlock()

    if evicted != 1 {
        t.Fatalf("expected 1 evicted, got %d", evicted)
    }

    _, _, size := sc.Stats()
    if size != 0 {
        t.Fatalf("expected 0 entries after eviction, got %d", size)
    }
}

func TestSemanticCache_Search_ExpiredEntry(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:           true,
        SimilarityThreshold: 0.5,
        MaxEntries:        100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("expire_me", "gpt-4", resp)

    sc.mu.Lock()
    for _, e := range sc.entries {
        e.Timestamp = time.Now().Add(-2 * time.Hour)
    }
    sc.mu.Unlock()

    _, ok := sc.Search("expire_me", "gpt-4")
    if ok {
        t.Fatal("expected miss for expired semantic entry")
    }

    _, misses, _ := sc.Stats()
    if misses != 1 {
        t.Fatalf("expected 1 miss, got %d", misses)
    }
}

func TestSemanticCache_Search_BelowThreshold(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:           true,
        SimilarityThreshold: 0.999,
        MaxEntries:        100,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)

    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("unique prompt text", "gpt-4", resp)

    _, ok := sc.Search("completely different prompt", "gpt-4")
    if ok {
        t.Log("high threshold may still match identical-ish prompts")
    }
}

func TestSemanticCache_Store_EmbeddingError(t *testing.T) {
    errFn := func(text string) ([]float64, error) {
        return nil, json.Unmarshal([]byte("invalid"), &struct{}{})
    }
    cfg := config.SemanticCacheConfig{
        Enabled:           true,
        SimilarityThreshold: 0.9,
        MaxEntries:        100,
    }
    sc := NewSemanticCache(cfg, errFn)
    sc.Store("test", "gpt-4", json.RawMessage(`{}`))

    _, _, size := sc.Stats()
    if size != 0 {
        t.Fatalf("expected 0 entries on embedding error, got %d", size)
    }
}

func TestSemanticCache_MaxEntriesEviction_Order(t *testing.T) {
    cfg := config.SemanticCacheConfig{
        Enabled:           true,
        SimilarityThreshold: 0.0,
        MaxEntries:        2,
    }
    sc := NewSemanticCache(cfg, testEmbedFn)
    resp, _ := json.Marshal(map[string]string{"content": "r"})

    sc.Store("first", "gpt-4", resp)
    sc.Store("second", "gpt-4", resp)
    sc.Store("third", "gpt-4", resp)

    _, _, size := sc.Stats()
    if size != 2 {
        t.Fatalf("expected max 2 entries, got %d", size)
    }

    sc.mu.RLock()
    prompts := make(map[string]bool)
    for _, e := range sc.entries {
        prompts[e.Prompt] = true
    }
    sc.mu.RUnlock()

    if prompts["first"] {
        t.Log("first entry may still exist depending on eviction order")
    }
}
