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

func TestVolcengineProvider_HealthCheck(t *testing.T) {
    t.Run("healthy", func(t *testing.T) {
        slog.Info("test VolcengineProvider_HealthCheck healthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/v1/models" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
        if err := p.HealthCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("unhealthy", func(t *testing.T) {
        slog.Info("test VolcengineProvider_HealthCheck unhealthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for unhealthy")
        }
    })
}

func TestVolcengineProvider_Chat(t *testing.T) {
    slog.Info("test VolcengineProvider_Chat")
    chatResp := ChatResponse{
        ID:      "volc-chat-1",
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   "doubao",
        Choices: []ChatChoice{{
            Index:        0,
            Message:      map[string]interface{}{"role": "assistant", "content": "hi"},
            FinishReason: "stop",
        }},
    }
    body, _ := json.Marshal(chatResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "doubao", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "volc-chat-1" {
        t.Fatalf("expected volc-chat-1, got %s", resp.ID)
    }
}

func TestVolcengineProvider_Chat_Error(t *testing.T) {
    slog.Info("test VolcengineProvider_Chat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Fatal("expected error")
    }
}

func TestVolcengineProvider_StreamChat(t *testing.T) {
    slog.Info("test VolcengineProvider_StreamChat")
    chunk := StreamChunk{ID: "volc-chunk-1", Object: "chat.completion.chunk", Model: "doubao"}
    b, _ := json.Marshal(chunk)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(b)
            _, _ = w.Write([]byte("\n"))
        }
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "doubao", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count != 1 {
        t.Fatalf("expected 1 chunk, got %d", count)
    }
}

func TestVolcengineProvider_StreamChat_Error(t *testing.T) {
    slog.Info("test VolcengineProvider_StreamChat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Fatal("expected error")
    }
}

func TestVolcengineProvider_Embedding(t *testing.T) {
    slog.Info("test VolcengineProvider_Embedding")
    embResp := EmbeddingResponse{Object: "list", Data: []EmbeddingData{{Object: "embedding", Embedding: []float64{0.1}, Index: 0}}, Model: "emb"}
    body, _ := json.Marshal(embResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(body)
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "emb", Input: []string{"a"}})
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1 embedding, got %d", len(resp.Data))
    }
}

func TestVolcengineProvider_ListModels(t *testing.T) {
    slog.Info("test VolcengineProvider_ListModels")
    listResp := struct{ Data []ModelInfo `json:"data"` }{Data: []ModelInfo{{ID: "doubao", Object: "model", OwnedBy: "volcengine"}}}
    body, _ := json.Marshal(listResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/models" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volcengine", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 1 {
        t.Fatalf("expected 1 model, got %d", len(models))
    }
}

func TestVolcengineProvider_signRequest(t *testing.T) {
    slog.Info("test VolcengineProvider_signRequest")
    p := NewVolcengineProvider("volcengine", config.BackendConfig{APIKey: "test-key"})
    req, _ := http.NewRequest(http.MethodGet, "http://localhost/v1/models", nil)
    p.signRequest(req)
    if req.Header.Get("Authorization") != "Bearer test-key" {
        t.Error("expected Bearer auth header")
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

func TestQianfanProvider_HealthCheck(t *testing.T) {
    slog.Info("test QianfanProvider_HealthCheck")
    p := NewQianfanProvider("qianfan", config.BackendConfig{})
    if err := p.HealthCheck(context.Background()); err != nil {
        t.Fatalf("expected nil health check, got %v", err)
    }
}

func TestQianfanProvider_Chat(t *testing.T) {
    slog.Info("test QianfanProvider_Chat")
    chatResp := ChatResponse{
        ID:      "qianfan-chat-1",
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   "ernie",
        Choices: []ChatChoice{{
            Index:        0,
            Message:      map[string]interface{}{"role": "assistant", "content": "hi"},
            FinishReason: "stop",
        }},
    }
    body, _ := json.Marshal(chatResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "ernie", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "qianfan-chat-1" {
        t.Fatalf("expected qianfan-chat-1, got %s", resp.ID)
    }
}

func TestQianfanProvider_StreamChat(t *testing.T) {
    slog.Info("test QianfanProvider_StreamChat")
    chunk := StreamChunk{ID: "qianfan-chunk-1", Object: "chat.completion.chunk", Model: "ernie"}
    b, _ := json.Marshal(chunk)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(b)
        _, _ = w.Write([]byte("\n"))
    }))
    defer srv.Close()
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "ernie", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count != 1 {
        t.Fatalf("expected 1 chunk, got %d", count)
    }
}

func TestQianfanProvider_Embedding(t *testing.T) {
    slog.Info("test QianfanProvider_Embedding")
    embResp := EmbeddingResponse{Object: "list", Data: []EmbeddingData{{Object: "embedding", Embedding: []float64{0.1}, Index: 0}}, Model: "emb"}
    body, _ := json.Marshal(embResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(body)
    }))
    defer srv.Close()
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "emb", Input: []string{"a"}})
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1, got %d", len(resp.Data))
    }
}

