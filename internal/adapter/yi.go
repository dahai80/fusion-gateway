package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// YiProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (Yi catalog) stays shim-specific.
type YiProvider struct {
    baseOpenAICompatible
}

func NewYiProvider(name string, backendCfg config.BackendConfig) *YiProvider {
    slog.Info("building yi provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &YiProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *YiProvider) Name() string { return p.baseName() }

func (p *YiProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "yi", p.baseOpenAICompatible.setBearerAuth)
}

func (p *YiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "yi", p.baseOpenAICompatible.setBearerAuth)
}

func (p *YiProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "yi", p.baseOpenAICompatible.setBearerAuth)
}

func (p *YiProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "yi", p.baseOpenAICompatible.setBearerAuth)
}

func (p *YiProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "yi", p.baseOpenAICompatible.setBearerAuth)
}

func (p *YiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "yi-lightning", Object: "model", OwnedBy: "yi"},
        {ID: "yi-large", Object: "model", OwnedBy: "yi"},
        {ID: "yi-medium", Object: "model", OwnedBy: "yi"},
        {ID: "yi-spark", Object: "model", OwnedBy: "yi"},
    }, nil
}
