package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// BaichuanProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (Baichuan catalog) stays shim-specific.
type BaichuanProvider struct {
    baseOpenAICompatible
}

func NewBaichuanProvider(name string, backendCfg config.BackendConfig) *BaichuanProvider {
    slog.Info("building baichuan provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &BaichuanProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *BaichuanProvider) Name() string { return p.baseName() }

func (p *BaichuanProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "baichuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *BaichuanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "baichuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *BaichuanProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "baichuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *BaichuanProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "baichuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *BaichuanProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "baichuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *BaichuanProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "Baichuan4", Object: "model", OwnedBy: "baichuan"},
        {ID: "Baichuan3-Turbo", Object: "model", OwnedBy: "baichuan"},
        {ID: "Baichuan3-Turbo-128k", Object: "model", OwnedBy: "baichuan"},
    }, nil
}
