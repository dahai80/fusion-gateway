package cache

import (
    "encoding/json"
    "sync/atomic"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// countingEmbedFn wraps testEmbedFn with an atomic counter so a guard can
// assert exactly how many times the upstream embedFn was invoked.
func countingEmbedFn(counter *int64) EmbedFunc {
    return func(text string) ([]float64, error) {
        atomic.AddInt64(counter, 1)
        return testEmbedFn(text)
    }
}

// TestE4_SearchThenStoreEmbedsOnce: the audit found Search and Store each
// called embedFn on the same prompt = double embedding on the miss-then-store
// path (the normal cache-fill path). embedFn is upstream MLX/cloud inference,
// so double embedding is CPU + inference-slot amplification with no dedup.
// With the E4 embedding cache, Search(miss) computes+cache the embedding,
// Store of the same prompt reuses it → embedFn called ONCE total. Revert
// (Store calls embedFn directly instead of embeddingFor): embedFn called twice
// → FAIL.
func TestE4_SearchThenStoreEmbedsOnce(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.5, MaxEntries: 100}
    var calls int64
    sc := NewSemanticCache(cfg, countingEmbedFn(&calls))
    if sc == nil {
        t.Fatal("NewSemanticCache returned nil for enabled cfg with embedFn")
    }

    // Search misses (empty cache) — first embedFn call.
    if _, ok := sc.Search("what is Go?", "gpt-4"); ok {
        t.Fatal("expected miss on empty cache")
    }
    // Store the response for the same prompt — must NOT embed again.
    resp, _ := json.Marshal(map[string]string{"content": "Go is a language"})
    sc.Store("what is Go?", "gpt-4", resp)

    got := atomic.LoadInt64(&calls)
    if got != 1 {
        t.Fatalf("E4: double embedding — Search(miss)+Store(same prompt) must embed ONCE (dedup via embedding cache), got %d embedFn calls; pre-E4 Search and Store each called embedFn = 2 calls on the cache-fill path", got)
    }

    // A second Search of the same prompt (now a hit) must NOT embed again.
    if _, ok := sc.Search("what is Go?", "gpt-4"); !ok {
        t.Fatal("expected hit after Store")
    }
    got = atomic.LoadInt64(&calls)
    if got != 1 {
        t.Fatalf("E4: repeat Search of a cached prompt must reuse the embedding (0 new calls), got %d total", got)
    }
}

// TestE4_DistinctPromptsEmbedSeparately: the embedding cache dedups by prompt
// text — distinct prompts each embed once. Guards against an over-eager cache
// (e.g. caching by model or returning a stale vector) that would corrupt
// similarity search.
func TestE4_DistinctPromptsEmbedSeparately(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.5, MaxEntries: 100}
    var calls int64
    sc := NewSemanticCache(cfg, countingEmbedFn(&calls))

    sc.Search("what is Go?", "gpt-4")
    sc.Search("how does Rust handle borrows?", "gpt-4")

    got := atomic.LoadInt64(&calls)
    if got != 2 {
        t.Fatalf("E4: distinct prompts must each embed once, got %d embedFn calls for 2 distinct prompts", got)
    }
}

// TestE4_EmbedCacheBounded: the embedding cache is bounded to maxEntries so a
// flood of distinct query prompts cannot grow it without limit (the audit's
// "无 embedding 缓存" concern cut both ways — unbounded caching would be a
// memory leak). After maxEntries+overflow distinct prompts the cache must not
// exceed its cap.
func TestE4_EmbedCacheBounded(t *testing.T) {
    cfg := config.SemanticCacheConfig{Enabled: true, SimilarityThreshold: 0.5, MaxEntries: 10}
    var calls int64
    sc := NewSemanticCache(cfg, countingEmbedFn(&calls))

    // Flood 3x maxEntries distinct prompts.
    for i := 0; i < 30; i++ {
        prompt := "distinct prompt number " + string(rune('A'+i%26)) + string(rune('a'+i))
        sc.Search(prompt, "gpt-4")
    }

    sc.embedCacheMu.Lock()
    size := len(sc.embedCache)
    sc.embedCacheMu.Unlock()
    // maxEntries is 10; overflow evicts half (5) at each crossing, so the
    // cache stays at or below the cap — never unbounded growth.
    if size > cfg.MaxEntries {
        t.Fatalf("E4: embedding cache grew beyond maxEntries (%d) to %d — overflow eviction not bounding it", cfg.MaxEntries, size)
    }
}
