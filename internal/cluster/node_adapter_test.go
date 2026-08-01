package cluster

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
)

func makeTestNode(id, addr string) *Node {
    return &Node{
        ID:       id,
        Address:  addr,
        GPU:      "M2",
        MemoryGB: 32,
        state:    NodeStateHealthy,
    }
}

func makeTestProvider(node *Node) *ClusterNodeProvider {
    return NewClusterNodeProvider(node, defaultRoutingCfg(), "test-api-key")
}

func TestNewClusterNodeProvider(t *testing.T) {
    node := makeTestNode("n1", "http://localhost:9001")
    p := NewClusterNodeProvider(node, defaultRoutingCfg(), "my-token")
    if p == nil {
        t.Fatal("provider should not be nil")
    }
    if p.apiKey != "my-token" {
        t.Errorf("expected apiKey my-token, got %s", p.apiKey)
    }
    if p.routeHeader != "X-Fusion-Route" {
        t.Errorf("expected route header X-Fusion-Route, got %s", p.routeHeader)
    }
    if p.routeHeaderValue != "gateway-decision" {
        t.Errorf("expected route header value gateway-decision, got %s", p.routeHeaderValue)
    }
}

func TestClusterNodeProvider_Name(t *testing.T) {
    node := makeTestNode("n1", "http://localhost:9001")
    p := makeTestProvider(node)
    if p.Name() != "cluster-n1" {
        t.Errorf("expected cluster-n1, got %s", p.Name())
    }
}

func TestClusterNodeProvider_NodeID(t *testing.T) {
    node := makeTestNode("n1", "http://localhost:9001")
    p := makeTestProvider(node)
    if p.NodeID() != "n1" {
        t.Errorf("expected n1, got %s", p.NodeID())
    }
}

func TestClusterNodeProvider_HealthCheck_OK(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/health" {
            t.Errorf("expected /health, got %s", r.URL.Path)
        }
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    if err := p.HealthCheck(context.Background()); err != nil {
        t.Fatalf("expected no error, got %v", err)
    }
}

func TestClusterNodeProvider_HealthCheck_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusServiceUnavailable)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    if err := p.HealthCheck(context.Background()); err == nil {
        t.Fatal("expected error on non-200 health check")
    }
}

func TestClusterNodeProvider_HealthCheck_ConnectionFailed(t *testing.T) {
    node := makeTestNode("n1", "http://127.0.0.1:1")
    p := makeTestProvider(node)
    p.httpClient.Timeout = 1 * time.Second

    if err := p.HealthCheck(context.Background()); err == nil {
        t.Fatal("expected error on connection failed")
    }
}

func TestClusterNodeProvider_HealthCheck_BadURL(t *testing.T) {
    node := &Node{ID: "n1", Address: "://bad-url"}
    p := makeTestProvider(node)

    if err := p.HealthCheck(context.Background()); err == nil {
        t.Fatal("expected error on bad URL")
    }
}

func TestClusterNodeProvider_Chat_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/chat/completions" {
            t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
        }
        if r.Method != http.MethodPost {
            t.Errorf("expected POST, got %s", r.Method)
        }
        if r.Header.Get("Authorization") != "Bearer test-api-key" {
            t.Errorf("missing auth header")
        }
        if r.Header.Get("X-Fusion-Route") != "gateway-decision" {
            t.Errorf("missing route header")
        }

        var req adapter.ChatRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            t.Errorf("decode request: %v", err)
        }

        resp := adapter.ChatResponse{
            ID:      "chat-123",
            Object:  "chat.completion",
            Created: time.Now().Unix(),
            Model:   req.Model,
            Choices: []adapter.ChatChoice{
                {Index: 0, Message: map[string]string{"role": "assistant", "content": "hello"}, FinishReason: "stop"},
            },
            Usage: adapter.UsageResponse{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{
        Model:    "test-model",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}},
    }
    resp, err := p.Chat(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if resp.ID != "chat-123" {
        t.Errorf("expected chat-123, got %s", resp.ID)
    }
    if resp.Usage.TotalTokens != 15 {
        t.Errorf("expected 15 tokens, got %d", resp.Usage.TotalTokens)
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after request, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_Chat_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("internal error"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.Chat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on non-200 chat")
    }
    if !strings.Contains(err.Error(), "status 500") {
        t.Errorf("error should mention status 500: %v", err)
    }
}

func TestClusterNodeProvider_Chat_ConnectionFailed(t *testing.T) {
    node := makeTestNode("n1", "http://127.0.0.1:1")
    p := makeTestProvider(node)
    p.httpClient.Timeout = 1 * time.Second

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.Chat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on connection failed")
    }
}

