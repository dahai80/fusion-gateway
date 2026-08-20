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

func TestAnthropicProvider_HealthCheck(t *testing.T) {
    slog.Info("test AnthropicProvider_HealthCheck")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        Type:    "anthropic",
        BaseURL: "https://api.anthropic.com",
        APIKey:  "test-key",
    })
    if err := p.HealthCheck(context.Background()); err != nil {
        t.Fatalf("expected nil health check, got %v", err)
    }
}

func TestAnthropicProvider_Chat(t *testing.T) {
    slog.Info("test AnthropicProvider_Chat")
    antResp := AnthropicResponse{
        ID:    "msg_test",
        Type:  "message",
        Role:  "assistant",
        Model: "claude-3-5-sonnet-20241022",
        Content: []AnthropicContentBlock{
            {Type: "text", Text: "Hello from Claude!"},
        },
        StopReason: "end_turn",
        Usage:      AnthropicUsage{InputTokens: 10, OutputTokens: 5},
    }
    body, _ := json.Marshal(antResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/messages" && r.Method == http.MethodPost {
            if r.Header.Get("x-api-key") != "test-key" {
                t.Error("expected x-api-key header")
            }
            if r.Header.Get("anthropic-version") == "" {
                t.Error("expected anthropic-version header")
            }
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        } else {
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    resp, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "claude-3-5-sonnet-20241022",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "msg_test" {
        t.Fatalf("expected msg_test, got %s", resp.ID)
    }
}

func TestAnthropicProvider_Chat_Error(t *testing.T) {
    slog.Info("test AnthropicProvider_Chat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("internal error"))
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    _, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestAnthropicProvider_Chat_ConnectionError(t *testing.T) {
    slog.Info("test AnthropicProvider_Chat_ConnectionError")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: "http://127.0.0.1:1",
        APIKey:  "test-key",
    })
    _, err := p.Chat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for connection refused")
    }
}

func writeAnthropicSSE(w http.ResponseWriter, events [][]byte) {
    flusher, canFlush := w.(http.Flusher)
    w.Header().Set("Content-Type", "text/event-stream")
    w.WriteHeader(http.StatusOK)
    for _, ev := range events {
        _, _ = w.Write([]byte("data: "))
        _, _ = w.Write(ev)
        _, _ = w.Write([]byte("\n"))
    }
    if canFlush {
        flusher.Flush()
    }
}

func TestAnthropicProvider_StreamChat(t *testing.T) {
    slog.Info("test AnthropicProvider_StreamChat")
    msgStartEvent := AnthropicStreamEvent{
        Type: "message_start",
        Message: &AnthropicResponse{
            ID:    "msg_1",
            Type:  "message",
            Role:  "assistant",
            Model: "claude-3-5-sonnet-20241022",
            Usage: AnthropicUsage{InputTokens: 10},
        },
    }
    deltaEvent := AnthropicStreamEvent{
        Type:  "content_block_delta",
        Index: 0,
        Delta: json.RawMessage(`{"type":"text_delta","text":"hello"}`),
    }
    msgDeltaEvent := AnthropicStreamEvent{
        Type:       "message_delta",
        StopReason: "end_turn",
        Usage:      &AnthropicUsage{OutputTokens: 5},
    }
    msgStopEvent := AnthropicStreamEvent{
        Type: "message_stop",
    }

    b1, _ := json.Marshal(msgStartEvent)
    b2, _ := json.Marshal(deltaEvent)
    b3, _ := json.Marshal(msgDeltaEvent)
    b4, _ := json.Marshal(msgStopEvent)

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/messages" {
            writeAnthropicSSE(w, [][]byte{b1, b2, b3, b4})
        }
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "claude-3-5-sonnet-20241022",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count < 2 {
        t.Fatalf("expected at least 2 chunks, got %d", count)
    }
}

