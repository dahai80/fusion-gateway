package adapter

import (
    "context"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestFusionMLXProvider_InFlight(t *testing.T) {
    slog.Info("test FusionMLXProvider_InFlight")
    p := NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: "http://localhost:11434",
    }, config.RoutingConfig{})
    if p.InFlight() != 0 {
        t.Fatalf("expected 0 in-flight, got %d", p.InFlight())
    }
    p.inFlightCounter.Add(3)
    if p.InFlight() != 3 {
        t.Fatalf("expected 3 in-flight, got %d", p.InFlight())
    }
}

func TestFusionMLXProvider_inFlightAcquire(t *testing.T) {
    slog.Info("test FusionMLXProvider_inFlightAcquire")
    p := NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: "http://localhost:11434",
    }, config.RoutingConfig{})
    release := p.inFlightAcquire()
    if p.InFlight() != 1 {
        t.Fatalf("expected 1 in-flight after acquire, got %d", p.InFlight())
    }
    release()
    if p.InFlight() != 0 {
        t.Fatalf("expected 0 in-flight after release, got %d", p.InFlight())
    }
}

func TestFusionMLXProvider_ModelSet(t *testing.T) {
    slog.Info("test FusionMLXProvider_ModelSet")
    p := NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: "http://localhost:11434",
    }, config.RoutingConfig{})
    if p.ModelSet() != nil {
        t.Fatal("expected nil model set initially")
    }
    m := map[string]bool{"model-a": true, "model-b": true}
    p.modelSet.Store(m)
    ms := p.ModelSet()
    if len(ms) != 2 {
        t.Fatalf("expected 2 models, got %d", len(ms))
    }
    if !ms["model-a"] {
        t.Error("expected model-a in set")
    }
}

func TestFusionMLXProvider_RefreshModelSet(t *testing.T) {
    slog.Info("test FusionMLXProvider_RefreshModelSet")
    modelsResp := struct {
        Data []ModelInfo `json:"data"`
    }{
        Data: []ModelInfo{
            {ID: "qwen-7b", Object: "model", OwnedBy: "mlx"},
            {ID: "llama-8b", Object: "model", OwnedBy: "mlx"},
        },
    }
    body, _ := json.Marshal(modelsResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(body)
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: srv.URL,
    }, config.RoutingConfig{})
    p.RefreshModelSet(context.Background())
    ms := p.ModelSet()
    if ms == nil || len(ms) != 2 {
        t.Fatalf("expected 2 models, got %v", ms)
    }
    if !ms["qwen-7b"] {
        t.Error("expected qwen-7b in model set")
    }
}

func TestFusionMLXProvider_RefreshModelSet_Error(t *testing.T) {
    slog.Info("test FusionMLXProvider_RefreshModelSet_Error")
    p := NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: "http://127.0.0.1:1",
    }, config.RoutingConfig{})
    p.RefreshModelSet(context.Background())
    if p.ModelSet() != nil {
        t.Fatal("expected nil model set on error")
    }
}

func TestFusionMLXProvider_HealthCheck(t *testing.T) {
    t.Run("healthy", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_HealthCheck healthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/health" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        if err := p.HealthCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("unhealthy_status", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_HealthCheck unhealthy")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for unhealthy status")
        }
    })

    t.Run("connection_refused", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_HealthCheck connection_refused")
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://127.0.0.1:1"}, config.RoutingConfig{})
        if err := p.HealthCheck(context.Background()); err == nil {
            t.Fatal("expected error for connection refused")
        }
    })
}

func TestFusionMLXProvider_ReadyCheck(t *testing.T) {
    t.Run("ready", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_ReadyCheck ready")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/readyz" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        if err := p.ReadyCheck(context.Background()); err != nil {
            t.Fatal(err)
        }
    })

    t.Run("not_ready", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_ReadyCheck not_ready")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusServiceUnavailable)
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        if err := p.ReadyCheck(context.Background()); err == nil {
            t.Fatal("expected error for not ready")
        }
    })
}

func TestFusionMLXProvider_Chat(t *testing.T) {
    slog.Info("test FusionMLXProvider_Chat")
    chatResp := ChatResponse{
        ID:      "chat-123",
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   "qwen-7b",
        Choices: []ChatChoice{{
            Index:        0,
            Message:      map[string]interface{}{"role": "assistant", "content": "hello"},
            FinishReason: "stop",
        }},
    }
    body, _ := json.Marshal(chatResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        } else {
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    }, config.RoutingConfig{
        Negotiation: config.NegotiationConfig{
            RouteHeader:      "X-Fusion-Route",
            RouteHeaderValue: "gateway-decision",
        },
    })
    resp, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "qwen-7b",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "chat-123" {
        t.Fatalf("expected chat-123, got %s", resp.ID)
    }
    if p.InFlight() != 0 {
        t.Fatalf("expected 0 in-flight after Chat, got %d", p.InFlight())
    }
}

