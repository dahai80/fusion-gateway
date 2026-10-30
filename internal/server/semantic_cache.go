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
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "log/slog"
    "net/http"
    "strings"
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

// semanticCacheKey is the #423 content-sensitive keying for the semantic
// cache. Payload is the embedded text (user/assistant turns only); SystemHash
// is the SHA-256 hex of the system-prompt text and is matched EXACTLY as a
// hit precondition — the system prompt (task instructions, JSON schema) is
// identical across agent calls and previously dominated the embedding, so
// two different payloads under the same agent prompt scored ~0.96 similarity
// and the second request got the first request's answer. Empty Payload means
// the request must skip semantic caching entirely.
type semanticCacheKey struct {
    Payload    string
    SystemHash string
}

// optsOutOfSemanticCache reports whether the client explicitly asked to
// bypass the semantic cache (#423 ask 2): X-Cache: no-store or
// Cache-Control: no-store on the request. Structured-output clients whose
// correctness cannot tolerate a near-match replay opt out per request; the
// bypass skips both Search and Store.
func optsOutOfSemanticCache(r *http.Request) bool {
    if r == nil {
        return false
    }
    if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Cache")), "no-store") {
        return true
    }
    for _, dir := range strings.Split(r.Header.Get("Cache-Control"), ",") {
        if strings.EqualFold(strings.TrimSpace(dir), "no-store") {
            return true
        }
    }
    return false
}

// semanticNoStoreKey carries the #423 opt-out decision from the entry handler
// (which holds the *http.Request) down to handleNonStreamChat (which does
// not — its signature predates this and is called from ~20 test sites).
type semanticNoStoreKey struct{}

// withSemanticCacheOptOut marks the context so the semantic cache is bypassed.
func withSemanticCacheOptOut(ctx context.Context) context.Context {
    return context.WithValue(ctx, semanticNoStoreKey{}, true)
}

// semanticCacheOptedOut reports whether the request opted out of the semantic
// cache via the no-store headers (checked by the entry handler).
func semanticCacheOptedOut(ctx context.Context) bool {
    v, _ := ctx.Value(semanticNoStoreKey{}).(bool)
    return v
}

// semanticCachePrompt returns the #423 key for the semantic cache, or a zero
// key when the request must skip semantic caching: cache disabled, tool calls
// present (a stored tool_calls payload must not surface for a similar
// prompt), multimodal content (extractTextContent cannot see image blocks,
// and an answer to a different image must not be replayed), empty payload
// (system-only requests have nothing content-sensitive to key on), or a
// payload over semanticPromptCap.
func (s *Server) semanticCachePrompt(req *adapter.ChatRequest) semanticCacheKey {
    if s.semanticCache == nil || req == nil {
        return semanticCacheKey{}
    }
    if req.Tools != nil || chatRequestHasImage(req) {
        return semanticCacheKey{}
    }
    var systemText, payloadText string
    for _, msg := range req.Messages {
        str, ok := msg.Content.(string)
        if !ok || str == "" {
            continue
        }
        if msg.Role == "system" || msg.Role == "developer" {
            systemText += str + " "
        } else {
            payloadText += str + " "
        }
    }
    if payloadText == "" {
        return semanticCacheKey{}
    }
    if len(payloadText) > semanticPromptCap {
        slog.Debug("semantic cache skipped: payload over cap", "payload_len", len(payloadText), "cap", semanticPromptCap)
        return semanticCacheKey{}
    }
    sum := sha256.Sum256([]byte(systemText))
    return semanticCacheKey{Payload: payloadText, SystemHash: hex.EncodeToString(sum[:])}
}