func TestAnthropicProvider_StreamChat_Error(t *testing.T) {
    slog.Info("test AnthropicProvider_StreamChat_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    _, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "test",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestAnthropicProvider_Messages(t *testing.T) {
    slog.Info("test AnthropicProvider_Messages")
    antResp := AnthropicResponse{
        ID:    "msg_test2",
        Type:  "message",
        Role:  "assistant",
        Model: "claude-3-5-sonnet-20241022",
        Content: []AnthropicContentBlock{
            {Type: "text", Text: "Hi from Messages"},
        },
        StopReason: "end_turn",
        Usage:      AnthropicUsage{InputTokens: 5, OutputTokens: 3},
    }
    body, _ := json.Marshal(antResp)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/messages" {
            w.WriteHeader(http.StatusOK)
            _, _ = w.Write(body)
        }
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    resp, err := p.Messages(context.Background(), &AnthropicRequest{
        Model:     "claude-3-5-sonnet-20241022",
        MaxTokens: 1024,
        Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
    })
    if err != nil {
        t.Fatal(err)
    }
    if resp.ID != "msg_test2" {
        t.Fatalf("expected msg_test2, got %s", resp.ID)
    }
}

func TestAnthropicProvider_Messages_Error(t *testing.T) {
    slog.Info("test AnthropicProvider_Messages_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    _, err := p.Messages(context.Background(), &AnthropicRequest{
        Model:     "test",
        MaxTokens: 1024,
        Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestAnthropicProvider_StreamMessages(t *testing.T) {
    slog.Info("test AnthropicProvider_StreamMessages")
    msgStartEvent := AnthropicStreamEvent{
        Type: "message_start",
        Message: &AnthropicResponse{
            ID:    "msg_stream",
            Type:  "message",
            Role:  "assistant",
            Model: "claude-3-5-sonnet-20241022",
            Usage: AnthropicUsage{InputTokens: 10},
        },
    }
    deltaEvent := AnthropicStreamEvent{
        Type:  "content_block_delta",
        Index: 0,
        Delta: json.RawMessage(`{"type":"text_delta","text":"stream"}`),
    }
    msgStopEvent := AnthropicStreamEvent{
        Type: "message_stop",
    }

    b1, _ := json.Marshal(msgStartEvent)
    b2, _ := json.Marshal(deltaEvent)
    b3, _ := json.Marshal(msgStopEvent)

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/messages" {
            writeAnthropicSSE(w, [][]byte{b1, b2, b3})
        }
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    ch, err := p.StreamMessages(context.Background(), &AnthropicRequest{
        Model:     "claude-3-5-sonnet-20241022",
        MaxTokens: 1024,
        Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
    })
    if err != nil {
        t.Fatal(err)
    }
    var count int
    for range ch {
        count++
    }
    if count < 2 {
        t.Fatalf("expected at least 2 events, got %d", count)
    }
}

func TestAnthropicProvider_StreamMessages_Error(t *testing.T) {
    slog.Info("test AnthropicProvider_StreamMessages_Error")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    _, err := p.StreamMessages(context.Background(), &AnthropicRequest{
        Model:     "test",
        MaxTokens: 1024,
        Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
    })
    if err == nil {
        t.Fatal("expected error for 500 status")
    }
}

func TestAnthropicProvider_Name(t *testing.T) {
    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        Type:    "anthropic",
        BaseURL: "https://api.anthropic.com",
        APIKey:  "test-key",
    })
    if p.Name() != "anthropic" {
        t.Errorf("expected anthropic, got %s", p.Name())
    }
}

func TestAnthropicProvider_ListModels(t *testing.T) {
    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        Type:    "anthropic",
        BaseURL: "https://api.anthropic.com",
        APIKey:  "test-key",
    })
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if len(models) == 0 {
        t.Error("expected at least one model")
    }
    found := false
    for _, m := range models {
        if m.ID == "claude-sonnet-4-20250514" {
            found = true
        }
    }
    if !found {
        t.Error("expected claude-sonnet-4-20250514 in model list")
    }
}

func TestAnthropicProvider_Embedding(t *testing.T) {
    p := NewAnthropicProvider("anthropic", config.BackendConfig{})
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{})
    if err == nil {
        t.Error("expected error for unsupported embedding")
    }
}

