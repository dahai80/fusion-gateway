package cache

// #423: the system-prompt hash is an exact-match precondition for a semantic
// hit. The similarity check runs on the USER payload embedding only; the
// system prompt (task instructions, JSON schema — identical across agent
// calls) previously dominated the embedding, letting two different payloads
// under the same agent prompt collide at ~0.96 similarity.

import (
    "encoding/json"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func newSystemHashTestCache(t *testing.T) *SemanticCache {
    t.Helper()
    sc := NewSemanticCache(config.SemanticCacheConfig{
        Enabled:             true,
        SimilarityThreshold: 0.95,
        MaxEntries:          100,
    }, func(prompt string) ([]float64, error) {
        return []float64{1, 0, 0}, nil
    })
    if sc == nil {
        t.Fatal("semantic cache must be enabled in this test")
    }
    t.Cleanup(sc.Close)
    return sc
}

func TestSemanticSearchSystemHashMismatch(t *testing.T) {
    sc := newSystemHashTestCache(t)
    resp := json.RawMessage(`{"stored":true}`)
    sc.Store("hello alpha payload", "hash-a", "gpt-4", resp)
    // Same payload embedding (identical vector), different system hash: the
    // entry must be invisible — cosine 1.0 cannot cross the hash filter.
    if _, ok := sc.Search("hello alpha payload", "hash-b", "gpt-4"); ok {
        t.Fatal("entry stored under hash-a must not match a search under hash-b")
    }
    if _, ok := sc.Search("hello alpha payload", "hash-a", "gpt-4"); !ok {
        t.Fatal("entry must still match under its own system hash")
    }
}

func TestSemanticStoreKeepsSystemHash(t *testing.T) {
    sc := newSystemHashTestCache(t)
    sc.Store("prompt", "hash-x", "gpt-4", json.RawMessage(`{}`))
    sc.mu.RLock()
    e := sc.entries.Front().Value.(*SemanticEntry)
    sc.mu.RUnlock()
    if e.SystemHash != "hash-x" {
        t.Fatalf("stored entry must carry the system hash, got %q", e.SystemHash)
    }
}