func TestClusterNodeProvider_Chat_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.Chat(context.Background(), req)
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestClusterNodeProvider_Chat_BadURL(t *testing.T) {
    node := &Node{ID: "n1", Address: "://bad"}
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.Chat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on bad URL")
    }
}

func TestClusterNodeProvider_Chat_InFlightTracking(t *testing.T) {
    var inFlightDuring int64
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Not easily observable from here; use a slow response to test
        time.Sleep(50 * time.Millisecond)
        resp := adapter.ChatResponse{ID: "chat-1", Object: "chat.completion", Model: "test"}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}

    doneCh := make(chan struct{})
    go func() {
        _, _ = p.Chat(context.Background(), req)
        close(doneCh)
    }()
    time.Sleep(20 * time.Millisecond)
    inFlightDuring = node.InFlight()

    <-doneCh

    if inFlightDuring != 1 {
        t.Errorf("expected 1 in-flight during request, got %d", inFlightDuring)
    }
    if node.InFlight() != 0 {
        t.Errorf("expected 0 in-flight after request, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_StreamChat_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/chat/completions" {
            t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer test-api-key" {
            t.Errorf("missing auth header")
        }

        flusher, ok := w.(http.Flusher)
        if !ok {
            t.Fatal("response writer does not support flushing")
        }

        chunks := []adapter.StreamChunk{
            {ID: "chat-1", Object: "chat.completion.chunk", Model: "test", Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"role": "assistant", "content": "hel"}}}},
            {ID: "chat-1", Object: "chat.completion.chunk", Model: "test", Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "lo"}}}},
        }
        for _, chunk := range chunks {
            _ = json.NewEncoder(w).Encode(chunk)
            flusher.Flush()
        }
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    ch, err := p.StreamChat(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    var chunks []adapter.StreamChunk
    for chunk := range ch {
        chunks = append(chunks, chunk)
    }

    if len(chunks) != 2 {
        t.Fatalf("expected 2 chunks, got %d", len(chunks))
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after stream, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_StreamChat_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = w.Write([]byte("bad gateway"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.StreamChat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on non-200 stream chat")
    }
    if !strings.Contains(err.Error(), "status 502") {
        t.Errorf("error should mention status 502: %v", err)
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after failed stream, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_StreamChat_ConnectionFailed(t *testing.T) {
    node := makeTestNode("n1", "http://127.0.0.1:1")
    p := makeTestProvider(node)
    p.httpClient.Timeout = 1 * time.Second

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.StreamChat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on connection failed")
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after failed stream, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_StreamChat_BadURL(t *testing.T) {
    node := &Node{ID: "n1", Address: "://bad"}
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.StreamChat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on bad URL")
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after failed stream, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_Embedding_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/embeddings" {
            t.Errorf("expected /v1/embeddings, got %s", r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer test-api-key" {
            t.Errorf("missing auth header")
        }
        if r.Header.Get("X-Fusion-Route") != "gateway-decision" {
            t.Errorf("missing route header")
        }

        resp := adapter.EmbeddingResponse{
            Object: "list",
            Model:  "text-embedding",
            Data: []adapter.EmbeddingData{
                {Object: "embedding", Embedding: []float64{0.1, 0.2, 0.3}, Index: 0},
                {Object: "embedding", Embedding: []float64{0.4, 0.5, 0.6}, Index: 1},
            },
            Usage: adapter.UsageResponse{PromptTokens: 20, TotalTokens: 20},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.EmbeddingRequest{
        Model: "text-embedding",
        Input: []string{"hello", "world"},
    }
    resp, err := p.Embedding(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Data) != 2 {
        t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
    }
    if resp.Data[0].Embedding[0] != 0.1 {
        t.Errorf("unexpected embedding value")
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after request, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_Embedding_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusServiceUnavailable)
        _, _ = w.Write([]byte("unavailable"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{"hello"}}
    _, err := p.Embedding(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on non-200 embedding")
    }
}

func TestClusterNodeProvider_Embedding_ConnectionFailed(t *testing.T) {
    node := makeTestNode("n1", "http://127.0.0.1:1")
    p := makeTestProvider(node)
    p.httpClient.Timeout = 1 * time.Second

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{"hello"}}
    _, err := p.Embedding(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on connection failed")
    }
}

func TestClusterNodeProvider_Embedding_BadURL(t *testing.T) {
    node := &Node{ID: "n1", Address: "://bad"}
    p := makeTestProvider(node)

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{"hello"}}
    _, err := p.Embedding(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on bad URL")
    }
}

func TestClusterNodeProvider_Embedding_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{"hello"}}
    _, err := p.Embedding(context.Background(), req)
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestClusterNodeProvider_Rerank_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/rerank" {
            t.Errorf("expected /v1/rerank, got %s", r.URL.Path)
        }
        if r.Method != http.MethodPost {
            t.Errorf("expected POST, got %s", r.Method)
        }
        if r.Header.Get("Authorization") != "Bearer test-api-key" {
            t.Errorf("missing auth header")
        }

        var req adapter.RerankRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            t.Errorf("decode request: %v", err)
        }

        docStr := "relevant doc"
        resp := adapter.RerankResponse{
            ID:    "rerank-1",
            Model: req.Model,
            Results: []adapter.RerankResult{
                {Index: 0, RelevanceScore: 0.95, Document: &docStr},
                {Index: 1, RelevanceScore: 0.3, Document: nil},
            },
            Usage: adapter.UsageResponse{PromptTokens: 5, TotalTokens: 5},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.RerankRequest{
        Model:     "rerank-model",
        Query:     "test query",
        Documents: []string{"relevant doc", "irrelevant doc"},
    }
    resp, err := p.Rerank(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Results) != 2 {
        t.Fatalf("expected 2 results, got %d", len(resp.Results))
    }
    if resp.Results[0].RelevanceScore != 0.95 {
        t.Errorf("expected 0.95, got %f", resp.Results[0].RelevanceScore)
    }
    if node.InFlight() != 0 {
        t.Errorf("in-flight should be 0 after request, got %d", node.InFlight())
    }
}

func TestClusterNodeProvider_Rerank_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = w.Write([]byte("bad gateway"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.RerankRequest{Model: "test", Query: "q", Documents: []string{"d1"}}
    _, err := p.Rerank(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on non-200 rerank")
    }
}

func TestClusterNodeProvider_Rerank_ConnectionFailed(t *testing.T) {
    node := makeTestNode("n1", "http://127.0.0.1:1")
    p := makeTestProvider(node)
    p.httpClient.Timeout = 1 * time.Second

    req := &adapter.RerankRequest{Model: "test", Query: "q", Documents: []string{"d1"}}
    _, err := p.Rerank(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on connection failed")
    }
}

func TestClusterNodeProvider_Rerank_BadURL(t *testing.T) {
    node := &Node{ID: "n1", Address: "://bad"}
    p := makeTestProvider(node)

    req := &adapter.RerankRequest{Model: "test", Query: "q", Documents: []string{"d1"}}
    _, err := p.Rerank(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on bad URL")
    }
}

func TestClusterNodeProvider_Rerank_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.RerankRequest{Model: "test", Query: "q", Documents: []string{"d1"}}
    _, err := p.Rerank(context.Background(), req)
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestClusterNodeProvider_ListModels_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/models" {
            t.Errorf("expected /v1/models, got %s", r.URL.Path)
        }
        if r.Method != http.MethodGet {
            t.Errorf("expected GET, got %s", r.Method)
        }
        if r.Header.Get("Authorization") != "Bearer test-api-key" {
            t.Errorf("missing auth header")
        }

        resp := struct {
            Data []adapter.ModelInfo `json:"data"`
        }{
            Data: []adapter.ModelInfo{
                {ID: "model-a", Object: "model", OwnedBy: "fusion"},
                {ID: "model-b", Object: "model", OwnedBy: "fusion"},
            },
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(models) != 2 {
        t.Fatalf("expected 2 models, got %d", len(models))
    }
    if models[0].ID != "model-a" {
        t.Errorf("expected model-a, got %s", models[0].ID)
    }
}

func TestClusterNodeProvider_ListModels_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusForbidden)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected error on non-200 list models")
    }
}