func TestAnthropicProvider_Rerank(t *testing.T) {
    p := NewAnthropicProvider("anthropic", config.BackendConfig{})
    _, err := p.Rerank(context.Background(), &RerankRequest{})
    if err == nil {
        t.Error("expected error for unsupported rerank")
    }
}

func TestOpenAIToAnthropic(t *testing.T) {
    temp := 0.7
    maxTokens := 1024
    req := &ChatRequest{
        Model:       "claude-3-5-sonnet-20241022",
        Temperature: &temp,
        MaxTokens:   &maxTokens,
        Stream:      false,
        Messages: []ChatMessage{
            {Role: "system", Content: "You are helpful."},
            {Role: "user", Content: "Hello"},
        },
    }
    antReq := OpenAIToAnthropic(req)
    if antReq.Model != "claude-3-5-sonnet-20241022" {
        t.Errorf("expected claude-3-5-sonnet-20241022, got %s", antReq.Model)
    }
    if antReq.MaxTokens != 1024 {
        t.Errorf("expected 1024, got %d", antReq.MaxTokens)
    }
    if antReq.System != "You are helpful." {
        t.Errorf("expected system prompt, got %v", antReq.System)
    }
    if len(antReq.Messages) != 1 {
        t.Fatalf("expected 1 message (system extracted), got %d", len(antReq.Messages))
    }
    if antReq.Messages[0].Role != "user" {
        t.Errorf("expected user role, got %s", antReq.Messages[0].Role)
    }
}

func TestAnthropicToOpenAI(t *testing.T) {
    resp := &AnthropicResponse{
        ID:    "msg_test",
        Type:  "message",
        Role:  "assistant",
        Model: "claude-3-5-sonnet-20241022",
        Content: []AnthropicContentBlock{
            {Type: "text", Text: "Hello!"},
        },
        StopReason: "end_turn",
        Usage:      AnthropicUsage{InputTokens: 10, OutputTokens: 5},
    }
    chatResp := AnthropicToOpenAI(resp)
    if chatResp.ID != "msg_test" {
        t.Errorf("expected msg_test, got %s", chatResp.ID)
    }
    if chatResp.Object != "chat.completion" {
        t.Errorf("expected chat.completion, got %s", chatResp.Object)
    }
    if len(chatResp.Choices) != 1 {
        t.Fatalf("expected 1 choice, got %d", len(chatResp.Choices))
    }
    if chatResp.Choices[0].FinishReason != "stop" {
        t.Errorf("expected stop, got %s", chatResp.Choices[0].FinishReason)
    }
    if chatResp.Usage.PromptTokens != 10 {
        t.Errorf("expected 10, got %d", chatResp.Usage.PromptTokens)
    }
}

func TestAnthropicToOpenAIChatRequest(t *testing.T) {
    temp := 0.5
    antReq := &AnthropicRequest{
        Model:       "claude-3-5-sonnet-20241022",
        MaxTokens:   2048,
        Temperature: &temp,
        System:      "You are a coding assistant.",
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "Write Go code"}}},
        },
    }
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if chatReq.Model != "claude-3-5-sonnet-20241022" {
        t.Errorf("expected claude model, got %s", chatReq.Model)
    }
    if chatReq.MaxTokens == nil || *chatReq.MaxTokens != 2048 {
        t.Errorf("expected max_tokens 2048, got %v", chatReq.MaxTokens)
    }
    if len(chatReq.Messages) != 2 {
        t.Fatalf("expected 2 messages (system + user), got %d", len(chatReq.Messages))
    }
    if chatReq.Messages[0].Role != "system" {
        t.Errorf("expected system role, got %s", chatReq.Messages[0].Role)
    }
    if chatReq.Messages[1].Role != "user" {
        t.Errorf("expected user role, got %s", chatReq.Messages[1].Role)
    }
}

func TestAnthropicToOpenAI_ToolUse(t *testing.T) {
    resp := &AnthropicResponse{
        ID:    "msg_tool",
        Type:  "message",
        Role:  "assistant",
        Model: "claude-3-5-sonnet-20241022",
        Content: []AnthropicContentBlock{
            {Type: "tool_use", ID: "tool_1", Name: "get_weather", Input: []byte(`{"city":"SF"}`)},
        },
        StopReason: "tool_use",
        Usage:      AnthropicUsage{InputTokens: 20, OutputTokens: 10},
    }
    chatResp := AnthropicToOpenAI(resp)
    if chatResp.Choices[0].FinishReason != "tool_calls" {
        t.Errorf("expected tool_calls, got %s", chatResp.Choices[0].FinishReason)
    }
}

