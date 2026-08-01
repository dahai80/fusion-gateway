package adapter

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func makeChatHandler(t *testing.T, id string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        chatResp := ChatResponse{
            ID: id, Object: "chat.completion", Created: time.Now().Unix(), Model: "test",
            Choices: []ChatChoice{{Index: 0, Message: map[string]interface{}{"role": "assistant", "content": "hi"}, FinishReason: "stop"}},
        }
        body, _ := json.Marshal(chatResp)
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(body)
    }
}

func makeStreamHandler(t *testing.T, id string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        chunk := StreamChunk{ID: id, Object: "chat.completion.chunk", Model: "test"}
        b, _ := json.Marshal(chunk)
        w.Header().Set("Content-Type", "text/event-stream")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("data: "))
        _, _ = w.Write(b)
        _, _ = w.Write([]byte("\n\n"))
    }
}

func makeEmbeddingHandler(t *testing.T) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        embResp := EmbeddingResponse{Object: "list", Data: []EmbeddingData{{Object: "embedding", Embedding: []float64{0.1}, Index: 0}}, Model: "emb"}
        body, _ := json.Marshal(embResp)
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(body)
    }
}

func TestDashScopeProvider_Name(t *testing.T) {
    p := NewDashScopeProvider("dashscope", config.BackendConfig{Type: "dashscope", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", APIKey: "test-key"})
    if p.Name() != "dashscope" { t.Errorf("expected dashscope, got %s", p.Name()) }
}

func TestDashScopeProvider_ListModels(t *testing.T) {
    p := NewDashScopeProvider("dashscope", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 4 { t.Errorf("expected 4 models, got %d", len(models)) }
}

func TestDashScopeProvider_Rerank(t *testing.T) {
    p := NewDashScopeProvider("dashscope", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil { t.Error("expected error for unsupported rerank") }
}

func TestDashScopeProvider_HealthCheck(t *testing.T) {
    slog.Info("test DashScopeProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewDashScopeProvider("dashscope", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestDashScopeProvider_Chat(t *testing.T) {
    slog.Info("test DashScopeProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "ds-chat-1"))
    defer srv.Close()
    p := NewDashScopeProvider("dashscope", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "qwen-turbo", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "ds-chat-1" { t.Fatalf("expected ds-chat-1, got %s", resp.ID) }
}

func TestDashScopeProvider_StreamChat(t *testing.T) {
    slog.Info("test DashScopeProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "ds-chunk-1"))
    defer srv.Close()
    p := NewDashScopeProvider("dashscope", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "qwen-turbo", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestDashScopeProvider_Embedding(t *testing.T) {
    slog.Info("test DashScopeProvider_Embedding")
    srv := httptest.NewServer(makeEmbeddingHandler(t))
    defer srv.Close()
    p := NewDashScopeProvider("dashscope", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "emb", Input: []string{"a"}})
    if err != nil { t.Fatal(err) }
    if len(resp.Data) != 1 { t.Fatalf("expected 1, got %d", len(resp.Data)) }
}

func TestMoonshotProvider_Name(t *testing.T) {
    p := NewMoonshotProvider("moonshot", config.BackendConfig{Type: "moonshot", BaseURL: "https://api.moonshot.cn/v1", APIKey: "test-key"})
    if p.Name() != "moonshot" { t.Errorf("expected moonshot, got %s", p.Name()) }
}

func TestMoonshotProvider_ListModels(t *testing.T) {
    p := NewMoonshotProvider("moonshot", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 3 { t.Errorf("expected 3 models, got %d", len(models)) }
}

func TestMoonshotProvider_Embedding(t *testing.T) {
    p := NewMoonshotProvider("moonshot", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil { t.Error("expected error for unsupported embedding") }
}

func TestMoonshotProvider_HealthCheck(t *testing.T) {
    slog.Info("test MoonshotProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewMoonshotProvider("moonshot", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestMoonshotProvider_Chat(t *testing.T) {
    slog.Info("test MoonshotProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "ms-chat-1"))
    defer srv.Close()
    p := NewMoonshotProvider("moonshot", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "moonshot-v1", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "ms-chat-1" { t.Fatalf("expected ms-chat-1, got %s", resp.ID) }
}

func TestMoonshotProvider_StreamChat(t *testing.T) {
    slog.Info("test MoonshotProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "ms-chunk-1"))
    defer srv.Close()
    p := NewMoonshotProvider("moonshot", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "moonshot-v1", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestZhipuProvider_Name(t *testing.T) {
    p := NewZhipuProvider("zhipu", config.BackendConfig{Type: "zhipu", BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIKey: "test-key"})
    if p.Name() != "zhipu" { t.Errorf("expected zhipu, got %s", p.Name()) }
}

func TestZhipuProvider_ListModels(t *testing.T) {
    p := NewZhipuProvider("zhipu", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 4 { t.Errorf("expected 4 models, got %d", len(models)) }
}

func TestZhipuProvider_Rerank(t *testing.T) {
    p := NewZhipuProvider("zhipu", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil { t.Error("expected error for unsupported rerank") }
}

func TestZhipuProvider_HealthCheck(t *testing.T) {
    slog.Info("test ZhipuProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewZhipuProvider("zhipu", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestZhipuProvider_Chat(t *testing.T) {
    slog.Info("test ZhipuProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "zp-chat-1"))
    defer srv.Close()
    p := NewZhipuProvider("zhipu", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "glm-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "zp-chat-1" { t.Fatalf("expected zp-chat-1, got %s", resp.ID) }
}

func TestZhipuProvider_StreamChat(t *testing.T) {
    slog.Info("test ZhipuProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "zp-chunk-1"))
    defer srv.Close()
    p := NewZhipuProvider("zhipu", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "glm-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestZhipuProvider_Embedding(t *testing.T) {
    slog.Info("test ZhipuProvider_Embedding")
    srv := httptest.NewServer(makeEmbeddingHandler(t))
    defer srv.Close()
    p := NewZhipuProvider("zhipu", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "emb", Input: []string{"a"}})
    if err != nil { t.Fatal(err) }
    if len(resp.Data) != 1 { t.Fatalf("expected 1, got %d", len(resp.Data)) }
}

func TestMinimaxProvider_Name(t *testing.T) {
    p := NewMinimaxProvider("minimax", config.BackendConfig{Type: "minimax", BaseURL: "https://api.minimax.chat/v1", APIKey: "test-key"})
    if p.Name() != "minimax" { t.Errorf("expected minimax, got %s", p.Name()) }
}

func TestMinimaxProvider_ListModels(t *testing.T) {
    p := NewMinimaxProvider("minimax", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 3 { t.Errorf("expected 3 models, got %d", len(models)) }
}

func TestMinimaxProvider_Rerank(t *testing.T) {
    p := NewMinimaxProvider("minimax", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil { t.Error("expected error for unsupported rerank") }
}

func TestMinimaxProvider_HealthCheck(t *testing.T) {
    slog.Info("test MinimaxProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewMinimaxProvider("minimax", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestMinimaxProvider_Chat(t *testing.T) {
    slog.Info("test MinimaxProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "mm-chat-1"))
    defer srv.Close()
    p := NewMinimaxProvider("minimax", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "abab6", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "mm-chat-1" { t.Fatalf("expected mm-chat-1, got %s", resp.ID) }
}

func TestMinimaxProvider_StreamChat(t *testing.T) {
    slog.Info("test MinimaxProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "mm-chunk-1"))
    defer srv.Close()
    p := NewMinimaxProvider("minimax", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "abab6", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestBaichuanProvider_Name(t *testing.T) {
    p := NewBaichuanProvider("baichuan", config.BackendConfig{Type: "baichuan", BaseURL: "https://api.baichuan-ai.com/v1", APIKey: "test-key"})
    if p.Name() != "baichuan" { t.Errorf("expected baichuan, got %s", p.Name()) }
}

func TestBaichuanProvider_ListModels(t *testing.T) {
    p := NewBaichuanProvider("baichuan", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 3 { t.Errorf("expected 3 models, got %d", len(models)) }
}

func TestBaichuanProvider_Embedding(t *testing.T) {
    p := NewBaichuanProvider("baichuan", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil { t.Error("expected error for unsupported embedding") }
}

func TestBaichuanProvider_HealthCheck(t *testing.T) {
    slog.Info("test BaichuanProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewBaichuanProvider("baichuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestBaichuanProvider_Chat(t *testing.T) {
    slog.Info("test BaichuanProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "bc-chat-1"))
    defer srv.Close()
    p := NewBaichuanProvider("baichuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "baichuan2-turbo", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "bc-chat-1" { t.Fatalf("expected bc-chat-1, got %s", resp.ID) }
}

func TestBaichuanProvider_StreamChat(t *testing.T) {
    slog.Info("test BaichuanProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "bc-chunk-1"))
    defer srv.Close()
    p := NewBaichuanProvider("baichuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "baichuan2-turbo", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestHunyuanProvider_Name(t *testing.T) {
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{Type: "hunyuan", BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", APIKey: "test-key"})
    if p.Name() != "hunyuan" { t.Errorf("expected hunyuan, got %s", p.Name()) }
}

func TestHunyuanProvider_ListModels(t *testing.T) {
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 4 { t.Errorf("expected 4 models, got %d", len(models)) }
}

func TestHunyuanProvider_Rerank(t *testing.T) {
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil { t.Error("expected error for unsupported rerank") }
}

func TestHunyuanProvider_HealthCheck(t *testing.T) {
    slog.Info("test HunyuanProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestHunyuanProvider_Chat(t *testing.T) {
    slog.Info("test HunyuanProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "hy-chat-1"))
    defer srv.Close()
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "hunyuan-lite", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "hy-chat-1" { t.Fatalf("expected hy-chat-1, got %s", resp.ID) }
}

func TestHunyuanProvider_StreamChat(t *testing.T) {
    slog.Info("test HunyuanProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "hy-chunk-1"))
    defer srv.Close()
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "hunyuan-lite", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestHunyuanProvider_Embedding(t *testing.T) {
    slog.Info("test HunyuanProvider_Embedding")
    srv := httptest.NewServer(makeEmbeddingHandler(t))
    defer srv.Close()
    p := NewHunyuanProvider("hunyuan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "emb", Input: []string{"a"}})
    if err != nil { t.Fatal(err) }
    if len(resp.Data) != 1 { t.Fatalf("expected 1, got %d", len(resp.Data)) }
}

func TestStepFunProvider_Name(t *testing.T) {
    p := NewStepFunProvider("stepfun", config.BackendConfig{Type: "stepfun", BaseURL: "https://api.stepfun.com/v1", APIKey: "test-key"})
    if p.Name() != "stepfun" { t.Errorf("expected stepfun, got %s", p.Name()) }
}

func TestStepFunProvider_ListModels(t *testing.T) {
    p := NewStepFunProvider("stepfun", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 3 { t.Errorf("expected 3 models, got %d", len(models)) }
}

func TestStepFunProvider_Rerank(t *testing.T) {
    p := NewStepFunProvider("stepfun", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil { t.Error("expected error for unsupported rerank") }
}

func TestStepFunProvider_HealthCheck(t *testing.T) {
    slog.Info("test StepFunProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewStepFunProvider("stepfun", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestStepFunProvider_Chat(t *testing.T) {
    slog.Info("test StepFunProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "sf-chat-1"))
    defer srv.Close()
    p := NewStepFunProvider("stepfun", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "step-1-8k", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "sf-chat-1" { t.Fatalf("expected sf-chat-1, got %s", resp.ID) }
}

func TestStepFunProvider_StreamChat(t *testing.T) {
    slog.Info("test StepFunProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "sf-chunk-1"))
    defer srv.Close()
    p := NewStepFunProvider("stepfun", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "step-1-8k", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestYiProvider_Name(t *testing.T) {
    p := NewYiProvider("yi", config.BackendConfig{Type: "yi", BaseURL: "https://api.lingyiwanwu.com/v1", APIKey: "test-key"})
    if p.Name() != "yi" { t.Errorf("expected yi, got %s", p.Name()) }
}

func TestYiProvider_ListModels(t *testing.T) {
    p := NewYiProvider("yi", config.BackendConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil { t.Fatal(err) }
    if len(models) != 4 { t.Errorf("expected 4 models, got %d", len(models)) }
}

func TestYiProvider_Embedding(t *testing.T) {
    p := NewYiProvider("yi", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil { t.Error("expected error for unsupported embedding") }
}

func TestYiProvider_HealthCheck(t *testing.T) {
    slog.Info("test YiProvider_HealthCheck")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/models" { w.WriteHeader(http.StatusOK) }
    }))
    defer srv.Close()
    p := NewYiProvider("yi", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    if err := p.HealthCheck(context.Background()); err != nil { t.Fatal(err) }
}

func TestYiProvider_Chat(t *testing.T) {
    slog.Info("test YiProvider_Chat")
    srv := httptest.NewServer(makeChatHandler(t, "yi-chat-1"))
    defer srv.Close()
    p := NewYiProvider("yi", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "yi-large", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    if resp.ID != "yi-chat-1" { t.Fatalf("expected yi-chat-1, got %s", resp.ID) }
}

func TestYiProvider_StreamChat(t *testing.T) {
    slog.Info("test YiProvider_StreamChat")
    srv := httptest.NewServer(makeStreamHandler(t, "yi-chunk-1"))
    defer srv.Close()
    p := NewYiProvider("yi", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "yi-large", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil { t.Fatal(err) }
    var count int
    for range ch { count++ }
    if count != 1 { t.Fatalf("expected 1 chunk, got %d", count) }
}

func TestChineseProviders_StubRerank(t *testing.T) {
    slog.Info("test ChineseProviders_StubRerank")
    providers := []struct {
        name string
        p    Provider
    }{
        {"baichuan", NewBaichuanProvider("baichuan", config.BackendConfig{BaseURL: "http://localhost"})},
        {"deepseek", NewDeepSeekProvider("deepseek", config.BackendConfig{BaseURL: "http://localhost"})},
        {"moonshot", NewMoonshotProvider("moonshot", config.BackendConfig{BaseURL: "http://localhost"})},
        {"yi", NewYiProvider("yi", config.BackendConfig{BaseURL: "http://localhost"})},
    }
    for _, pp := range providers {
        _, err := pp.p.Rerank(context.Background(), &RerankRequest{Query: "test", Documents: []string{"a"}})
        if err == nil {
            t.Errorf("%s: expected error from stub Rerank", pp.name)
        }
    }
}

func TestChineseProviders_StubEmbedding(t *testing.T) {
    slog.Info("test ChineseProviders_StubEmbedding")
    providers := []struct {
        name string
        p    Provider
    }{
        {"minimax", NewMinimaxProvider("minimax", config.BackendConfig{BaseURL: "http://localhost"})},
        {"stepfun", NewStepFunProvider("stepfun", config.BackendConfig{BaseURL: "http://localhost"})},
    }
    for _, pp := range providers {
        _, err := pp.p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
        if err == nil {
            t.Errorf("%s: expected error from stub Embedding", pp.name)
        }
    }
}

func makeErrorHandler(statusCode int) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(statusCode)
        _, _ = w.Write([]byte(`{"error":"bad request"}`))
    }
}

func makeBadJSONHandler() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`not json`))
    }
}

func TestChineseProviders_ChatErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_ChatErrorPaths")
    constructors := []struct {
        name string
        fn   func(string, config.BackendConfig) Provider
    }{
        {"volcengine", func(n string, c config.BackendConfig) Provider { return NewVolcengineProvider(n, c) }},
        {"qianfan", func(n string, c config.BackendConfig) Provider { return NewQianfanProvider(n, c) }},
        {"deepseek", func(n string, c config.BackendConfig) Provider { return NewDeepSeekProvider(n, c) }},
        {"openrouter", func(n string, c config.BackendConfig) Provider { return NewOpenRouterProvider(n, c) }},
        {"dashscope", func(n string, c config.BackendConfig) Provider { return NewDashScopeProvider(n, c) }},
        {"moonshot", func(n string, c config.BackendConfig) Provider { return NewMoonshotProvider(n, c) }},
        {"zhipu", func(n string, c config.BackendConfig) Provider { return NewZhipuProvider(n, c) }},
        {"minimax", func(n string, c config.BackendConfig) Provider { return NewMinimaxProvider(n, c) }},
        {"baichuan", func(n string, c config.BackendConfig) Provider { return NewBaichuanProvider(n, c) }},
        {"hunyuan", func(n string, c config.BackendConfig) Provider { return NewHunyuanProvider(n, c) }},
        {"stepfun", func(n string, c config.BackendConfig) Provider { return NewStepFunProvider(n, c) }},
        {"yi", func(n string, c config.BackendConfig) Provider { return NewYiProvider(n, c) }},
    }
    for _, c := range constructors {
        t.Run(c.name+"_http_error", func(t *testing.T) {
            srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
            if err == nil {
                t.Error("expected error for HTTP 400")
            }
        })
        t.Run(c.name+"_bad_json", func(t *testing.T) {
            srv := httptest.NewServer(makeBadJSONHandler())
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
            if err == nil {
                t.Error("expected error for bad JSON")
            }
        })
    }
}

func TestChineseProviders_HealthCheckErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_HealthCheckErrorPaths")
    constructors := []struct {
        name string
        fn   func(string, config.BackendConfig) Provider
    }{
        {"volcengine", func(n string, c config.BackendConfig) Provider { return NewVolcengineProvider(n, c) }},
        {"deepseek", func(n string, c config.BackendConfig) Provider { return NewDeepSeekProvider(n, c) }},
        {"openrouter", func(n string, c config.BackendConfig) Provider { return NewOpenRouterProvider(n, c) }},
        {"dashscope", func(n string, c config.BackendConfig) Provider { return NewDashScopeProvider(n, c) }},
        {"moonshot", func(n string, c config.BackendConfig) Provider { return NewMoonshotProvider(n, c) }},
        {"zhipu", func(n string, c config.BackendConfig) Provider { return NewZhipuProvider(n, c) }},
        {"minimax", func(n string, c config.BackendConfig) Provider { return NewMinimaxProvider(n, c) }},
        {"baichuan", func(n string, c config.BackendConfig) Provider { return NewBaichuanProvider(n, c) }},
        {"hunyuan", func(n string, c config.BackendConfig) Provider { return NewHunyuanProvider(n, c) }},
        {"stepfun", func(n string, c config.BackendConfig) Provider { return NewStepFunProvider(n, c) }},
        {"yi", func(n string, c config.BackendConfig) Provider { return NewYiProvider(n, c) }},
    }
    for _, c := range constructors {
        t.Run(c.name+"_unhealthy", func(t *testing.T) {
            srv := httptest.NewServer(makeErrorHandler(http.StatusInternalServerError))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            err := p.HealthCheck(context.Background())
            if err == nil {
                t.Error("expected error for unhealthy")
            }
        })
    }
}

func TestChineseProviders_EmbeddingErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_EmbeddingErrorPaths")
    constructors := []struct {
        name string
        fn   func(string, config.BackendConfig) Provider
    }{
        {"volcengine", func(n string, c config.BackendConfig) Provider { return NewVolcengineProvider(n, c) }},
        {"qianfan", func(n string, c config.BackendConfig) Provider { return NewQianfanProvider(n, c) }},
        {"deepseek", func(n string, c config.BackendConfig) Provider { return NewDeepSeekProvider(n, c) }},
        {"openrouter", func(n string, c config.BackendConfig) Provider { return NewOpenRouterProvider(n, c) }},
        {"dashscope", func(n string, c config.BackendConfig) Provider { return NewDashScopeProvider(n, c) }},
        {"zhipu", func(n string, c config.BackendConfig) Provider { return NewZhipuProvider(n, c) }},
        {"hunyuan", func(n string, c config.BackendConfig) Provider { return NewHunyuanProvider(n, c) }},
    }
    for _, c := range constructors {
        t.Run(c.name+"_embedding_http_error", func(t *testing.T) {
            srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
            if err == nil {
                t.Error("expected error for HTTP 400")
            }
        })
    }
}

func TestChineseProviders_RerankErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_RerankErrorPaths")
    constructors := []struct {
        name string
        fn   func(string, config.BackendConfig) Provider
    }{
        {"volcengine", func(n string, c config.BackendConfig) Provider { return NewVolcengineProvider(n, c) }},
        {"qianfan", func(n string, c config.BackendConfig) Provider { return NewQianfanProvider(n, c) }},
        {"deepseek", func(n string, c config.BackendConfig) Provider { return NewDeepSeekProvider(n, c) }},
        {"openrouter", func(n string, c config.BackendConfig) Provider { return NewOpenRouterProvider(n, c) }},
        {"zhipu", func(n string, c config.BackendConfig) Provider { return NewZhipuProvider(n, c) }},
        {"hunyuan", func(n string, c config.BackendConfig) Provider { return NewHunyuanProvider(n, c) }},
    }
    for _, c := range constructors {
        t.Run(c.name+"_rerank_http_error", func(t *testing.T) {
            srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            _, err := p.Rerank(context.Background(), &RerankRequest{Query: "test", Documents: []string{"a"}})
            if err == nil {
                t.Error("expected error for HTTP 400")
            }
        })
    }
}
