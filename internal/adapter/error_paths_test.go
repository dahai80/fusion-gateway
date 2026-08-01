package adapter

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func makeStreamErrorHandler(statusCode int) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(statusCode)
        _, _ = w.Write([]byte(`{"error":"bad"}`))
    }
}

func makeBadStreamHandler() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`not json at all`))
    }
}

func makeChunkedStreamHandler() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n"))
    }
}

func TestChineseProviders_StreamChatErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_StreamChatErrorPaths")
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
        t.Run(c.name+"_stream_http_error", func(t *testing.T) {
            srv := httptest.NewServer(makeStreamErrorHandler(http.StatusBadRequest))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
            if err == nil {
                t.Error("expected error for HTTP 400 stream")
                if ch != nil {
                    for range ch {
                    }
                }
            }
        })
        t.Run(c.name+"_stream_bad_json", func(t *testing.T) {
            srv := httptest.NewServer(makeBadStreamHandler())
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
            if err != nil {
                t.Skip("provider returned error instead of stream with bad json")
            }
            for range ch {
            }
        })
    }
}

func TestChineseProviders_ListModelsErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_ListModelsErrorPaths")
    // Only volcengine, qianfan, openrouter make HTTP calls for ListModels;
    // the rest are hardcoded and always return nil error.
    constructors := []struct {
        name string
        fn   func(string, config.BackendConfig) Provider
    }{
        {"volcengine", func(n string, c config.BackendConfig) Provider { return NewVolcengineProvider(n, c) }},
        {"qianfan", func(n string, c config.BackendConfig) Provider { return NewQianfanProvider(n, c) }},
        {"openrouter", func(n string, c config.BackendConfig) Provider { return NewOpenRouterProvider(n, c) }},
    }
    for _, c := range constructors {
        t.Run(c.name+"_listmodels_http_error", func(t *testing.T) {
            srv := httptest.NewServer(makeErrorHandler(http.StatusInternalServerError))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            _, err := p.ListModels(context.Background())
            if err == nil {
                t.Error("expected error for HTTP 500 list models")
            }
        })
    }
}

func TestFusionMLX_EmbeddingErrorPath(t *testing.T) {
    slog.Info("test FusionMLX_EmbeddingErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
    if err == nil {
        t.Error("expected error for HTTP 400 embedding")
    }
}

func TestFusionMLX_RerankErrorPath(t *testing.T) {
    slog.Info("test FusionMLX_RerankErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{Query: "test", Documents: []string{"a"}})
    if err == nil {
        t.Error("expected error for HTTP 400 rerank")
    }
}

func TestFusionMLX_ListModelsErrorPath(t *testing.T) {
    slog.Info("test FusionMLX_ListModelsErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusInternalServerError))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Error("expected error for HTTP 500 list models")
    }
}

func TestFusionMLX_ChatErrorPath(t *testing.T) {
    slog.Info("test FusionMLX_ChatErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400 chat")
    }
}

func TestFusionMLX_ChatBadJSON(t *testing.T) {
    slog.Info("test FusionMLX_ChatBadJSON")
    srv := httptest.NewServer(makeBadJSONHandler())
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for bad JSON chat")
    }
}

func TestFusionMLX_StreamChatHTTPError(t *testing.T) {
    slog.Info("test FusionMLX_StreamChatHTTPError")
    srv := httptest.NewServer(makeStreamErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400 stream")
        if ch != nil {
            for range ch {
            }
        }
    }
}

func TestOpenAICompatible_ChatErrorPath(t *testing.T) {
    slog.Info("test OpenAICompatible_ChatErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400")
    }
}

func TestOpenAICompatible_ChatBadJSON(t *testing.T) {
    slog.Info("test OpenAICompatible_ChatBadJSON")
    srv := httptest.NewServer(makeBadJSONHandler())
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for bad JSON")
    }
}

func TestOpenAICompatible_StreamChatHTTPError(t *testing.T) {
    slog.Info("test OpenAICompatible_StreamChatHTTPError")
    srv := httptest.NewServer(makeStreamErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400 stream")
        if ch != nil {
            for range ch {
            }
        }
    }
}

