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

func TestOpenAICompatibleProvider_HealthCheck(t *testing.T) {
    t.Run("healthy", func(t *testing.T) {
        slog.Info("test OpenAICompatibleProvider_HealthCheck healthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/v1/models" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
        if err := p.HealthCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("unhealthy", func(t *testing.T) {
        slog.Info("test OpenAICompatibleProvider_HealthCheck unhealthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for unhealthy status")
        }
    })

    t.Run("connection_error", func(t *testing.T) {
        slog.Info("test OpenAICompatibleProvider_HealthCheck connection_error")
        p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: "http://127.0.0.1:1"})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for connection refused")
        }
    })
}

func TestOpenAICompatibleProvider_Chat(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Chat")
    chatResp := ChatResponse{
        ID:      "openai-chat-1",
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   "gpt-4",
        Choices: []ChatChoice{{
            Index:        0,
            Message:      map[string]interface{}{"role": "assistant", "content": "hi"},
            FinishReason: "stop",
        }},
    }
    body, _ := json.Marshal(chatResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "gpt-4",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "openai-chat-1" {
        t.Fatalf("expected openai-chat-1, got %s", resp.ID)
    }
}

func TestOpenAICompatibleProvider_Chat_Error(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Chat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestOpenAICompatibleProvider_StreamChat(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_StreamChat")
    chunk := StreamChunk{
        ID:     "openai-chunk-1",
        Object: "chat.completion.chunk",
        Model:  "gpt-4",
    }
    b, _ := json.Marshal(chunk)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(b)
            _, _ = w.Write([]byte("\n"))
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "gpt-4",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
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

func TestOpenAICompatibleProvider_StreamChat_Error(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_StreamChat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestOpenAICompatibleProvider_Embedding(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Embedding")
    embResp := EmbeddingResponse{
        Object: "list",
        Data: []EmbeddingData{
            {Object: "embedding", Embedding: []float64{0.1, 0.2}, Index: 0},
        },
        Model: "text-embedding-ada-002",
    }
    body, _ := json.Marshal(embResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/embeddings" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{
        Model: "text-embedding-ada-002",
        Input: []string{"hello"},
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1 embedding, got %d", len(resp.Data))
    }
}

func TestOpenAICompatibleProvider_Embedding_Error(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Embedding_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad"))
    }))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"a"}})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestOpenAICompatibleProvider_Rerank(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Rerank")
    rerankResp := RerankResponse{
        ID:    "rerank-1",
        Model: "reranker",
        Results: []RerankResult{
            {Index: 0, RelevanceScore: 0.9},
        },
    }
    body, _ := json.Marshal(rerankResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/rerank" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    resp, err := p.Rerank(context.Background(), &RerankRequest{
        Model:     "reranker",
        Query:     "test",
        Documents: []string{"doc1"},
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Results) != 1 {
        t.Fatalf("expected 1 result, got %d", len(resp.Results))
    }
}

func TestOpenAICompatibleProvider_Rerank_Error(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Rerank_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad"))
    }))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Rerank(context.Background(), &RerankRequest{Model: "test", Query: "q", Documents: []string{"d"}})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestOpenAICompatibleProvider_ListModels(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_ListModels")
    listResp := struct {
        Data []ModelInfo `json:"data"`
    }{
        Data: []ModelInfo{
            {ID: "gpt-4", Object: "model", OwnedBy: "openai"},
        },
    }
    body, _ := json.Marshal(listResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/models" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 1 || models[0].ID != "gpt-4" {
        t.Fatalf("unexpected models: %v", models)
    }
}

func TestOpenAICompatibleProvider_ListModels_Error(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_ListModels_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestOpenAICompatibleProvider_Images(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Images")
    imgResp := ImageResponse{
        Created: time.Now().Unix(),
        Data: []ImageData{
            {URL: "https://example.com/image.png", RevisedPrompt: "a cat"},
        },
    }
    body, _ := json.Marshal(imgResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/images/generations" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Images(context.Background(), &ImageRequest{
        Model:  "dall-e-3",
        Prompt: "a cat",
        N:      1,
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1 image, got %d", len(resp.Data))
    }
}

func TestOpenAICompatibleProvider_Images_Error(t *testing.T) {
    slog.Info("test OpenAICompatibleProvider_Images_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad"))
    }))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Images(context.Background(), &ImageRequest{Model: "test", Prompt: "test"})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestOpenAICompatibleProvider_Name(t *testing.T) {
    p := NewOpenAICompatibleProvider("openai", config.BackendConfig{})
    if p.Name() != "openai" {
        t.Errorf("expected openai, got %s", p.Name())
    }
}

func TestParseSSEStream(t *testing.T) {
    slog.Info("test ParseSSEStream")
    chunk := StreamChunk{
        ID:     "c1",
        Object: "chat.completion.chunk",
        Model:  "test",
    }
    b, _ := json.Marshal(chunk)
    ch := make(chan StreamChunk, 64)
    parseSSEStream(bytesReader(b), ch)
    if len(ch) != 1 {
        t.Fatalf("expected 1 chunk in channel, got %d", len(ch))
    }
}

func TestParseSSEStream_Backpressure(t *testing.T) {
    slog.Info("test ParseSSEStream_Backpressure")
    chunk := StreamChunk{ID: "c1", Object: "chat.completion.chunk", Model: "test"}
    b, _ := json.Marshal(chunk)
    ch := make(chan StreamChunk, 1)
    ch <- StreamChunk{ID: "filler"}
    parseSSEStream(bytesReader(b), ch)
}