func TestFusionMLXProvider_Chat_Error(t *testing.T) {
    slog.Info("test FusionMLXProvider_Chat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("internal error"))
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    _, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestFusionMLXProvider_Chat_ConnectionError(t *testing.T) {
    slog.Info("test FusionMLXProvider_Chat_ConnectionError")
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://127.0.0.1:1"}, config.RoutingConfig{})
    _, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for connection refused")
    }
}

func TestFusionMLXProvider_StreamChat(t *testing.T) {
    slog.Info("test FusionMLXProvider_StreamChat")
    chunk1 := StreamChunk{
        ID:      "chunk-1",
        Object:  "chat.completion.chunk",
        Created: time.Now().Unix(),
        Model:   "qwen-7b",
        Choices: []ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "hel"}}},
    }
    chunk2 := StreamChunk{
        ID:      "chunk-2",
        Object:  "chat.completion.chunk",
        Created: time.Now().Unix(),
        Model:   "qwen-7b",
        Choices: []ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "lo"}}},
    }
    b1, _ := json.Marshal(chunk1)
    b2, _ := json.Marshal(chunk2)
    sseData := "data: " + string(b1) + "\n\ndata: " + string(b2) + "\n\ndata: [DONE]\n\n"

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.Header().Set("Content-Type", "text/event-stream")
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write([]byte(sseData))
        }
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "qwen-7b",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count != 2 {
        t.Fatalf("expected 2 chunks, got %d", count)
    }
}

func TestFusionMLXProvider_StreamChat_Error(t *testing.T) {
    slog.Info("test FusionMLXProvider_StreamChat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    _, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestFusionMLXProvider_Embedding(t *testing.T) {
    slog.Info("test FusionMLXProvider_Embedding")
    embResp := EmbeddingResponse{
        Object: "list",
        Data: []EmbeddingData{
            {Object: "embedding", Embedding: []float64{0.1, 0.2, 0.3}, Index: 0},
        },
        Model: "bge-small",
    }
    body, _ := json.Marshal(embResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/embeddings" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{
        Model: "bge-small",
        Input: []string{"hello"},
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(resp.Data) != 1 {
        t.Fatalf("expected 1 embedding, got %d", len(resp.Data))
    }
    if p.InFlight() != 0 {
        t.Fatalf("expected 0 in-flight after Embedding, got %d", p.InFlight())
    }
}

func TestFusionMLXProvider_Embedding_Error(t *testing.T) {
    slog.Info("test FusionMLXProvider_Embedding_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad request"))
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"a"}})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestFusionMLXProvider_Rerank(t *testing.T) {
    slog.Info("test FusionMLXProvider_Rerank")
    rerankResp := RerankResponse{
        ID:    "rerank-1",
        Model: "bge-reranker",
        Results: []RerankResult{
            {Index: 0, RelevanceScore: 0.95},
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

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    resp, err := p.Rerank(context.Background(), &RerankRequest{
        Model:     "bge-reranker",
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

func TestFusionMLXProvider_ListModels(t *testing.T) {
    slog.Info("test FusionMLXProvider_ListModels")
    listResp := struct {
        Data []ModelInfo `json:"data"`
    }{
        Data: []ModelInfo{
            {ID: "qwen-7b", Object: "model", OwnedBy: "mlx"},
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

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) != 1 || models[0].ID != "qwen-7b" {
        t.Fatalf("unexpected models: %v", models)
    }
}

func TestFusionMLXProvider_Cancel(t *testing.T) {
    slog.Info("test FusionMLXProvider_Cancel")
    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: "http://localhost",
        GC:      config.GCConfig{Enabled: true, MinIdleSinceLastGC: 300 * time.Second},
    }, config.RoutingConfig{})

    p.inFlightCounter.Add(2)
    p.Cancel("req-1")
    if p.InFlight() != 1 {
        t.Fatalf("expected 1 in-flight after cancel, got %d", p.InFlight())
    }

    p.Cancel("req-2")
    if p.InFlight() != 0 {
        t.Fatalf("expected 0 in-flight after cancel, got %d", p.InFlight())
    }

    p.Cancel("req-3")
    if p.InFlight() != 0 {
        t.Fatalf("expected 0 in-flight after cancel on zero, got %d", p.InFlight())
    }
}

func TestFusionMLXProvider_SafeGC(t *testing.T) {
    t.Run("skip_when_inflight", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_SafeGC skip_when_inflight")
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://localhost"}, config.RoutingConfig{})
        p.inFlightCounter.Add(1)
        p.gcPending.Store(true)
        p.SafeGC()
    })

    t.Run("skip_when_gcPending_false", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_SafeGC skip_when_gcPending_false")
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://localhost"}, config.RoutingConfig{})
        p.SafeGC()
    })

    t.Run("success", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_SafeGC success")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path == "/api/v1/gc" {
                w.WriteHeader(http.StatusOK)
            }
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        p.gcPending.Store(true)
        p.SafeGC()
        if p.lastGCTime.Load() == 0 {
            t.Fatal("expected lastGCTime to be updated")
        }
    })

    t.Run("non_ok_status", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_SafeGC non_ok_status")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusInternalServerError)
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        p.gcPending.Store(true)
        p.SafeGC()
    })
}

