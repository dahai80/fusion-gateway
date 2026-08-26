package adapter

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
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
    sseData := "data: " + string(b) + "\n\ndata: [DONE]\n\n"
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            w.Header().Set("Content-Type", "text/event-stream")
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write([]byte(sseData))
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
    sseData := "data: " + string(b) + "\n\n"
    ch := make(chan StreamChunk, 64)
    parseSSEStream(context.Background(), bytesReader([]byte(sseData)), ch)
    if len(ch) != 1 {
        t.Fatalf("expected 1 chunk in channel, got %d", len(ch))
    }
}

func TestParseSSEStream_Backpressure(t *testing.T) {
    slog.Info("test ParseSSEStream_Backpressure")
    chunk := StreamChunk{ID: "c1", Object: "chat.completion.chunk", Model: "test"}
    b, _ := json.Marshal(chunk)
    sseData := "data: " + string(b) + "\n\n"
    ch := make(chan StreamChunk, 1)
    ch <- StreamChunk{ID: "filler"}

    // F3 fix: with the ctx-bounded send, a full buffer blocks until the
    // consumer reads OR ctx is canceled. The old test called
    // parseSSEStream synchronously with a background ctx and relied on the
    // deleted Degraded fallback to return — that now hangs forever. Drive it
    // in a goroutine and cancel ctx to prove cancel unblocks the producer
    // without emitting a Degraded sentinel (cancel is a silent stop, not
    // false backpressure that would trigger a non-stream re-run).
    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        parseSSEStream(ctx, bytesReader([]byte(sseData)), ch)
        close(done)
    }()
    cancel()
    select {
    case <-done:
        // producer returned on cancel — correct
    case <-time.After(2 * time.Second):
        t.Fatal("parseSSEStream hung on full buffer; ctx cancel did not unblock it")
    }
    // no Degraded sentinel should have been emitted on cancel
    select {
    case extra := <-ch:
        if extra.Degraded {
            t.Fatal("cancel must not emit a Degraded chunk (false backpressure -> double GPU load)")
        }
    default:
        // only the filler is present; nothing added on cancel
    }
}

// TestRR8_CtxWatcherUnblocksStalledReader verifies the RR8 ctx-watcher pattern
// (the one bolted onto every stream pump) closes the body on ctx cancel,
// unblocking a stalled Read in a parser that only checks ctx on its send arm.
//
// To ISOLATE the watcher from Go's transport-level ctx propagation (which also
// closes http.Response bodies on request-ctx cancel and would mask the
// watcher), this drives parseSSEStream directly with a hanging io.Reader that
// ignores ctx - its Read blocks forever unless SOMETHING closes it. Only the
// watcher's resp.Body.Close() (simulated via the hangingReader Close) can
// unblock it. The transport is not in the loop, so this proves the watcher.
func TestRR8_CtxWatcherUnblocksStalledReader(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    ch := make(chan StreamChunk, 64)

    // hangingReader blocks on Read forever until Close is called. It does NOT
    // observe any context - mirroring a stalled upstream where the transport
    // does not propagate cancel (the gap the RR8 watcher fills).
    body := &hangingReader{unblock: make(chan struct{})}

    pumpDone := make(chan struct{})
    go func() {
        defer close(pumpDone)
        defer close(ch)
        // The exact watcher pattern from openai_compatible.go StreamChat:
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        go func() {
            select {
            case <-ctx.Done():
                slog.Debug("test watcher: ctx canceled, closing body")
                body.Close()
            case <-stopBodyWatch:
            }
        }()
        parseSSEStream(ctx, body, ch)
    }()

    // Pump is now blocked in body.Read (hangingReader ignores ctx). The parser
    // would stay hung here forever - ctx.Done() is only checked on the send
    // arm, which is never reached because Read never returns.
    time.Sleep(50 * time.Millisecond)

    cancel() // triggers the watcher -> body.Close -> Read returns error

    select {
    case <-pumpDone:
        // pump goroutine exited; channel closed via defer. Watcher worked.
    case <-time.After(3 * time.Second):
        t.Fatal("RR8 watcher did not unblock stalled body.Read within 3s - goroutine leaked")
    }
    if _, ok := <-ch; ok {
        t.Fatal("channel should be closed after pump exit")
    }
}

// hangingReader is an io.ReadCloser whose Read blocks until Close is called.
// It does NOT observe any context - mirroring a stalled upstream where the
// transport does not propagate cancel (the gap the RR8 watcher fills). Close
// unblocks an in-flight Read by closing unblock, causing Read to error.
type hangingReader struct {
    unblock chan struct{}
    closed  atomic.Bool
}

func (h *hangingReader) Read(p []byte) (int, error) {
    select {
    case <-h.unblock:
        return 0, fmt.Errorf("read after body closed")
    case <-time.After(30 * time.Second):
        return 0, fmt.Errorf("hangingReader timed out (watcher failed to close body)")
    }
}

func (h *hangingReader) Close() error {
    if h.closed.CompareAndSwap(false, true) {
        close(h.unblock)
    }
    return nil
}
