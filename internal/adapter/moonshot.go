package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// MoonshotProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — Chat/StreamChat/Embedding/Rerank/HealthCheck
// inherited from base (RR8 ctx-watcher included). Only ListModels (Kimi
// catalog) stays shim-specific.
type MoonshotProvider struct {
    baseOpenAICompatible
}

func NewMoonshotProvider(name string, backendCfg config.BackendConfig) *MoonshotProvider {
    slog.Info("building moonshot provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &MoonshotProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *MoonshotProvider) Name() string { return p.baseName() }

func (p *MoonshotProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "moonshot", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MoonshotProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "moonshot", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MoonshotProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "moonshot", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MoonshotProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "moonshot", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MoonshotProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "moonshot", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MoonshotProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "moonshot-v1-8k", Object: "model", OwnedBy: "moonshot"},
        {ID: "moonshot-v1-32k", Object: "model", OwnedBy: "moonshot"},
        {ID: "moonshot-v1-128k", Object: "model", OwnedBy: "moonshot"},
    }, nil
}
