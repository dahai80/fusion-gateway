package adapter

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

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
