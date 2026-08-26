package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// ZhipuProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (GLM catalog) stays shim-specific.
type ZhipuProvider struct {
    baseOpenAICompatible
}

func NewZhipuProvider(name string, backendCfg config.BackendConfig) *ZhipuProvider {
    slog.Info("building zhipu provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &ZhipuProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *ZhipuProvider) Name() string { return p.baseName() }

func (p *ZhipuProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "zhipu", p.baseOpenAICompatible.setBearerAuth)
}

func (p *ZhipuProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "zhipu", p.baseOpenAICompatible.setBearerAuth)
}

func (p *ZhipuProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "zhipu", p.baseOpenAICompatible.setBearerAuth)
}

func (p *ZhipuProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "zhipu", p.baseOpenAICompatible.setBearerAuth)
}

func (p *ZhipuProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "zhipu", p.baseOpenAICompatible.setBearerAuth)
}

func (p *ZhipuProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "glm-4", Object: "model", OwnedBy: "zhipu"},
        {ID: "glm-4-flash", Object: "model", OwnedBy: "zhipu"},
        {ID: "glm-4-plus", Object: "model", OwnedBy: "zhipu"},
        {ID: "glm-4-long", Object: "model", OwnedBy: "zhipu"},
    }, nil
}