func TestClusterNodeProvider_ListModels_ConnectionFailed(t *testing.T) {
    node := makeTestNode("n1", "http://127.0.0.1:1")
    p := makeTestProvider(node)
    p.httpClient.Timeout = 1 * time.Second

    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected error on connection failed")
    }
}

func TestClusterNodeProvider_ListModels_BadURL(t *testing.T) {
    node := &Node{ID: "n1", Address: "://bad"}
    p := makeTestProvider(node)

    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected error on bad URL")
    }
}

func TestClusterNodeProvider_ListModels_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    _, err := p.ListModels(context.Background())
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestClusterNodeProvider_NoAPIKey(t *testing.T) {
    checkAuthHeader := func(t *testing.T, headerName, expectedValue string) {
        t.Helper()
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            got := r.Header.Get(headerName)
            if got != expectedValue {
                t.Errorf("expected %s=%q, got %q", headerName, expectedValue, got)
            }
            if r.Header.Get("Authorization") != "" {
                t.Errorf("Authorization header should not be set when no API key")
            }
            w.WriteHeader(http.StatusOK)
            resp := adapter.ChatResponse{ID: "1", Object: "chat.completion", Model: "test"}
            _ = json.NewEncoder(w).Encode(resp)
        }))
        defer srv.Close()

        node := makeTestNode("n1", srv.URL)
        p := NewClusterNodeProvider(node, defaultRoutingCfg(), "")

        req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
        _, err := p.Chat(context.Background(), req)
        if err != nil {
            t.Fatalf("unexpected error: %v", err)
        }
    }
    checkAuthHeader(t, "X-Fusion-Route", "gateway-decision")
}

