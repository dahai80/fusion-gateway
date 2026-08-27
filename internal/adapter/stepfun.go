package adapter

import (
    "context"
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// StepFunProvider embeds baseOpenAICompatible (EI7). Plain Bearer-auth
// OpenAI-compatible endpoint — behavior inherited from base (RR8 ctx-watcher
// included). Only ListModels (Step catalog) stays shim-specific.
type StepFunProvider struct {
    baseOpenAICompatible
}

func NewStepFunProvider(name string, backendCfg config.BackendConfig) *StepFunProvider {
    slog.Info("building stepfun provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &StepFunProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *StepFunProvider) Name() string { return p.baseName() }

func (p *StepFunProvider) HealthCheck(ctx context.Context) error {
    return p.baseHealthCheck(ctx, "stepfun", p.baseOpenAICompatible.setBearerAuth)
}

func (p *StepFunProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "stepfun", p.baseOpenAICompatible.setBearerAuth)
}

func (p *StepFunProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "stepfun", p.baseOpenAICompatible.setBearerAuth)
}

func (p *StepFunProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "stepfun", p.baseOpenAICompatible.setBearerAuth)
}

func (p *StepFunProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "stepfun", p.baseOpenAICompatible.setBearerAuth)
}

func (p *StepFunProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "step-1-8k", Object: "model", OwnedBy: "stepfun"},
        {ID: "step-1-32k", Object: "model", OwnedBy: "stepfun"},
        {ID: "step-2-16k", Object: "model", OwnedBy: "stepfun"},
    }, nil
}