func TestAnthropicProvider_setHeaders(t *testing.T) {
    slog.Info("test AnthropicProvider_setHeaders")
    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: "http://localhost",
        APIKey:  "test-key",
    })
    req, _ := http.NewRequest(http.MethodPost, "http://localhost/v1/messages", nil)
    p.setHeaders(req)
    if req.Header.Get("Content-Type") != "application/json" {
        t.Error("expected application/json content type")
    }
    if req.Header.Get("x-api-key") != "test-key" {
        t.Error("expected x-api-key")
    }
    if req.Header.Get("anthropic-version") == "" {
        t.Error("expected anthropic-version header")
    }
}

func TestParseAnthropicSSE(t *testing.T) {
    slog.Info("test ParseAnthropicSSE")
    msgStart := AnthropicStreamEvent{
        Type: "message_start",
        Message: &AnthropicResponse{
            ID:    "msg_1",
            Type:  "message",
            Role:  "assistant",
            Model: "claude-3-5-sonnet-20241022",
            Usage: AnthropicUsage{InputTokens: 10, OutputTokens: 5},
        },
    }
    deltaEvent := AnthropicStreamEvent{
        Type:  "content_block_delta",
        Index: 0,
        Delta: json.RawMessage(`{"type":"text_delta","text":"hello"}`),
    }
    msgDelta := AnthropicStreamEvent{
        Type:       "message_delta",
        StopReason: "end_turn",
        Usage:      &AnthropicUsage{OutputTokens: 5},
    }
    msgStop := AnthropicStreamEvent{
        Type: "message_stop",
    }

    b1, _ := json.Marshal(msgStart)
    b2, _ := json.Marshal(deltaEvent)
    b3, _ := json.Marshal(msgDelta)
    b4, _ := json.Marshal(msgStop)

    var sbuf strings.Builder
    sbuf.WriteString("data: ")
    sbuf.Write(b1)
    sbuf.WriteString("\n")
    sbuf.WriteString("data: ")
    sbuf.Write(b2)
    sbuf.WriteString("\n")
    sbuf.WriteString("data: ")
    sbuf.Write(b3)
    sbuf.WriteString("\n")
    sbuf.WriteString("data: ")
    sbuf.Write(b4)
    sbuf.WriteString("\n")

    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost"})
    ch := make(chan StreamChunk, 64)
    p.parseAnthropicSSE(context.Background(), strings.NewReader(sbuf.String()), ch, "claude-3-5-sonnet-20241022")
    close(ch)

    var chunks []StreamChunk
    for c := range ch {
        chunks = append(chunks, c)
    }
    if len(chunks) < 2 {
        t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
    }
}

func TestParseAnthropicStreamEvents(t *testing.T) {
    slog.Info("test ParseAnthropicStreamEvents")
    event1 := AnthropicStreamEvent{
        Type: "message_start",
        Message: &AnthropicResponse{
            ID:    "msg_1",
            Type:  "message",
            Role:  "assistant",
            Model: "claude-3-5-sonnet-20241022",
        },
    }
    event2 := AnthropicStreamEvent{
        Type: "message_stop",
    }

    b1, _ := json.Marshal(event1)
    b2, _ := json.Marshal(event2)

    var sbuf strings.Builder
    sbuf.WriteString("data: ")
    sbuf.Write(b1)
    sbuf.WriteString("\n")
    sbuf.WriteString("data: ")
    sbuf.Write(b2)
    sbuf.WriteString("\n")

    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: "http://localhost"})
    ch := make(chan AnthropicStreamEvent, 64)
    p.parseAnthropicStreamEvents(context.Background(), strings.NewReader(sbuf.String()), ch)
    close(ch)

    var events []AnthropicStreamEvent
    for e := range ch {
        events = append(events, e)
    }
    if len(events) != 2 {
        t.Fatalf("expected 2 events, got %d", len(events))
    }
    if events[0].Type != "message_start" {
        t.Errorf("expected message_start, got %s", events[0].Type)
    }
    if events[1].Type != "message_stop" {
        t.Errorf("expected message_stop, got %s", events[1].Type)
    }
}

