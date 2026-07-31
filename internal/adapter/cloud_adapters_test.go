package adapter

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestVolcengineProvider_Name(t *testing.T) {
    p := NewVolcengineProvider("volcengine", config.BackendConfig{
        Type:    "volcengine",
        BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
        APIKey:  "test-key",
    })
    if p.Name() != "volcengine" {
        t.Errorf("expected volcengine, got %s", p.Name())
    }
}

func TestVolcengineProvider_Rerank(t *testing.T) {
    p := NewVolcengineProvider("volcengine", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestQianfanProvider_Name(t *testing.T) {
    p := NewQianfanProvider("qianfan", config.BackendConfig{
        Type:    "qianfan",
        BaseURL: "https://qianfan.baidubce.com/v2",
        APIKey:  "test-key",
    })
    if p.Name() != "qianfan" {
        t.Errorf("expected qianfan, got %s", p.Name())
    }
}

func TestQianfanProvider_Rerank(t *testing.T) {
    p := NewQianfanProvider("qianfan", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestDeepSeekProvider_Name(t *testing.T) {
    p := NewDeepSeekProvider("deepseek", config.BackendConfig{
        Type:    "deepseek",
        BaseURL: "https://api.deepseek.com",
        APIKey:  "test-key",
    })
    if p.Name() != "deepseek" {
        t.Errorf("expected deepseek, got %s", p.Name())
    }
}

func TestDeepSeekProvider_ListModels(t *testing.T) {
    p := NewDeepSeekProvider("deepseek", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 2 {
        t.Errorf("expected 2 models, got %d", len(models))
    }
}

func TestDeepSeekProvider_Embedding(t *testing.T) {
    p := NewDeepSeekProvider("deepseek", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil {
        t.Error("expected error for unsupported embedding")
    }
}

func TestOpenRouterProvider_Name(t *testing.T) {
    p := NewOpenRouterProvider("openrouter", config.BackendConfig{
        Type:    "openrouter",
        BaseURL: "https://openrouter.ai/api/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "openrouter" {
        t.Errorf("expected openrouter, got %s", p.Name())
    }
}

func TestOpenRouterProvider_Rerank(t *testing.T) {
    p := NewOpenRouterProvider("openrouter", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}
