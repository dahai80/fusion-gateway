package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// HunyuanProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (Hunyuan catalog) stays shim-specific.
type HunyuanProvider struct {
    baseOpenAICompatible
}

func NewHunyuanProvider(name string, backendCfg config.BackendConfig) *HunyuanProvider {
    slog.Info("building hunyuan provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &HunyuanProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *HunyuanProvider) Name() string { return p.baseName() }

func (p *HunyuanProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "hunyuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *HunyuanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "hunyuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *HunyuanProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "hunyuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *HunyuanProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "hunyuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *HunyuanProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "hunyuan", p.baseOpenAICompatible.setBearerAuth)
}

func (p *HunyuanProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "hunyuan-lite", Object: "model", OwnedBy: "hunyuan"},
        {ID: "hunyuan-standard", Object: "model", OwnedBy: "hunyuan"},
        {ID: "hunyuan-pro", Object: "model", OwnedBy: "hunyuan"},
        {ID: "hunyuan-turbo", Object: "model", OwnedBy: "hunyuan"},
    }, nil
}
