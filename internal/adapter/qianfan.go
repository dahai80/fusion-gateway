package adapter

import (
    "context"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// QianfanProvider embeds baseOpenAICompatible (EI7). OpenAI-compatible
// endpoint, but Qianfan's auth prefers an accessToken (Bearer) over the raw
// apiKey when set, so this shim overrides setAuth and threads it into every
// base method. Chat/StreamChat/Embedding inherited from base — RR8 ctx-watcher
// included (the prior standalone StreamChat lacked it). HealthCheck stays nil
// (Qianfan has no /v1/models health contract) and ListModels does a live fetch.
type QianfanProvider struct {
    baseOpenAICompatible
    accessToken string
}

func NewQianfanProvider(name string, backendCfg config.BackendConfig) *QianfanProvider {
    slog.Info("building qianfan provider on openai-compatible base", "name", name, "base_url", backendCfg.BaseURL)
    return &QianfanProvider{baseOpenAICompatible: newBaseOpenAICompatible(name, backendCfg)}
}

func (p *QianfanProvider) Name() string { return p.baseName() }

func (p *QianfanProvider) setAuth(req *http.Request) {
    if p.accessToken != "" {
        req.Header.Set("Authorization", "Bearer "+p.accessToken)
    } else {
        p.baseOpenAICompatible.setBearerAuth(req)
    }
}

func (p *QianfanProvider) HealthCheck(ctx context.Context) error { return nil }

func (p *QianfanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return p.baseChat(ctx, req, "qianfan", p.setAuth)
}

func (p *QianfanProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return p.baseStream(ctx, req, "qianfan", p.setAuth)
}

func (p *QianfanProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return p.baseEmbedding(ctx, req, "qianfan", p.setAuth)
}

func (p *QianfanProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return p.baseRerank(ctx, req, "qianfan", p.setAuth)
}

func (p *QianfanProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return p.baseListModels(ctx, "qianfan", p.setAuth)
}