func TestClusterNodeProvider_StreamChat_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        flusher, _ := w.(http.Flusher)
        _, _ = w.Write([]byte("not json\n"))
        flusher.Flush()
        _, _ = w.Write([]byte("also not json\n"))
        flusher.Flush()
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    ch, err := p.StreamChat(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    // Channel should close after decode error without blocking
    timeout := time.After(5 * time.Second)
    for {
        select {
        case _, ok := <-ch:
            if !ok {
                return
            }
        case <-timeout:
            t.Fatal("stream should have closed after decode error")
        }
    }
}

func TestClusterNodeProvider_HealthCheck_DrainsBody(t *testing.T) {
    bodyContent := "some health details"
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(bodyContent))
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    if err := p.HealthCheck(context.Background()); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}

func TestClusterNodeProvider_Chat_NoAPIKey(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no API key")
        }
        w.WriteHeader(http.StatusOK)
        resp := adapter.ChatResponse{ID: "1", Object: "chat.completion", Model: "test"}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := NewClusterNodeProvider(node, defaultRoutingCfg(), "")

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.Chat(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}

func TestClusterNodeProvider_StreamChat_NoAPIKey(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no API key")
        }
        w.WriteHeader(http.StatusOK)
        flusher, _ := w.(http.Flusher)
        chunk := adapter.StreamChunk{ID: "1", Object: "chat.completion.chunk", Model: "test"}
        _ = json.NewEncoder(w).Encode(chunk)
        flusher.Flush()
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := NewClusterNodeProvider(node, defaultRoutingCfg(), "")

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    ch, err := p.StreamChat(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    for range ch {
    }
}

