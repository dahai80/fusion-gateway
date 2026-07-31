package cache

import (
    "encoding/json"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestSemanticCache_Disabled(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: false}
    sc := NewSemanticCache(cfg, nil)
    if sc != nil {
        t.Fatal("should be nil when disabled")
    }
}

func TestSemanticCache_SearchMiss(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.92}
    sc := NewSemanticCache(cfg, nil)
    _, ok := sc.Search("hello world", "gpt-4")
    if ok {
        t.Fatal("should miss on empty cache")
    }
}

func TestSemanticCache_StoreAndSearch(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.5}
    sc := NewSemanticCache(cfg, nil)
    resp, _ := json.Marshal(map[string]string{"content": "test response"})
    sc.Store("what is Go?", "gpt-4", resp)
    result, ok := sc.Search("what is Go?", "gpt-4")
    if !ok {
        t.Fatal("should hit on same prompt")
    }
    if string(result) != string(resp) {
        t.Fatalf("response mismatch: %s", string(result))
    }
}

func TestSemanticCache_Stats(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.92}
    sc := NewSemanticCache(cfg, nil)
    sc.Search("test", "gpt-4")
    hits, misses, size := sc.Stats()
    if hits != 0 || misses != 1 || size != 0 {
        t.Fatalf("expected 0/1/0 got %d/%d/%d", hits, misses, size)
    }
}

func TestCosineSimilarity(t *testing.T) {
    a := []float64{1, 0, 0}
    b := []float64{1, 0, 0}
    if sim := cosineSimilarity(a, b); sim != 1.0 {
        t.Fatalf("identical vectors should have similarity 1.0, got %f", sim)
    }
    c := []float64{0, 1, 0}
    if sim := cosineSimilarity(a, c); sim != 0.0 {
        t.Fatalf("orthogonal vectors should have similarity 0.0, got %f", sim)
    }
}

func TestSimpleHashEmbedding(t *testing.T) {
    v := simpleHashEmbedding("hello", 64)
    if len(v) != 64 {
        t.Fatalf("expected 64 dims, got %d", len(v))
    }
    norm := 0.0
    for _, x := range v {
        norm += x * x
    }
    if norm < 0.99 || norm > 1.01 {
        t.Fatalf("embedding not normalized: %f", norm)
    }
}