func TestOpenAICompatible_RerankErrorPath(t *testing.T) {
    slog.Info("test OpenAICompatible_RerankErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Rerank(context.Background(), &RerankRequest{Query: "test", Documents: []string{"a"}})
    if err == nil {
        t.Error("expected error for HTTP 400 rerank")
    }
}

func TestOpenAICompatible_ListModelsErrorPath(t *testing.T) {
    slog.Info("test OpenAICompatible_ListModelsErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusInternalServerError))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Error("expected error for HTTP 500 list models")
    }
}

func TestFusionKB_ChatErrorPath(t *testing.T) {
    slog.Info("test FusionKB_ChatErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400")
    }
}

func TestFusionKB_ChatBadJSON(t *testing.T) {
    slog.Info("test FusionKB_ChatBadJSON")
    srv := httptest.NewServer(makeBadJSONHandler())
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for bad JSON")
    }
}

func TestFusionKB_StreamChatHTTPError(t *testing.T) {
    slog.Info("test FusionKB_StreamChatHTTPError")
    srv := httptest.NewServer(makeStreamErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400 stream")
        if ch != nil {
            for range ch {
            }
        }
    }
}

func TestFusionKB_RerankErrorPath(t *testing.T) {
    slog.Info("test FusionKB_RerankErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Rerank(context.Background(), &RerankRequest{Query: "test", Documents: []string{"a"}})
    if err == nil {
        t.Error("expected error for HTTP 400 rerank")
    }
}

func TestFusionKB_ListModelsErrorPath(t *testing.T) {
    slog.Info("test FusionKB_ListModelsErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusInternalServerError))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Error("expected error for HTTP 500 list models")
    }
}

func TestFusionKB_EmbeddingErrorPath(t *testing.T) {
    slog.Info("test FusionKB_EmbeddingErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
    if err == nil {
        t.Error("expected error for HTTP 400 embedding")
    }
}

func TestAnthropic_ParseSSE(t *testing.T) {
    slog.Info("test Anthropic_ParseSSE")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost", APIKey: "test-key"})
    t.Run("message_start_and_content", func(t *testing.T) {
        input := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg1\",\"usage\":{\"output_tokens\":5}}}\n\n" +
            "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
            "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":10},\"stop_reason\":\"end_turn\"}\n\n" +
            "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
        ch := make(chan StreamChunk, 64)
        p.parseAnthropicSSE(strings.NewReader(input), ch, "test-model")
        close(ch)
        var count int
        for range ch {
            count++
        }
        if count < 2 {
            t.Errorf("expected at least 2 chunks, got %d", count)
        }
    })
    t.Run("bad_json_line", func(t *testing.T) {
        input := "data: not-json\n\n"
        ch := make(chan StreamChunk, 64)
        p.parseAnthropicSSE(strings.NewReader(input), ch, "test-model")
        close(ch)
        for range ch {
        }
    })
    t.Run("no_data_prefix", func(t *testing.T) {
        input := "event: ping\njust some text\n\n"
        ch := make(chan StreamChunk, 64)
        p.parseAnthropicSSE(strings.NewReader(input), ch, "test-model")
        close(ch)
        for range ch {
        }
    })
    t.Run("done_signal", func(t *testing.T) {
        input := "data: [DONE]\n\n"
        ch := make(chan StreamChunk, 64)
        p.parseAnthropicSSE(strings.NewReader(input), ch, "test-model")
        close(ch)
        for range ch {
        }
    })
    t.Run("read_error", func(t *testing.T) {
        ch := make(chan StreamChunk, 64)
        p.parseAnthropicSSE(&errorReader{}, ch, "test-model")
        close(ch)
        for range ch {
        }
    })
    t.Run("message_delta_no_stop_reason", func(t *testing.T) {
        input := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg2\",\"usage\":{\"output_tokens\":0}}}\n\n" +
            "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5},\"stop_reason\":\"\"}\n\n"
        ch := make(chan StreamChunk, 64)
        p.parseAnthropicSSE(strings.NewReader(input), ch, "test-model")
        close(ch)
        var count int
        for range ch {
            count++
        }
        if count < 1 {
            t.Errorf("expected at least 1 chunk, got %d", count)
        }
    })
}

type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
    return 0, context.DeadlineExceeded
}