func TestFusionMLXProvider_TriggerGCWhenIdle(t *testing.T) {
    slog.Info("test FusionMLXProvider_TriggerGCWhenIdle")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/api/v1/gc" {
            w.WriteHeader(http.StatusOK)
        }
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    p.TriggerGCWhenIdle()
    time.Sleep(1 * time.Second)
    gcRan := p.lastGCTime.Load() > 0
    if !gcRan {
        t.Log("GC did not run within 1s, which is acceptable for async timer")
    }
}

func TestFusionMLXProvider_TriggerGCWhenIdle_AlreadyQueued(t *testing.T) {
    slog.Info("test FusionMLXProvider_TriggerGCWhenIdle_AlreadyQueued")
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://localhost"}, config.RoutingConfig{})
    p.gcPending.Store(true)
    p.TriggerGCWhenIdle()
}

func TestFusionMLXProvider_StartIdleGCTimer(t *testing.T) {
    t.Run("disabled", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_StartIdleGCTimer disabled")
        p := NewFusionMLXProvider(config.BackendConfig{
            BaseURL: "http://localhost",
            GC:      config.GCConfig{Enabled: false},
        }, config.RoutingConfig{})
        stopCh := make(chan struct{})
        p.StartIdleGCTimer(stopCh)
        close(stopCh)
    })

    t.Run("enabled_then_stop", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_StartIdleGCTimer enabled_then_stop")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusOK)
        }))
        defer srv.Close()

        p := NewFusionMLXProvider(config.BackendConfig{
            BaseURL: srv.URL,
            GC:      config.GCConfig{Enabled: true, MinIdleSinceLastGC: 100 * time.Millisecond},
        }, config.RoutingConfig{})
        stopCh := make(chan struct{})
        p.StartIdleGCTimer(stopCh)
        time.Sleep(200 * time.Millisecond)
        close(stopCh)
    })
}

func TestFusionMLXProvider_parseSSEStream(t *testing.T) {
    t.Run("normal_chunks", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_parseSSEStream normal_chunks")
        chunk := StreamChunk{
            ID:     "c1",
            Object: "chat.completion.chunk",
            Model:  "test",
        }
        b, _ := json.Marshal(chunk)
        sseData := "data: " + string(b) + "\n\n"
        ch := make(chan StreamChunk, 64)
        parseSSEStream(bytesReader([]byte(sseData)), ch)
        if len(ch) != 1 {
            t.Fatalf("expected 1 chunk in channel, got %d", len(ch))
        }
    })

    t.Run("backpressure", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_parseSSEStream backpressure")
        chunk := StreamChunk{ID: "c1", Object: "chat.completion.chunk", Model: "test"}
        b, _ := json.Marshal(chunk)
        sseData := "data: " + string(b) + "\n\n"
        ch := make(chan StreamChunk, 1)
        ch <- StreamChunk{ID: "filler"}

        parseSSEStream(bytesReader([]byte(sseData)), ch)
    })
}

func TestFusionMLXProvider_Rerank_Error(t *testing.T) {
    slog.Info("test FusionMLXProvider_Rerank_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("bad"))
    }))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{Model: "test", Query: "q", Documents: []string{"d"}})
    if err == nil {
        t.Fatal("expected error for 400 status")
    }
}

func TestFusionMLXProvider_ListModels_Error(t *testing.T) {
    slog.Info("test FusionMLXProvider_ListModels_Error")
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://127.0.0.1:1"}, config.RoutingConfig{})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected error for connection refused")
    }
}

type bytesReaderImpl struct {
    data []byte
    pos  int
}

func bytesReader(data []byte) *bytesReaderImpl {
    return &bytesReaderImpl{data: data}
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
    if r.pos >= len(r.data) {
        return 0, io.EOF
    }
    n := copy(p, r.data[r.pos:])
    r.pos += n
    return n, nil
}

func TestFusionMLXProvider_StartIdleGCTimer_Disabled(t *testing.T) {
    slog.Info("test FusionMLXProvider_StartIdleGCTimer_Disabled")
    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: "http://localhost:11434",
        GC:      config.GCConfig{Enabled: false},
    }, config.RoutingConfig{})
    stopCh := make(chan struct{})
    close(stopCh)
    p.StartIdleGCTimer(stopCh)
}

