package cache

import (
    "encoding/json"
    "errors"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestSemanticCache_NilEmbedFn(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true}
    sc := NewSemanticCache(cfg, nil)
    if sc != nil {
        t.Fatal("should be nil when embedFn is nil")
    }
}

func TestSemanticCache_NilReceiver_Search(t *testing.T) {
    var sc *SemanticCache
    _, ok := sc.Search("test", "model")
    if ok {
        t.Fatal("nil cache should miss")
    }
}

func TestSemanticCache_NilReceiver_Store(t *testing.T) {
    var sc *SemanticCache
    sc.Store("test", "model", json.RawMessage(`{}`))
}

func TestSemanticCache_NilReceiver_Stats(t *testing.T) {
    var sc *SemanticCache
    hits, misses, size := sc.Stats()
    if hits != 0 || misses != 0 || size != 0 {
        t.Fatalf("expected 0/0/0, got %d/%d/%d", hits, misses, size)
    }
}

func TestSemanticCache_EmbeddingError(t *testing.T) {
    errFn := func(text string) ([]float64, error) {
        return nil, errors.New("embed failed")
    }
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.9}
    sc := NewSemanticCache(cfg, errFn)

    _, ok := sc.Search("hello", "gpt-4")
    if ok {
        t.Fatal("should miss on embedding error")
    }

    sc.Store("hello", "gpt-4", json.RawMessage(`{}`))
}

func TestSemanticCache_DifferentModel(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.5}
    sc := NewSemanticCache(cfg, testEmbedFn)
    resp, _ := json.Marshal(map[string]string{"content": "test"})
    sc.Store("hello", "gpt-4", resp)
    _, ok := sc.Search("hello", "claude-3")
    if ok {
        t.Fatal("should miss on different model")
    }
}

func TestSemanticCache_MaxEntriesEviction(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.0, MaxEntries: 2}
    sc := NewSemanticCache(cfg, testEmbedFn)
    resp, _ := json.Marshal(map[string]string{"content": "r"})
    sc.Store("p1", "gpt-4", resp)
    sc.Store("p2", "gpt-4", resp)
    sc.Store("p3", "gpt-4", resp)
    _, _, size := sc.Stats()
    if size != 2 {
        t.Fatalf("expected max 2 entries, got %d", size)
    }
}

func TestSemanticCache_DefaultThreshold(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0}
    sc := NewSemanticCache(cfg, testEmbedFn)
    if sc.threshold != 0.92 {
        t.Fatalf("expected default 0.92, got %f", sc.threshold)
    }
}

func TestSemanticCache_DefaultMaxEntries(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, MaxEntries: 0}
    sc := NewSemanticCache(cfg, testEmbedFn)
    if sc.maxEntries != 5000 {
        t.Fatalf("expected default 5000, got %d", sc.maxEntries)
    }
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
    a := []float64{1, 0}
    b := []float64{1, 0, 0}
    sim := cosineSimilarity(a, b)
    if sim != 0 {
        t.Fatalf("different length vectors should return 0, got %f", sim)
    }
}

func TestCosineSimilarity_ZeroVectors(t *testing.T) {
    a := []float64{0, 0, 0}
    b := []float64{1, 0, 0}
    sim := cosineSimilarity(a, b)
    if sim != 0 {
        t.Fatalf("zero vector should return 0, got %f", sim)
    }
}

func TestComputeCacheKey_NilParams(t *testing.T) {
    key := ComputeCacheKey("model", nil, nil, nil, nil)
    if key == "" {
        t.Fatal("expected non-empty key")
    }
}
