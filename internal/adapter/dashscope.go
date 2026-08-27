package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// DashScopeProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (Qwen catalog) stays shim-specific.
type DashScopeProvider struct {
    baseOpenAICompatible
}

func NewDashScopeProvider(name string, backendCfg config.BackendConfig) *DashScopeProvider {
    slog.Info("building dashscope provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &DashScopeProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *DashScopeProvider) Name() string { return p.baseName() }

func (p *DashScopeProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "dashscope", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DashScopeProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "dashscope", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DashScopeProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "dashscope", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DashScopeProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "dashscope", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DashScopeProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "dashscope", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DashScopeProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "qwen-turbo", Object: "model", OwnedBy: "dashscope"},
        {ID: "qwen-plus", Object: "model", OwnedBy: "dashscope"},
        {ID: "qwen-max", Object: "model", OwnedBy: "dashscope"},
        {ID: "qwen-long", Object: "model", OwnedBy: "dashscope"},
    }, nil
}
