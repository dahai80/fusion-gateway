package server

// #400: SemanticCache wiring. NewSemanticCache was always called with a nil
// EmbedFunc, so semantic_cache.enabled: true could never activate the cache —
// every boot logged a WARN and every request took the full round trip.
//
// The EmbedFunc is now derived from the adapter pool: semantic_cache.provider
// names a backends: entry (default "fusion-mlx") whose Embedding() call
// supplies prompt embeddings, using that provider's existing auth/transport/
// slot accounting. semantic_cache.model selects the embedding model id (e.g.
// BAAI--bge-m3 served by fusion-mlx). An enabled cache with no model (or a
// missing provider) stays disabled with a WARN that names the fix.

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// semanticPromptCap bounds the text sent to the embedding provider. Prompts
// beyond it skip the semantic cache: the embedding cost approaches the
// generation it would save, and a truncation would change the similarity
// semantics silently.
const semanticPromptCap = 16000

// semanticEmbedTimeout bounds one embedding round trip. The cache fails open
// (treated as a miss / skipped store), so a slow provider must not stall the
// request path beyond this.
const semanticEmbedTimeout = 10 * time.Second

// buildSemanticEmbedFn returns the EmbedFunc for the semantic cache, or nil
// when the cache cannot be wired. nil is safe: NewSemanticCache turns it into
// a disabled cache with a WARN. Provider defaults to the local fusion-mlx
// backend (local-first); Model has no default — an embedding model id must be
// named explicitly so the cache never silently embeds with the wrong model.
func buildSemanticEmbedFn(scCfg config.SemanticCacheConfig, pool *adapter.Pool) cache.EmbedFunc {
    if !scCfg.Enabled {
        return nil
    }
    providerName := scCfg.Provider
    if providerName == "" {
        providerName = "fusion-mlx"
    }
    if scCfg.Model == "" {
        slog.Warn("semantic cache disabled: semantic_cache.model is not set",
            "hint", "set semantic_cache.model to an embedding model id served by the provider (e.g. BAAI--bge-m3 on fusion-mlx), or set semantic_cache.enabled: false")
        return nil
    }
    if pool == nil {
        slog.Warn("semantic cache disabled: no adapter pool available")
        return nil
    }
    provider, ok := pool.Get(providerName)
    if !ok {
        slog.Warn("semantic cache disabled: semantic_cache.provider not found in backends",
            "provider", providerName,
            "hint", "set semantic_cache.provider to a backends entry name")
        return nil
    }
    slog.Info("semantic cache embedding provider wired",
        "provider", providerName, "model", scCfg.Model)
    return func(text string) ([]float64, error) {
        ctx, cancel := context.WithTimeout(context.Background(), semanticEmbedTimeout)
        defer cancel()
        resp, err := provider.Embedding(ctx, &adapter.EmbeddingRequest{
            Model: scCfg.Model,
            Input: []string{text},
        })
        if err != nil {
            return nil, fmt.Errorf("semantic embedding via %s failed: %w", providerName, err)
        }
        if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
            return nil, fmt.Errorf("semantic embedding via %s returned no vector", providerName)
        }
        return resp.Data[0].Embedding, nil
    }
}

// semanticCachePrompt returns the prompt text to embed for the semantic
// cache, or "" when the request must skip semantic caching: cache disabled,
// tool calls present (a stored tool_calls payload must not surface for a
// similar prompt), multimodal content (extractTextContent cannot see image
// blocks, and an answer to a different image must not be replayed), empty
// text, or a prompt over semanticPromptCap.
func (s *Server) semanticCachePrompt(req *adapter.ChatRequest) string {
    if s.semanticCache == nil || req == nil {
        return ""
    }
    if req.Tools != nil || chatRequestHasImage(req) {
        return ""
    }
    text := extractTextContent(req.Messages)
    if text == "" || len(text) > semanticPromptCap {
        if len(text) > semanticPromptCap {
            slog.Debug("semantic cache skipped: prompt over cap", "prompt_len", len(text), "cap", semanticPromptCap)
        }
        return ""
    }
    return text
}
