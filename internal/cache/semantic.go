package cache

// Semantic cache for v0.5.0 Task #70.
// Importers: internal/server/server.go handleChatCompletions/handleStreamChat.
// User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md: "semantic cache, similarity threshold, local vector store".
// Schema: SemanticEntry(embedding/response/model/timestamp), SemanticCache with cosine similarity search.
// API: NewSemanticCache, Search, Store, Stats.

import (
    "encoding/json"
    "log/slog"
    "math"
    "sync"
    "sync/atomic"
    "time"

    "context"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

type SemanticEntry struct {
    ID        string          `json:"id"`
    Embedding []float64       `json:"embedding"`
    Prompt    string          `json:"prompt"`
    Response  json.RawMessage `json:"response"`
    Model     string          `json:"model"`
    Timestamp time.Time       `json:"timestamp"`
}

type SemanticCache struct {
    mu         sync.RWMutex
    entries    []*SemanticEntry
    threshold  float64
    maxEntries int
    ttl        time.Duration
    // EI2: hits/misses are atomic counters, NOT plain int64. Search runs under
    // RLock (concurrent readers), so a `hits++` RMW would be a data race — two
    // readers both read+write the same int64, lost increments + race detector
    // failure. atomic.Int64 makes the increment lock-free and race-free while
    // keeping the RLock for the entries slice scan.
    hits   atomic.Int64
    misses atomic.Int64
    embedFn EmbedFunc
    // R1: lifecycle.Worker wrapping the eviction goroutine so Server.Shutdown
    // can Stop (cancel + join) it. nil when semantic cache disabled.
    evictWorker *lifecycle.Worker
    // E4 (audit): cache of prompt to embedding so the same prompt is embedded
    // once, not on every Search AND again on every Store. The audit found
    // Search and Store each call embedFn on the same text = double embedding,
    // with no embedding cache at all. embedFn is upstream MLX/cloud inference
    // — deduping avoids re-running it for repeated prompts. Bounded to
    // maxEntries so distinct query prompts cannot grow it without limit.
    embedCache   map[string][]float64
    embedCacheMu sync.Mutex
}

// Close stops the background eviction goroutine (R1). Idempotent; safe to call
// from Server.Shutdown. Before R1 the eviction ticker leaked on shutdown.
func (sc *SemanticCache) Close() {
    if sc == nil || sc.evictWorker == nil {
        return
    }
    sc.evictWorker.Stop()
    sc.evictWorker = nil
}

type EmbedFunc func(text string) ([]float64, error)

func NewSemanticCache(cfg config.SemanticCacheConfig, embedFn EmbedFunc) *SemanticCache {
    if !cfg.Enabled {
        return nil
    }
    threshold := cfg.SimilarityThreshold
    if threshold <= 0 {
        threshold = 0.92
    }
    maxEntries := cfg.MaxEntries
    if maxEntries <= 0 {
        maxEntries = 5000
    }
    if embedFn == nil {
        slog.Error("semantic cache requires embedding function but none provided, disabling semantic cache")
        return nil
    }
    sc := &SemanticCache{
        entries:     make([]*SemanticEntry, 0),
        threshold:   threshold,
        maxEntries:  maxEntries,
        ttl:         30 * time.Minute,
        embedFn:     embedFn,
        embedCache:  make(map[string][]float64),
    }
    // R1: launch eviction through lifecycle.Worker so Shutdown can Stop (cancel
    // + join) it instead of leaking. H3 panic-restart inherited from Worker.
    sc.evictWorker = lifecycle.Start(context.Background(), "semantic_evict_expired", sc.evictExpired)
    slog.Info("semantic cache initialized", "threshold", threshold, "max_entries", maxEntries)
    return sc
}



func cosineSimilarity(a, b []float64) float64 {
    if len(a) != len(b) {
        return 0
    }
    var dot, normA, normB float64
    for i := range a {
        dot += a[i] * b[i]
        normA += a[i] * a[i]
        normB += b[i] * b[i]
    }
    if normA == 0 || normB == 0 {
        return 0
    }
    return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// embeddingFor returns the embedding for prompt, computing it once and caching
// it so repeat prompts (a Search miss followed by a Store of the same prompt,
// or repeated Searches of a popular query) do NOT re-invoke the upstream
// embedFn. E4 (audit): Search and Store each called embedFn directly = the
// same text embedded twice across the miss-then-store path; embedFn is upstream
// inference, so that was CPU + inference slot amplification with no dedup. The
// cache is bounded to maxEntries — on overflow the oldest half is dropped, so
// distinct query prompts cannot grow it without limit.
func (sc *SemanticCache) embeddingFor(prompt string) ([]float64, error) {
    sc.embedCacheMu.Lock()
    if vec, ok := sc.embedCache[prompt]; ok {
        sc.embedCacheMu.Unlock()
        return vec, nil
    }
    sc.embedCacheMu.Unlock()
    vec, err := sc.embedFn(prompt)
    if err != nil {
        return nil, err
    }
    sc.embedCacheMu.Lock()
    defer sc.embedCacheMu.Unlock()
    if len(sc.embedCache) >= sc.maxEntries {
        // E4: bound the embedding cache by evicting half on overflow (same
        // bound as the response store). Iteration order is non-deterministic,
        // which is fine — the goal is to cap size, not LRU-evict precisely.
        drop := len(sc.embedCache) / 2
        n := 0
        for k := range sc.embedCache {
            delete(sc.embedCache, k)
            n++
            if n >= drop {
                break
            }
        }
        slog.Debug("semantic cache embed cache overflow, evicted half", "dropped", n, "remaining", len(sc.embedCache))
    }
    sc.embedCache[prompt] = vec
    return vec, nil
}

func (sc *SemanticCache) Search(prompt string, model string) (json.RawMessage, bool) {
    if sc == nil {
        return nil, false
    }
    embedding, err := sc.embeddingFor(prompt)
    if err != nil {
        slog.Warn("semantic cache embedding failed", "error", err)
        sc.misses.Add(1)
        return nil, false
    }
    sc.mu.RLock()
    defer sc.mu.RUnlock()
    var bestEntry *SemanticEntry
    bestSim := 0.0
    for _, e := range sc.entries {
        if e.Model != model {
            continue
        }
        sim := cosineSimilarity(embedding, e.Embedding)
        if sim > bestSim {
            bestSim = sim
            bestEntry = e
        }
    }
    if bestEntry != nil && bestSim >= sc.threshold {
        if time.Since(bestEntry.Timestamp) > sc.ttl {
            sc.misses.Add(1)
            return nil, false
        }
        sc.hits.Add(1)
        slog.Debug("semantic cache hit", "similarity", bestSim, "model", model)
        return bestEntry.Response, true
    }
    sc.misses.Add(1)
    slog.Debug("semantic cache miss", "best_similarity", bestSim, "threshold", sc.threshold)
    return nil, false
}

func (sc *SemanticCache) Store(prompt string, model string, response json.RawMessage) {
    if sc == nil {
        return
    }
    embedding, err := sc.embeddingFor(prompt)
    if err != nil {
        slog.Warn("semantic cache embedding failed for store", "error", err)
        return
    }
    sc.mu.Lock()
    defer sc.mu.Unlock()
    // P2 fix: evict oldest entries without slice head-delete memory leak
    for len(sc.entries) >= sc.maxEntries {
        copy(sc.entries, sc.entries[1:])
        sc.entries[len(sc.entries)-1] = nil
        sc.entries = sc.entries[:len(sc.entries)-1]
    }
    entry := &SemanticEntry{
        ID:        ComputeCacheKey(model, prompt, nil, nil, nil),
        Embedding: embedding,
        Prompt:    prompt,
        Response:  response,
        Model:     model,
        Timestamp: time.Now(),
    }
    sc.entries = append(sc.entries, entry)
    slog.Debug("semantic cache stored", "model", model, "prompt_len", len(prompt))
}

func (sc *SemanticCache) Stats() (hits, misses int64, size int) {
    if sc == nil {
        return 0, 0, 0
    }
    sc.mu.RLock()
    defer sc.mu.RUnlock()
    // EI2: hits/misses are atomic.Int64 — Load() is the race-free read. The
    // RLock stays for len(sc.entries) (the slice header is guarded by the
    // mutex); the counters do not need it.
    return sc.hits.Load(), sc.misses.Load(), len(sc.entries)
}

func (sc *SemanticCache) evictExpired(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            // R1: honor shutdown so eviction exits and Shutdown joins it.
            return
        case <-ticker.C:
        }
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
        if evicted > 0 {
            slog.Debug("semantic cache evicted expired", "count", evicted)
        }
    }
}