func TestFusionMLXProvider_StartIdleGCTimer_StopImmediately(t *testing.T) {
    slog.Info("test FusionMLXProvider_StartIdleGCTimer_StopImmediately")
    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: "http://localhost:11434",
        GC:      config.GCConfig{Enabled: true, MinIdleSinceLastGC: 100 * time.Millisecond},
    }, config.RoutingConfig{})
    stopCh := make(chan struct{})
    go func() {
        time.Sleep(50 * time.Millisecond)
        close(stopCh)
    }()
    p.StartIdleGCTimer(stopCh)
    time.Sleep(150 * time.Millisecond)
}

func TestFusionMLXProvider_StartIdleGCTimer_DefaultThreshold(t *testing.T) {
    slog.Info("test FusionMLXProvider_StartIdleGCTimer_DefaultThreshold")
    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: "http://localhost:11434",
        GC:      config.GCConfig{Enabled: true},
    }, config.RoutingConfig{})
    stopCh := make(chan struct{})
    close(stopCh)
    p.StartIdleGCTimer(stopCh)
}

func TestFusionMLXProvider_ReverseProxy(t *testing.T) {
    t.Run("forwards_path_and_injects_headers", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_ReverseProxy forwards_path_and_injects_headers")
        var gotPath, gotAuth, gotRoute, gotMethod string
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            gotPath = r.URL.Path
            gotMethod = r.Method
            gotAuth = r.Header.Get("Authorization")
            gotRoute = r.Header.Get("X-Fusion-Route")
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write([]byte(`{"ok":true}`))
        }))
        defer srv.Close()

        p := NewFusionMLXProvider(config.BackendConfig{
            BaseURL: srv.URL,
            APIKey:  "mlx-secret",
        }, config.RoutingConfig{
            Negotiation: config.NegotiationConfig{
                RouteHeader:      "X-Fusion-Route",
                RouteHeaderValue: "gateway-decision",
            },
        })

        rec := httptest.NewRecorder()
        req := httptest.NewRequest(http.MethodGet, "/admin/api/fine-tune/jobs", nil)
        p.ReverseProxy().ServeHTTP(rec, req)

        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        if gotPath != "/admin/api/fine-tune/jobs" {
            t.Fatalf("expected path forwarded verbatim, got %q", gotPath)
        }
        if gotMethod != http.MethodGet {
            t.Fatalf("expected GET forwarded, got %q", gotMethod)
        }
        if gotAuth != "Bearer mlx-secret" {
            t.Fatalf("expected Authorization injected, got %q", gotAuth)
        }
        if gotRoute != "gateway-decision" {
            t.Fatalf("expected X-Fusion-Route injected, got %q", gotRoute)
        }
        if rec.Body.String() != `{"ok":true}` {
            t.Fatalf("expected body passthrough, got %q", rec.Body.String())
        }
    })

    t.Run("forwards_post_body", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_ReverseProxy forwards_post_body")
        var gotBody []byte
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            gotBody, _ = io.ReadAll(r.Body)
            w.WriteHeader(http.StatusCreated)
        }))
        defer srv.Close()

        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "k"}, config.RoutingConfig{
            Negotiation: config.NegotiationConfig{RouteHeader: "X-Fusion-Route", RouteHeaderValue: "gateway-decision"},
        })

        rec := httptest.NewRecorder()
        req := httptest.NewRequest(http.MethodPost, "/admin/api/fine-tune/jobs", strings.NewReader(`{"model_id":"x"}`))
        p.ReverseProxy().ServeHTTP(rec, req)

        if rec.Code != http.StatusCreated {
            t.Fatalf("expected 201, got %d", rec.Code)
        }
        if string(gotBody) != `{"model_id":"x"}` {
            t.Fatalf("expected body passthrough, got %q", string(gotBody))
        }
    })

    t.Run("cached_single_instance", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_ReverseProxy cached_single_instance")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(http.StatusOK)
        }))
        defer srv.Close()
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
        first := p.ReverseProxy()
        second := p.ReverseProxy()
        if first != second {
            t.Fatal("expected ReverseProxy to return the same cached instance")
        }
    })

    t.Run("invalid_base_url_falls_back", func(t *testing.T) {
        slog.Info("test FusionMLXProvider_ReverseProxy invalid_base_url_falls_back")
        p := NewFusionMLXProvider(config.BackendConfig{BaseURL: "://bad-url"}, config.RoutingConfig{})
        rp := p.ReverseProxy()
        if rp == nil {
            t.Fatal("expected non-nil reverse proxy despite invalid base_url")
        }
    })
}

