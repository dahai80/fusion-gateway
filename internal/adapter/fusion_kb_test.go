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

func TestFusionKBProvider_HealthCheck(t *testing.T) {
    t.Run("healthy", func(t *testing.T) {
        slog.Info("test FusionKBProvider_HealthCheck healthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/v1/models" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
        if err := p.HealthCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("unhealthy", func(t *testing.T) {
        slog.Info("test FusionKBProvider_HealthCheck unhealthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for unhealthy status")
        }
    })

    t.Run("connection_error", func(t *testing.T) {
        slog.Info("test FusionKBProvider_HealthCheck connection_error")
        p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: "http://127.0.0.1:1"})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for connection refused")
        }
    })
}

func TestFusionKBProvider_Chat(t *testing.T) {
    slog.Info("test FusionKBProvider_Chat")
    chatResp := ChatResponse{
        ID:      "kb-chat-1",
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   "kb-model",
        Choices: []ChatChoice{{
            Index:        0,
            Message:      map[string]interface{}{"role": "assistant", "content": "kb reply"},
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

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "kb-model",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "kb-chat-1" {
        t.Fatalf("expected kb-chat-1, got %s", resp.ID)
    }
}

func TestFusionKBProvider_Chat_Error(t *testing.T) {
    slog.Info("test FusionKBProvider_Chat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestFusionKBProvider_StreamChat(t *testing.T) {
    slog.Info("test FusionKBProvider_StreamChat")
    chunk := StreamChunk{
        ID:     "kb-chunk-1",
        Object: "chat.completion.chunk",
        Model:  "kb-model",
    }
    b, _ := json.Marshal(chunk)
    sseData := "data: " + string(b) + "\n\ndata: [DONE]\n\n"
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.Header().Set("Content-Type", "text/event-stream")
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write([]byte(sseData))
        }
    }))
    defer srv.Close()

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "kb-model",
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

func TestFusionKBProvider_StreamChat_Error(t *testing.T) {
    slog.Info("test FusionKBProvider_StreamChat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestFusionKBProvider_Embedding(t *testing.T) {
    slog.Info("test FusionKBProvider_Embedding")
    embResp := EmbeddingResponse{
        Object: "list",
        Data: []EmbeddingData{
            {Object: "embedding", Embedding: []float64{0.1, 0.2}, Index: 0},
        },
        Model: "kb-emb",
    }
    body, _ := json.Marshal(embResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/embeddings" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{
        Model: "kb-emb",
        Input: []string{"hello"},
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1 embedding, got %d", len(resp.Data))
    }
}

func TestFusionKBProvider_Embedding_Error(t *testing.T) {
    slog.Info("test FusionKBProvider_Embedding_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad"))
    }))
    defer srv.Close()
    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"a"}})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestFusionKBProvider_Rerank(t *testing.T) {
    slog.Info("test FusionKBProvider_Rerank")
    rerankResp := RerankResponse{
        ID:    "kb-rerank-1",
        Model: "kb-reranker",
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

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
    resp, err := p.Rerank(context.Background(), &RerankRequest{
        Model:     "kb-reranker",
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

func TestFusionKBProvider_Rerank_Error(t *testing.T) {
    slog.Info("test FusionKBProvider_Rerank_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad"))
    }))
    defer srv.Close()
    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.Rerank(context.Background(), &RerankRequest{Model: "test", Query: "q", Documents: []string{"d"}})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestFusionKBProvider_ListModels(t *testing.T) {
    slog.Info("test FusionKBProvider_ListModels")
    listResp := struct {
        Data []ModelInfo `json:"data"`
    }{
        Data: []ModelInfo{
            {ID: "kb-model-1", Object: "model", OwnedBy: "kb"},
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

    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 1 || models[0].ID != "kb-model-1" {
        t.Fatalf("unexpected models: %v", models)
    }
}

func TestFusionKBProvider_ListModels_Error(t *testing.T) {
    slog.Info("test FusionKBProvider_ListModels_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()
    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestFusionKBProvider_Name(t *testing.T) {
    p := NewFusionKBProvider("fusion-kb", config.BackendConfig{})
    if p.Name() != "fusion-kb" {
        t.Errorf("expected fusion-kb, got %s", p.Name())
    }
}
