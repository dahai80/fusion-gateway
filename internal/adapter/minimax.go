package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// MinimaxProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (MiniMax catalog) stays shim-specific.
type MinimaxProvider struct {
    baseOpenAICompatible
}

func NewMinimaxProvider(name string, backendCfg config.BackendConfig) *MinimaxProvider {
    slog.Info("building minimax provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &MinimaxProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *MinimaxProvider) Name() string { return p.baseName() }

func (p *MinimaxProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "minimax", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MinimaxProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "minimax", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MinimaxProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "minimax", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MinimaxProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "minimax", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MinimaxProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "minimax", p.baseOpenAICompatible.setBearerAuth)
}

func (p *MinimaxProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "MiniMax-Text-01", Object: "model", OwnedBy: "minimax"},
        {ID: "abab6.5s-chat", Object: "model", OwnedBy: "minimax"},
        {ID: "abab6.5-chat", Object: "model", OwnedBy: "minimax"},
    }, nil
}