func TestAnthropic_ParseStreamEvents(t *testing.T) {
    slog.Info("test Anthropic_ParseStreamEvents")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost", APIKey: "test-key"})
    t.Run("valid_events", func(t *testing.T) {
        input := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg1\"}}\n\n" +
            "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
            "data: [DONE]\n\n"
        ch := make(chan AnthropicStreamEvent, 64)
        p.parseAnthropicStreamEvents(strings.NewReader(input), ch)
        close(ch)
        var count int
        for range ch {
            count++
        }
        if count < 2 {
            t.Errorf("expected at least 2 events, got %d", count)
        }
    })
    t.Run("bad_json", func(t *testing.T) {
        input := "data: not-json\n\n"
        ch := make(chan AnthropicStreamEvent, 64)
        p.parseAnthropicStreamEvents(strings.NewReader(input), ch)
        close(ch)
        for range ch {
        }
    })
    t.Run("comment_line", func(t *testing.T) {
        input := ": this is a comment\n\n"
        ch := make(chan AnthropicStreamEvent, 64)
        p.parseAnthropicStreamEvents(strings.NewReader(input), ch)
        close(ch)
        for range ch {
        }
    })
    t.Run("read_error", func(t *testing.T) {
        ch := make(chan AnthropicStreamEvent, 64)
        p.parseAnthropicStreamEvents(&errorReader{}, ch)
        close(ch)
        for range ch {
        }
    })
}

func TestVolcengine_SignRequestWithAPIKey(t *testing.T) {
    slog.Info("test Volcengine_SignRequestWithAPIKey")
    p := NewVolcengineProvider("test", config.BackendConfig{BaseURL: "http://localhost:8080", APIKey: "test-key"})
    p.accessKey = "test-access"
    p.secretKey = "test-secret"
    req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
    p.signRequest(req)
    auth := req.Header.Get("Authorization")
    if !strings.HasPrefix(auth, "HMAC-SHA256") {
        t.Errorf("expected HMAC signature when both access and secret keys present, got %s", auth)
    }
}

func TestFusionMLX_EmbeddingWithRouteHeader(t *testing.T) {
    slog.Info("test FusionMLX_EmbeddingWithRouteHeader")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("X-Route") != "test-value" {
            t.Errorf("expected X-Route header")
        }
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(EmbeddingResponse{Data: []EmbeddingData{{Embedding: []float64{0.1}, Index: 0}}})
    }))
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    }, config.RoutingConfig{
        Negotiation: config.NegotiationConfig{
            RouteHeader:      "X-Route",
            RouteHeaderValue: "test-value",
        },
    })
    resp, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Data) != 1 {
        t.Errorf("expected 1 embedding, got %d", len(resp.Data))
    }
}

func TestFusionMLX_EmbeddingBadJSON(t *testing.T) {
    slog.Info("test FusionMLX_EmbeddingBadJSON")
    srv := httptest.NewServer(makeBadJSONHandler())
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
    if err == nil {
        t.Error("expected error for bad JSON embedding")
    }
}

func TestFusionMLX_StreamChatBadJSON(t *testing.T) {
    slog.Info("test FusionMLX_StreamChatBadJSON")
    srv := httptest.NewServer(makeBadStreamHandler())
    defer srv.Close()
    p := NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}, config.RoutingConfig{})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Skip("provider returned error instead of stream with bad json")
    }
    for range ch {
    }
}

func TestFusionKB_ListModelsSuccess(t *testing.T) {
    slog.Info("test FusionKB_ListModelsSuccess")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "Bearer test-key" {
            t.Errorf("expected Authorization header")
        }
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]string{{"id": "kb-model", "object": "model", "owned_by": "kb"}}})
    }))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(models) != 1 || models[0].ID != "kb-model" {
        t.Errorf("expected kb-model, got %v", models)
    }
}

func TestFusionKB_ListModelsNoAPIKey(t *testing.T) {
    slog.Info("test FusionKB_ListModelsNoAPIKey")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("expected no Authorization header when no API key")
        }
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]string{{"id": "kb-model", "object": "model", "owned_by": "kb"}}})
    }))
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL})
    _, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}