func TestClusterNodeProvider_Embedding_NoAPIKey(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no API key")
        }
        w.WriteHeader(http.StatusOK)
        resp := adapter.EmbeddingResponse{Object: "list", Model: "test", Data: []adapter.EmbeddingData{{Object: "embedding", Index: 0}}}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := NewClusterNodeProvider(node, defaultRoutingCfg(), "")

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{"hello"}}
    _, err := p.Embedding(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}

func TestClusterNodeProvider_Rerank_NoAPIKey(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no API key")
        }
        w.WriteHeader(http.StatusOK)
        resp := adapter.RerankResponse{ID: "1", Model: "test"}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := NewClusterNodeProvider(node, defaultRoutingCfg(), "")

    req := &adapter.RerankRequest{Model: "test", Query: "q", Documents: []string{"d1"}}
    _, err := p.Rerank(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}

func TestClusterNodeProvider_ListModels_NoAPIKey(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no API key")
        }
        w.WriteHeader(http.StatusOK)
        resp := struct {
            Data []adapter.ModelInfo `json:"data"`
        }{Data: []adapter.ModelInfo{{ID: "m1", Object: "model"}}}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := NewClusterNodeProvider(node, defaultRoutingCfg(), "")

    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(models) != 1 {
        t.Fatalf("expected 1 model, got %d", len(models))
    }
}

func TestClusterNodeProvider_StreamChat_Canceled(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        flusher, _ := w.(http.Flusher)
        for i := 0; i < 10; i++ {
            chunk := adapter.StreamChunk{ID: "1", Object: "chat.completion.chunk", Model: "test"}
            _ = json.NewEncoder(w).Encode(chunk)
            flusher.Flush()
            time.Sleep(50 * time.Millisecond)
        }
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    ch, err := p.StreamChat(ctx, req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    // Read a couple chunks then cancel
    count := 0
    for range ch {
        count++
        if count >= 2 {
            cancel()
            break
        }
    }

    // Drain remaining if any
    timeout := time.After(3 * time.Second)
    for {
        select {
        case _, ok := <-ch:
            if !ok {
                return
            }
        case <-timeout:
            return
        }
    }
}

func TestClusterNodeProvider_Chat_ReadBodyOnNon200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = io.WriteString(w, "upstream error details here")
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.Chat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "upstream error details") {
        t.Errorf("error should contain body: %v", err)
    }
}

func TestClusterNodeProvider_Embedding_ReadBodyOnNon200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = io.WriteString(w, "embedding error details")
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{"hello"}}
    _, err := p.Embedding(context.Background(), req)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "embedding error details") {
        t.Errorf("error should contain body: %v", err)
    }
}

func TestClusterNodeProvider_Rerank_ReadBodyOnNon200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = io.WriteString(w, "rerank error details")
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.RerankRequest{Model: "test", Query: "q", Documents: []string{"d"}}
    _, err := p.Rerank(context.Background(), req)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "rerank error details") {
        t.Errorf("error should contain body: %v", err)
    }
}

func TestClusterNodeProvider_StreamChat_ReadBodyOnNon200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = io.WriteString(w, "stream error details")
    }))
    defer srv.Close()

    node := makeTestNode("n1", srv.URL)
    p := makeTestProvider(node)

    req := &adapter.ChatRequest{Model: "test", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    _, err := p.StreamChat(context.Background(), req)
    if err == nil {
        t.Fatal("expected error")
    }
    if !strings.Contains(err.Error(), "stream error details") {
        t.Errorf("error should contain body: %v", err)
    }
}