func TestAnthropicStreamEvent_MarshalIndexZeroNotOmitted(t *testing.T) {
    // Regression (issue #46): AnthropicStreamEvent.Index used json:"index,omitempty",
    // so the first content block (index 0) was marshaled WITHOUT an "index" field.
    // Clients (claude code SDK) then receive a content_block_delta with no index,
    // cannot match it to an open content block, and throw "Content block not found".
    // Block-scoped events must always emit "index", even when 0.
    blockCases := []struct {
        name string
        ev   AnthropicStreamEvent
    }{
        {"content_block_start index 0", AnthropicStreamEvent{
            Type:         "content_block_start",
            Index:        0,
            ContentBlock: &AnthropicContentBlock{Type: "thinking"},
        }},
        {"content_block_delta index 0", AnthropicStreamEvent{
            Type:  "content_block_delta",
            Index: 0,
            Delta: json.RawMessage(`{"type":"thinking_delta","thinking":"1"}`),
        }},
        {"content_block_stop index 0", AnthropicStreamEvent{
            Type:  "content_block_stop",
            Index: 0,
        }},
    }
    for _, c := range blockCases {
        b, err := json.Marshal(c.ev)
        if err != nil {
            t.Fatalf("%s: marshal error: %v", c.name, err)
        }
        s := string(b)
        if !strings.Contains(s, `"index":0`) {
            t.Fatalf("%s: expected \"index\":0 in JSON, got: %s", c.name, s)
        }
    }
    // Message-scoped events must NOT carry an index (Anthropic SSE spec).
    msgCases := []AnthropicStreamEvent{
        {Type: "message_start", Message: &AnthropicResponse{ID: "m1", Model: "glm5.2"}},
        {Type: "message_delta", StopReason: "end_turn", Usage: &AnthropicUsage{OutputTokens: 5}},
        {Type: "message_stop"},
    }
    for _, ev := range msgCases {
        b, err := json.Marshal(ev)
        if err != nil {
            t.Fatalf("marshal error: %v", err)
        }
        s := string(b)
        if strings.Contains(s, `"index"`) {
            t.Fatalf("message-scoped event %s must not carry index, got: %s", ev.Type, s)
        }
    }
}

func TestAnthropicProvider_StreamMessagesNotTruncatedByClientTimeout(t *testing.T) {
    // Regression: http.Client.Timeout caps the full request incl. body read,
    // truncating long reasoning streams. StreamMessages must use a client
    // with Timeout:0 so a stream whose body takes longer than the backend
    // timeout still completes. Upstream here emits headers fast, then waits
    // 600ms (> the 300ms backend timeout below) before the final event.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        w.WriteHeader(http.StatusOK)
        flusher, _ := w.(http.Flusher)
        _, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_slow\",\"model\":\"glm5.2\"}}\n\n"))
        if flusher != nil {
            flusher.Flush()
        }
        // Delay longer than the backend timeout to prove no Client.Timeout cut.
        time.Sleep(600 * time.Millisecond)
        _, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"stop_reason\":\"end_turn\"}\n\n"))
        _, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
        if flusher != nil {
            flusher.Flush()
        }
    }))
    defer srv.Close()

    p := NewAnthropicProvider("glm52", config.BackendConfig{
        Type:    "anthropic",
        BaseURL: srv.URL,
        APIKey:  "test-key",
        Timeout: 300 * time.Millisecond, // would truncate at 300ms if applied to stream
    })

    ch, err := p.StreamMessages(context.Background(), &AnthropicRequest{
        Model:     "glm5.2",
        MaxTokens: 64,
        Messages:  []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
    })
    if err != nil {
        t.Fatalf("StreamMessages failed: %v", err)
    }
    var sawStop bool
    for ev := range ch {
        if ev.Type == "message_stop" {
            sawStop = true
        }
    }
    if !sawStop {
        t.Fatal("stream was truncated before message_stop: Client.Timeout is cutting the stream body")
    }
}

