package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// DeepSeekProvider embeds baseOpenAICompatible (EI7). DeepSeek is a plain
// Bearer-auth OpenAI-compatible endpoint, so Chat/StreamChat/Embedding/
// Rerank/HealthCheck all come from the base — including the RR8 ctx-watcher
// the prior standalone copy was missing (live connection/slot leak on stall).
// Only ListModels (DeepSeek's fixed model catalog) stays shim-specific.
type DeepSeekProvider struct {
    baseOpenAICompatible
}

func NewDeepSeekProvider(name string, backendCfg config.BackendConfig) *DeepSeekProvider {
    slog.Info("building deepseek provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &DeepSeekProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *DeepSeekProvider) Name() string { return p.baseName() }

func (p *DeepSeekProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "deepseek", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DeepSeekProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "deepseek", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DeepSeekProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "deepseek", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DeepSeekProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "deepseek", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DeepSeekProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "deepseek", p.baseOpenAICompatible.setBearerAuth)
}

func (p *DeepSeekProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "deepseek-chat", Object: "model", OwnedBy: "deepseek"},
        {ID: "deepseek-reasoner", Object: "model", OwnedBy: "deepseek"},
    }, nil
}