func TestQianfanProvider_ListModels(t *testing.T) {
    slog.Info("test QianfanProvider_ListModels")
    listResp := struct{ Data []ModelInfo `json:"data"` }{Data: []ModelInfo{{ID: "ernie", Object: "model", OwnedBy: "baidu"}}}
    body, _ := json.Marshal(listResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/models" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 1 {
        t.Fatalf("expected 1 model, got %d", len(models))
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

func TestDeepSeekProvider_HealthCheck(t *testing.T) {
    t.Run("healthy", func(t *testing.T) {
        slog.Info("test DeepSeekProvider_HealthCheck healthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/v1/models" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewDeepSeekProvider("deepseek", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
        if err := p.HealthCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("unhealthy", func(t *testing.T) {
        slog.Info("test DeepSeekProvider_HealthCheck unhealthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewDeepSeekProvider("deepseek", config.BackendConfig{BaseURL: srv.URL})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error")
        }
    })
}

func TestDeepSeekProvider_Chat(t *testing.T) {
    slog.Info("test DeepSeekProvider_Chat")
    chatResp := ChatResponse{
        ID:      "ds-chat-1", Object: "chat.completion", Created: time.Now().Unix(), Model: "deepseek-chat",
        Choices: []ChatChoice{{Index: 0, Message: map[string]interface{}{"role": "assistant", "content": "hi"}, FinishReason: "stop"}},
    }
    body, _ := json.Marshal(chatResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewDeepSeekProvider("deepseek", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "deepseek-chat", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "ds-chat-1" {
        t.Fatalf("expected ds-chat-1, got %s", resp.ID)
    }
}

func TestDeepSeekProvider_StreamChat(t *testing.T) {
    slog.Info("test DeepSeekProvider_StreamChat")
    chunk := StreamChunk{ID: "ds-chunk-1", Object: "chat.completion.chunk", Model: "deepseek-chat"}
    b, _ := json.Marshal(chunk)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(b)
        _, _ = w.Write([]byte("\n"))
    }))
    defer srv.Close()
    p := NewDeepSeekProvider("deepseek", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "deepseek-chat", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count != 1 {
        t.Fatalf("expected 1 chunk, got %d", count)
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

func TestOpenRouterProvider_HealthCheck(t *testing.T) {
    t.Run("healthy", func(t *testing.T) {
        slog.Info("test OpenRouterProvider_HealthCheck healthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/v1/models" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewOpenRouterProvider("openrouter", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
        if err := p.HealthCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("unhealthy", func(t *testing.T) {
        slog.Info("test OpenRouterProvider_HealthCheck unhealthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewOpenRouterProvider("openrouter", config.BackendConfig{BaseURL: srv.URL})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error")
        }
    })
}

func TestOpenRouterProvider_Chat(t *testing.T) {
    slog.Info("test OpenRouterProvider_Chat")
    chatResp := ChatResponse{
        ID: "or-chat-1", Object: "chat.completion", Created: time.Now().Unix(), Model: "gpt-4",
        Choices: []ChatChoice{{Index: 0, Message: map[string]interface{}{"role": "assistant", "content": "hi"}, FinishReason: "stop"}},
    }
    body, _ := json.Marshal(chatResp)
    var gotReferer, gotTitle bool
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("HTTP-Referer") != "" {
            gotReferer = true
        }
        if r.Header.Get("X-Title") != "" {
            gotTitle = true
        }
        if r.URL.Path == "/v1/chat/completions" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewOpenRouterProvider("openrouter", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "or-chat-1" {
        t.Fatalf("expected or-chat-1, got %s", resp.ID)
    }
    if !gotReferer {
        t.Error("expected HTTP-Referer header")
    }
    if !gotTitle {
        t.Error("expected X-Title header")
    }
}

func TestOpenRouterProvider_StreamChat(t *testing.T) {
    slog.Info("test OpenRouterProvider_StreamChat")
    chunk := StreamChunk{ID: "or-chunk-1", Object: "chat.completion.chunk", Model: "gpt-4"}
    b, _ := json.Marshal(chunk)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(b)
        _, _ = w.Write([]byte("\n"))
    }))
    defer srv.Close()
    p := NewOpenRouterProvider("openrouter", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count != 1 {
        t.Fatalf("expected 1 chunk, got %d", count)
    }
}

func TestOpenRouterProvider_Embedding(t *testing.T) {
    slog.Info("test OpenRouterProvider_Embedding")
    embResp := EmbeddingResponse{Object: "list", Data: []EmbeddingData{{Object: "embedding", Embedding: []float64{0.1}, Index: 0}}, Model: "emb"}
    body, _ := json.Marshal(embResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(body)
    }))
    defer srv.Close()
    p := NewOpenRouterProvider("openrouter", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "emb", Input: []string{"a"}})
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1, got %d", len(resp.Data))
    }
}

func TestOpenRouterProvider_ListModels(t *testing.T) {
    slog.Info("test OpenRouterProvider_ListModels")
    listResp := struct{ Data []ModelInfo `json:"data"` }{Data: []ModelInfo{{ID: "gpt-4", Object: "model", OwnedBy: "openai"}}}
    body, _ := json.Marshal(listResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/models" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()
    p := NewOpenRouterProvider("openrouter", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 1 {
        t.Fatalf("expected 1, got %d", len(models))
    }
}
