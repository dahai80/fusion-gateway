package cache

import (
    "encoding/json"
    "math"
    "sync"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func testEmbedFn(text string) ([]float64, error) {
    dims := 64
    vec := make([]float64, dims)
    for i, ch := range text {
        idx := i % dims
        vec[idx] += float64(ch)
    }
    // pad sparse dims with small deterministic values to ensure full coverage
    for i := range vec {
        if vec[i] == 0 {
            vec[i] = float64(i+1) * 0.01
        }
    }
    var norm float64
    for _, v := range vec {
        norm += v * v
    }
    norm = math.Sqrt(norm)
    if norm > 0 {
        for i := range vec {
            vec[i] /= norm
        }
    }
    return vec, nil
}

func TestSemanticCache_Disabled(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: false}
    sc := NewSemanticCache(cfg, testEmbedFn)
    if sc != nil {
        t.Fatal("should be nil when disabled")
    }
}

func TestSemanticCache_SearchMiss(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.92}
    sc := NewSemanticCache(cfg, testEmbedFn)
    _, ok := sc.Search("hello world", "gpt-4")
    if ok {
        t.Fatal("should miss on empty cache")
    }
}

func TestSemanticCache_StoreAndSearch(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.5}
    sc := NewSemanticCache(cfg, testEmbedFn)
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
    sc := NewSemanticCache(cfg, testEmbedFn)
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
    v, _ := testEmbedFn("hello")
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

// TestEI2_SearchConcurrentCountersRaceFree drives many concurrent Search
// calls (each does misses.Add(1) under RLock, the old RMW site) and reads
// Stats concurrently, then asserts the miss count is exact. Under the old
// plain-int64 `sc.misses++` this lost increments AND tripped `go test -race`
// (concurrent readers both RMW the int64). With atomic.Int64 it is race-free
// and the count is exact: every Search that misses increments exactly once.
// Run with -race to catch the data race the bug produced.
func TestEI2_SearchConcurrentCountersRaceFree(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.99}
    sc := NewSemanticCache(cfg, testEmbedFn)
    // Pre-store one entry so half the searches hit (hits.Add) and half miss
    // (misses.Add) — exercises both atomic counters concurrently.
    resp, _ := json.Marshal(map[string]string{"content": "cached"})
    sc.Store("what is Go?", "gpt-4", resp)

    const goroutines = 64
    const iters = 200
    var wg sync.WaitGroup
    // searchers: alternate hit/miss prompts
    for g := 0; g < goroutines; g++ {
        wg.Add(1)
        go func(g int) {
            defer wg.Done()
            for i := 0; i < iters; i++ {
                prompt := "what is Go?"
                if (g+i)%2 == 0 {
                    prompt = "completely different prompt " + testEmbedFnKey(g, i)
                }
                sc.Search(prompt, "gpt-4")
            }
        }(g)
    }
    // stats reader: concurrent Stats() while counters mutate
    var swg sync.WaitGroup
    for s := 0; s < 8; s++ {
        swg.Add(1)
        go func() {
            defer swg.Done()
            for i := 0; i < iters; i++ {
                _, _, _ = sc.Stats()
            }
        }()
    }
    wg.Wait()
    swg.Wait()

    hits, misses, size := sc.Stats()
    if size != 1 {
        t.Fatalf("expected 1 stored entry, got %d", size)
    }
    total := hits + misses
    expected := int64(goroutines * iters)
    if total != expected {
        t.Fatalf("EI2: counter lost increments — expected %d total (hits+misses), got hits=%d misses=%d total=%d; non-atomic RMW under RLock dropped counts", expected, hits, misses, total)
    }
}

// testEmbedFnKey returns a deterministic distinct string per (g,i) so miss
// prompts rarely collide (keeps the miss count predictable for the assertion).
func testEmbedFnKey(g, i int) string {
    return string(rune('A'+(g%26))) + string(rune('a'+(i%26)))
}
