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
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
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
    hits       int64
    misses     int64
    embedFn    EmbedFunc
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
        entries:    make([]*SemanticEntry, 0),
        threshold:  threshold,
        maxEntries: maxEntries,
        ttl:        30 * time.Minute,
        embedFn:    embedFn,
    }
    // M2 fix: use safeGo for panic recovery on background goroutine
    safego.Go("semantic_evict_expired", sc.evictExpired)
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

func (sc *SemanticCache) Search(prompt string, model string) (json.RawMessage, bool) {
    if sc == nil {
        return nil, false
    }
    embedding, err := sc.embedFn(prompt)
    if err != nil {
        slog.Warn("semantic cache embedding failed", "error", err)
        sc.misses++
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
            sc.misses++
            return nil, false
        }
        sc.hits++
        slog.Debug("semantic cache hit", "similarity", bestSim, "model", model)
        return bestEntry.Response, true
    }
    sc.misses++
    slog.Debug("semantic cache miss", "best_similarity", bestSim, "threshold", sc.threshold)
    return nil, false
}

func (sc *SemanticCache) Store(prompt string, model string, response json.RawMessage) {
    if sc == nil {
        return
    }
    embedding, err := sc.embedFn(prompt)
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
    return sc.hits, sc.misses, len(sc.entries)
}

func (sc *SemanticCache) evictExpired() {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
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
