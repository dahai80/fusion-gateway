package adapter

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestDashScopeProvider_Name(t *testing.T) {
    p := NewDashScopeProvider("dashscope", config.BackendConfig{
        Type:    "dashscope",
        BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "dashscope" {
        t.Errorf("expected dashscope, got %s", p.Name())
    }
}

func TestDashScopeProvider_ListModels(t *testing.T) {
    p := NewDashScopeProvider("dashscope", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 4 {
        t.Errorf("expected 4 models, got %d", len(models))
    }
}

func TestDashScopeProvider_Rerank(t *testing.T) {
    p := NewDashScopeProvider("dashscope", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestMoonshotProvider_Name(t *testing.T) {
    p := NewMoonshotProvider("moonshot", config.BackendConfig{
        Type:    "moonshot",
        BaseURL: "https://api.moonshot.cn/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "moonshot" {
        t.Errorf("expected moonshot, got %s", p.Name())
    }
}

func TestMoonshotProvider_ListModels(t *testing.T) {
    p := NewMoonshotProvider("moonshot", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 3 {
        t.Errorf("expected 3 models, got %d", len(models))
    }
}

func TestMoonshotProvider_Embedding(t *testing.T) {
    p := NewMoonshotProvider("moonshot", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil {
        t.Error("expected error for unsupported embedding")
    }
}

func TestZhipuProvider_Name(t *testing.T) {
    p := NewZhipuProvider("zhipu", config.BackendConfig{
        Type:    "zhipu",
        BaseURL: "https://open.bigmodel.cn/api/paas/v4",
        APIKey:  "test-key",
    })
    if p.Name() != "zhipu" {
        t.Errorf("expected zhipu, got %s", p.Name())
    }
}

func TestZhipuProvider_ListModels(t *testing.T) {
    p := NewZhipuProvider("zhipu", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 4 {
        t.Errorf("expected 4 models, got %d", len(models))
    }
}

func TestZhipuProvider_Rerank(t *testing.T) {
    p := NewZhipuProvider("zhipu", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestMinimaxProvider_Name(t *testing.T) {
    p := NewMinimaxProvider("minimax", config.BackendConfig{
        Type:    "minimax",
        BaseURL: "https://api.minimax.chat/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "minimax" {
        t.Errorf("expected minimax, got %s", p.Name())
    }
}

func TestMinimaxProvider_ListModels(t *testing.T) {
    p := NewMinimaxProvider("minimax", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 3 {
        t.Errorf("expected 3 models, got %d", len(models))
    }
}

func TestMinimaxProvider_Rerank(t *testing.T) {
    p := NewMinimaxProvider("minimax", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestBaichuanProvider_Name(t *testing.T) {
    p := NewBaichuanProvider("baichuan", config.BackendConfig{
        Type:    "baichuan",
        BaseURL: "https://api.baichuan-ai.com/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "baichuan" {
        t.Errorf("expected baichuan, got %s", p.Name())
    }
}

func TestBaichuanProvider_ListModels(t *testing.T) {
    p := NewBaichuanProvider("baichuan", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 3 {
        t.Errorf("expected 3 models, got %d", len(models))
    }
}

func TestBaichuanProvider_Embedding(t *testing.T) {
    p := NewBaichuanProvider("baichuan", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil {
        t.Error("expected error for unsupported embedding")
    }
}

func TestHunyuanProvider_Name(t *testing.T) {
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{
        Type:    "hunyuan",
        BaseURL: "https://api.hunyuan.cloud.tencent.com/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "hunyuan" {
        t.Errorf("expected hunyuan, got %s", p.Name())
    }
}

func TestHunyuanProvider_ListModels(t *testing.T) {
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 4 {
        t.Errorf("expected 4 models, got %d", len(models))
    }
}

func TestHunyuanProvider_Rerank(t *testing.T) {
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestStepFunProvider_Name(t *testing.T) {
    p := NewStepFunProvider("stepfun", config.BackendConfig{
        Type:    "stepfun",
        BaseURL: "https://api.stepfun.com/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "stepfun" {
        t.Errorf("expected stepfun, got %s", p.Name())
    }
}

func TestStepFunProvider_ListModels(t *testing.T) {
    p := NewStepFunProvider("stepfun", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 3 {
        t.Errorf("expected 3 models, got %d", len(models))
    }
}

func TestStepFunProvider_Rerank(t *testing.T) {
    p := NewStepFunProvider("stepfun", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestYiProvider_Name(t *testing.T) {
    p := NewYiProvider("yi", config.BackendConfig{
        Type:    "yi",
        BaseURL: "https://api.lingyiwanwu.com/v1",
        APIKey:  "test-key",
    })
    if p.Name() != "yi" {
        t.Errorf("expected yi, got %s", p.Name())
    }
}

func TestYiProvider_ListModels(t *testing.T) {
    p := NewYiProvider("yi", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 4 {
        t.Errorf("expected 4 models, got %d", len(models))
    }
}

func TestYiProvider_Embedding(t *testing.T) {
    p := NewYiProvider("yi", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil {
        t.Error("expected error for unsupported embedding")
    }
}