func TestParseAnthropicStreamEvents_SlowConsumerNotTruncated(t *testing.T) {
    // Regression: parseAnthropicStreamEvents used a non-blocking send
    // (select { case ch<-e: default: return }) so when the channel buffer
    // (64) filled under a slow consumer it aborted the whole stream,
    // dropping every remaining event including message_stop. Claude Code
    // saw a stream that "stopped arriving" mid-response. A blocking send
    // lets the producer wait for the consumer instead of truncating.
    const total = 200 // > 64 buffer
    var sbuf strings.Builder
    sbuf.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_b\",\"model\":\"glm5.2\"}}\n\n")
    for i := 0; i < total; i++ {
        sbuf.WriteString("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
    }
    sbuf.WriteString("data: {\"type\":\"message_stop\"}\n\n")

    p := NewAnthropicProvider("glm52", config.BackendConfig{BaseURL: "http://localhost"})
    ch := make(chan AnthropicStreamEvent, 64)
    done := make(chan struct{})
    go func() {
        defer close(done)
        p.parseAnthropicStreamEvents(context.Background(), strings.NewReader(sbuf.String()), ch)
        close(ch)
    }()

    var got int
    var sawStop bool
    for ev := range ch {
        if ev.Type == "content_block_delta" {
            got++
        }
        if ev.Type == "message_stop" {
            sawStop = true
        }
        // Slow consumer: backpressure fills the 64-buffer.
        if got%4 == 0 {
            time.Sleep(2 * time.Millisecond)
        }
    }
    <-done
    if got != total {
        t.Fatalf("stream truncated by backpressure: got %d deltas, want %d", got, total)
    }
    if !sawStop {
        t.Fatal("stream truncated before message_stop: consumer saw no terminal event")
    }
}

// Regression: a blocking send on a full buffer deadlocks the producer
// goroutine when the consumer stops reading on client cancel (no Cancel
// method on AnthropicProvider). The producer must observe ctx cancellation
// and exit via select{case <-ctx.Done()} so the goroutine + upstream
// connection are released — not leaked forever blocked on ch <-.
func TestParseAnthropicStreamEvents_ContextCancelUnblocksProducer(t *testing.T) {
    // Build more events than the 64-buffer so the producer blocks on send.
    const total = 200
    var sbuf strings.Builder
    sbuf.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_c\",\"model\":\"glm5.2\"}}\n\n")
    for i := 0; i < total; i++ {
        sbuf.WriteString("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
    }
    p := NewAnthropicProvider("glm52", config.BackendConfig{BaseURL: "http://localhost"})
    ch := make(chan AnthropicStreamEvent, 64)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    done := make(chan struct{})
    go func() {
        defer close(done)
        p.parseAnthropicStreamEvents(ctx, &blockingReader{data: []byte(sbuf.String())}, ch)
        close(ch)
    }()
    // Drain the initial buffer, then cancel mid-stream (consumer stops).
    var got int
    for ev := range ch {
        if ev.Type == "content_block_delta" {
            got++
        }
        if got == 30 {
            cancel()
            // Stop reading — emulate client cancel. Discard remainder.
            for range ch {
            }
            break
        }
    }
    select {
    case <-done:
        // Producer observed ctx cancellation and exited cleanly.
    case <-time.After(2 * time.Second):
        t.Fatal("producer goroutine leaked: blocked on ch<- after client cancel (ctx not honored)")
    }
    _ = got
}

// blockingReader serves data once then blocks on subsequent reads so the
// parser stays parked in a send; combined with a cancel it exercises the
// ctx.Done() branch without a real upstream.
type blockingReader struct {
    data []byte
    sent bool
}

func (b *blockingReader) Read(p []byte) (int, error) {
    if !b.sent {
        n := copy(p, b.data)
        b.sent = true
        return n, nil
    }
    // Stream ended; parser breaks on EOF and returns, closing the channel.
    return 0, io.EOF
}