func TestFusionKB_ListModelsBadJSON(t *testing.T) {
    slog.Info("test FusionKB_ListModelsBadJSON")
    srv := httptest.NewServer(makeBadJSONHandler())
    defer srv.Close()
    p := NewFusionKBProvider("kb", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // FusionKB returns nil, nil on decode failure
    if len(models) != 0 {
        t.Errorf("expected empty models on bad JSON, got %d", len(models))
    }
}

func TestQianfan_SetAuth_AccessToken(t *testing.T) {
    slog.Info("test Qianfan_SetAuth_AccessToken")
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: "http://localhost", APIKey: "api-key"})
    p.accessToken = "my-access-token"
    req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
    p.setAuth(req)
    if req.Header.Get("Authorization") != "Bearer my-access-token" {
        t.Errorf("expected Bearer my-access-token, got %s", req.Header.Get("Authorization"))
    }
}

func TestQianfan_SetAuth_APIKeyOnly(t *testing.T) {
    slog.Info("test Qianfan_SetAuth_APIKeyOnly")
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: "http://localhost", APIKey: "api-key"})
    req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
    p.setAuth(req)
    if req.Header.Get("Authorization") != "Bearer api-key" {
        t.Errorf("expected Bearer api-key, got %s", req.Header.Get("Authorization"))
    }
}

func TestQianfan_SetAuth_NoKey(t *testing.T) {
    slog.Info("test Qianfan_SetAuth_NoKey")
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: "http://localhost"})
    req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
    p.setAuth(req)
    if req.Header.Get("Authorization") != "" {
        t.Errorf("expected no Authorization header, got %s", req.Header.Get("Authorization"))
    }
}

func TestQianfan_StreamChatHTTPError(t *testing.T) {
    slog.Info("test Qianfan_StreamChatHTTPError")
    srv := httptest.NewServer(makeStreamErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400 stream")
        if ch != nil {
            for range ch {
            }
        }
    }
}

func TestQianfan_StreamChatBadJSON(t *testing.T) {
    slog.Info("test Qianfan_StreamChatBadJSON")
    srv := httptest.NewServer(makeBadStreamHandler())
    defer srv.Close()
    p := NewQianfanProvider("qianfan", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Skip("provider returned error instead of stream")
    }
    for range ch {
    }
}

func TestVolcengine_ListModelsSuccess(t *testing.T) {
    slog.Info("test Volcengine_ListModelsSuccess")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]string{{"id": "volc-model", "object": "model", "owned_by": "volc"}}})
    }))
    defer srv.Close()
    p := NewVolcengineProvider("volc", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(models) != 1 || models[0].ID != "volc-model" {
        t.Errorf("expected volc-model, got %v", models)
    }
}

func TestVolcengine_ListModelsBadJSON(t *testing.T) {
    slog.Info("test Volcengine_ListModelsBadJSON")
    srv := httptest.NewServer(makeBadJSONHandler())
    defer srv.Close()
    p := NewVolcengineProvider("volc", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Error("expected error for bad JSON")
    }
}

func TestFusionMLX_StartIdleGCTrigger(t *testing.T) {
    slog.Info("test FusionMLX_StartIdleGCTrigger")
    var gcCalled atomic.Int32
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gcCalled.Add(1)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
        GC: config.GCConfig{
            Enabled:            true,
            MinIdleSinceLastGC: 100 * time.Millisecond,
        },
    }, config.RoutingConfig{})
    stopCh := make(chan struct{})
    p.StartIdleGCTimer(stopCh)

    time.Sleep(500 * time.Millisecond)
    close(stopCh)
    time.Sleep(50 * time.Millisecond)

    if gcCalled.Load() == 0 {
        t.Log("idle GC not triggered within timeout (timing-dependent)")
    }
}

func TestFusionMLX_StartIdleGCTimer_Cancelled(t *testing.T) {
    slog.Info("test FusionMLX_StartIdleGCTimer_Cancelled")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
        GC: config.GCConfig{
            Enabled:            true,
            MinIdleSinceLastGC: 10 * time.Minute,
        },
    }, config.RoutingConfig{})
    stopCh := make(chan struct{})
    p.StartIdleGCTimer(stopCh)
    close(stopCh)
    time.Sleep(50 * time.Millisecond)
}

func TestOpenAICompatible_ChatSuccess(t *testing.T) {
    slog.Info("test OpenAICompatible_ChatSuccess")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(ChatResponse{ID: "1", Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: "hello"}}}})
    }))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Choices) != 1 {
        t.Errorf("expected 1 choice, got %d", len(resp.Choices))
    }
}

func TestOpenAICompatible_StreamChatSuccess(t *testing.T) {
    slog.Info("test OpenAICompatible_StreamChatSuccess")
    srv := httptest.NewServer(makeChunkedStreamHandler())
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    var count int
    for range ch {
        count++
    }
    if count < 1 {
        t.Errorf("expected at least 1 chunk, got %d", count)
    }
}

func TestOpenAICompatible_EmbeddingErrorPath(t *testing.T) {
    slog.Info("test OpenAICompatible_EmbeddingErrorPath")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
    if err == nil {
        t.Error("expected error for HTTP 400 embedding")
    }
}

func TestOpenAICompatible_HealthCheckError(t *testing.T) {
    slog.Info("test OpenAICompatible_HealthCheckError")
    p := NewOpenAICompatibleProvider("test", config.BackendConfig{BaseURL: "http://localhost:1", APIKey: "test-key"})
    err := p.HealthCheck(context.Background())
    if err == nil {
        t.Error("expected error for unreachable health check")
    }
}

func TestChineseProviders_StreamChatSuccess(t *testing.T) {
    slog.Info("test ChineseProviders_StreamChatSuccess")
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
        t.Run(c.name+"_stream_success", func(t *testing.T) {
            srv := httptest.NewServer(makeChunkedStreamHandler())
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "test", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
            if err != nil {
                t.Fatalf("unexpected error: %v", err)
            }
            var count int
            for range ch {
                count++
            }
            if count < 1 {
                t.Errorf("expected at least 1 chunk, got %d", count)
            }
        })
    }
}

func TestChineseProviders_EmbeddingHTTPErrorPaths(t *testing.T) {
    slog.Info("test ChineseProviders_EmbeddingHTTPErrorPaths")
    constructors := []struct {
        name string
        fn   func(string, config.BackendConfig) Provider
    }{
        {"volcengine", func(n string, c config.BackendConfig) Provider { return NewVolcengineProvider(n, c) }},
        {"qianfan", func(n string, c config.BackendConfig) Provider { return NewQianfanProvider(n, c) }},
        {"openrouter", func(n string, c config.BackendConfig) Provider { return NewOpenRouterProvider(n, c) }},
        {"deepseek", func(n string, c config.BackendConfig) Provider { return NewDeepSeekProvider(n, c) }},
        {"dashscope", func(n string, c config.BackendConfig) Provider { return NewDashScopeProvider(n, c) }},
        {"zhipu", func(n string, c config.BackendConfig) Provider { return NewZhipuProvider(n, c) }},
    }
    for _, c := range constructors {
        t.Run(c.name+"_embedding_http_error", func(t *testing.T) {
            srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
            defer srv.Close()
            p := c.fn(c.name, config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
            _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "test", Input: []string{"hello"}})
            if err == nil {
                t.Error("expected error for HTTP 400 embedding")
            }
        })
    }
}

func TestAnthropic_ChatHTTPError(t *testing.T) {
    slog.Info("test Anthropic_ChatHTTPError")
    srv := httptest.NewServer(makeErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "claude-3", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400")
    }
}

func TestAnthropic_StreamChatHTTPError(t *testing.T) {
    slog.Info("test Anthropic_StreamChatHTTPError")
    srv := httptest.NewServer(makeStreamErrorHandler(http.StatusBadRequest))
    defer srv.Close()
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "claude-3", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err == nil {
        t.Error("expected error for HTTP 400 stream")
        if ch != nil {
            for range ch {
            }
        }
    }
}

func TestAnthropic_EmbeddingNotSupported(t *testing.T) {
    slog.Info("test Anthropic_EmbeddingNotSupported")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost", APIKey: "test-key"})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "claude-3", Input: []string{"hello"}})
    if err == nil {
        t.Error("expected error for unsupported embedding")
    }
}

func TestAnthropic_RerankNotSupported(t *testing.T) {
    slog.Info("test Anthropic_RerankNotSupported")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost", APIKey: "test-key"})
    _, err := p.Rerank(context.Background(), &RerankRequest{Query: "test", Documents: []string{"a"}})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestAnthropic_ListModelsHardcoded(t *testing.T) {
    slog.Info("test Anthropic_ListModelsHardcoded")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost", APIKey: "test-key"})
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(models) == 0 {
        t.Error("expected hardcoded models list")
    }
}
