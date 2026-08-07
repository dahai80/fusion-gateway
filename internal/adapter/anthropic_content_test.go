package adapter

import (
    "encoding/json"
    "log/slog"
    "testing"
)

func TestConvertContentToBlocks_Nil(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_Nil")
    result := convertContentToBlocks(nil)
    if result != nil {
        t.Errorf("expected nil, got %v", result)
    }
}

func TestConvertContentToBlocks_EmptyString(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_EmptyString")
    result := convertContentToBlocks("")
    if result != nil {
        t.Errorf("expected nil for empty string, got %v", result)
    }
}

func TestConvertContentToBlocks_String(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_String")
    result := convertContentToBlocks("hello")
    if len(result) != 1 || result[0].Type != "text" || result[0].Text != "hello" {
        t.Errorf("unexpected result: %v", result)
    }
}

func TestConvertContentToBlocks_TextArray(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_TextArray")
    content := []interface{}{
        map[string]interface{}{"type": "text", "text": "hello"},
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "text" || result[0].Text != "hello" {
        t.Errorf("unexpected result: %v", result)
    }
}

func TestConvertContentToBlocks_ImageDataURL(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_ImageDataURL")
    content := []interface{}{
        map[string]interface{}{
            "type": "image_url",
            "image_url": map[string]interface{}{
                "url": "data:image/png;base64,iVBORw0KGgo=",
            },
        },
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "image" {
        t.Errorf("unexpected result: %v", result)
    }
    if result[0].Source == nil || result[0].Source.Type != "base64" {
        t.Error("expected base64 source")
    }
}

func TestConvertContentToBlocks_ImageURL(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_ImageURL")
    content := []interface{}{
        map[string]interface{}{
            "type": "image_url",
            "image_url": map[string]interface{}{
                "url": "https://example.com/img.png",
            },
        },
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "image" {
        t.Errorf("unexpected result: %v", result)
    }
    if result[0].Source == nil || result[0].Source.Type != "url" {
        t.Error("expected url source")
    }
}

func TestConvertContentToBlocks_ToolUse(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_ToolUse")
    content := []interface{}{
        map[string]interface{}{
            "type":  "tool_use",
            "id":    "tool1",
            "name":  "get_weather",
            "input": map[string]interface{}{"city": "SF"},
        },
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "tool_use" {
        t.Errorf("unexpected result: %v", result)
    }
    if result[0].ID != "tool1" || result[0].Name != "get_weather" {
        t.Errorf("tool_use mismatch: id=%s name=%s", result[0].ID, result[0].Name)
    }
}

func TestConvertContentToBlocks_ToolUse_RawInput(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_ToolUse_RawInput")
    inputJSON, _ := json.Marshal(map[string]interface{}{"city": "SF"})
    content := []interface{}{
        map[string]interface{}{
            "type":  "tool_use",
            "id":    "tool1",
            "name":  "get_weather",
            "input": json.RawMessage(inputJSON),
        },
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "tool_use" {
        t.Errorf("unexpected result: %v", result)
    }
}

func TestConvertContentToBlocks_ToolResult(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_ToolResult")
    content := []interface{}{
        map[string]interface{}{
            "type":        "tool_result",
            "tool_use_id": "tool1",
            "content":     "sunny",
        },
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "tool_result" {
        t.Errorf("unexpected result: %v", result)
    }
    if result[0].ToolUseID != "tool1" {
        t.Errorf("tool_use_id mismatch: %s", result[0].ToolUseID)
    }
}

func TestConvertContentToBlocks_NonMapItem(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_NonMapItem")
    content := []interface{}{
        "not-a-map",
        map[string]interface{}{"type": "text", "text": "valid"},
    }
    result := convertContentToBlocks(content)
    if len(result) != 1 {
        t.Errorf("expected 1 block (non-map should be skipped), got %d", len(result))
    }
}

func TestConvertContentToBlocks_DefaultType(t *testing.T) {
    slog.Info("test ConvertContentToBlocks_DefaultType")
    content := 42
    result := convertContentToBlocks(content)
    if len(result) != 1 || result[0].Type != "text" {
        t.Errorf("expected text block for default type, got %v", result)
    }
}

func TestContentToString_String(t *testing.T) {
    slog.Info("test ContentToString_String")
    result := contentToString("hello world")
    if result != "hello world" {
        t.Errorf("expected 'hello world', got '%s'", result)
    }
}

func TestContentToString_Array(t *testing.T) {
    slog.Info("test ContentToString_Array")
    content := []interface{}{
        map[string]interface{}{"type": "text", "text": "line1"},
        map[string]interface{}{"type": "text", "text": "line2"},
        map[string]interface{}{"type": "image_url", "url": "http://example.com"},
    }
    result := contentToString(content)
    if result != "line1\nline2" {
        t.Errorf("expected 'line1\\nline2', got '%s'", result)
    }
}

func TestContentToString_ArrayNonMap(t *testing.T) {
    slog.Info("test ContentToString_ArrayNonMap")
    content := []interface{}{
        "not-a-map",
        map[string]interface{}{"type": "text", "text": "valid"},
    }
    result := contentToString(content)
    if result != "valid" {
        t.Errorf("expected 'valid', got '%s'", result)
    }
}

func TestContentToString_Default(t *testing.T) {
    slog.Info("test ContentToString_Default")
    result := contentToString(42)
    if result != "42" {
        t.Errorf("expected '42', got '%s'", result)
    }
}

func TestAnthropicBlocksToContent_Empty(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_Empty")
    result := anthropicBlocksToContent(nil)
    if result != "" {
        t.Errorf("expected empty string, got %v", result)
    }
}

func TestAnthropicBlocksToContent_SingleText(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_SingleText")
    blocks := []AnthropicContentBlock{{Type: "text", Text: "hello"}}
    result := anthropicBlocksToContent(blocks)
    if result != "hello" {
        t.Errorf("expected 'hello', got %v", result)
    }
}

func TestAnthropicBlocksToContent_Multiple(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_Multiple")
    blocks := []AnthropicContentBlock{
        {Type: "text", Text: "hello"},
        {Type: "image", Source: &AnthropicImageSource{Type: "base64", MediaType: "image/png", Data: "abc123"}},
    }
    result := anthropicBlocksToContent(blocks)
    arr, ok := result.([]interface{})
    if !ok || len(arr) != 2 {
        t.Fatalf("expected 2-element array, got %v", result)
    }
    first, ok := arr[0].(map[string]interface{})
    if !ok || first["type"] != "text" {
        t.Error("first block should be text")
    }
}

func TestAnthropicBlocksToContent_ToolUse(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_ToolUse")
    inputJSON, _ := json.Marshal(map[string]interface{}{"city": "SF"})
    blocks := []AnthropicContentBlock{
        {Type: "tool_use", ID: "tool1", Name: "get_weather", Input: inputJSON},
    }
    result := anthropicBlocksToContent(blocks)
    arr, ok := result.([]interface{})
    if !ok || len(arr) != 1 {
        t.Fatalf("expected 1-element array, got %v", result)
    }
    m, ok := arr[0].(map[string]interface{})
    if !ok || m["type"] != "tool_use" {
        t.Error("expected tool_use type")
    }
}

func TestAnthropicBlocksToContent_ToolResult(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_ToolResult")
    blocks := []AnthropicContentBlock{
        {Type: "tool_result", ToolUseID: "tool1", ACContent: "sunny", IsError: false},
    }
    result := anthropicBlocksToContent(blocks)
    arr, ok := result.([]interface{})
    if !ok || len(arr) != 1 {
        t.Fatalf("expected 1-element array, got %v", result)
    }
    m, ok := arr[0].(map[string]interface{})
    if !ok || m["type"] != "tool_result" {
        t.Error("expected tool_result type")
    }
}

func TestAnthropicBlocksToContent_ImageNoSource(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_ImageNoSource")
    blocks := []AnthropicContentBlock{
        {Type: "image", Source: nil},
        {Type: "text", Text: "after"},
    }
    result := anthropicBlocksToContent(blocks)
    arr, ok := result.([]interface{})
    if !ok || len(arr) != 1 {
        t.Fatalf("expected 1-element array (nil source image skipped), got %v", result)
    }
    if arr[0].(map[string]interface{})["type"] != "text" {
        t.Error("expected text block only")
    }
}

func TestAnthropicBlocksToContent_DefaultType(t *testing.T) {
    slog.Info("test AnthropicBlocksToContent_DefaultType")
    blocks := []AnthropicContentBlock{
        {Type: "thinking", Text: "hmm"},
    }
    result := anthropicBlocksToContent(blocks)
    arr, ok := result.([]interface{})
    if !ok || len(arr) != 1 {
        t.Fatalf("expected 1-element array, got %v", result)
    }
}

func TestOpenAIToAnthropic_Tools(t *testing.T) {
    slog.Info("test OpenAIToAnthropic_Tools")
    temp := 0.7
    topP := 0.9
    req := &ChatRequest{
        Model:       "claude-3",
        Temperature: &temp,
        TopP:        &topP,
        Stop:        []string{"stop1"},
        Stream:      true,
        Messages: []ChatMessage{
            {Role: "user", Content: "hello"},
            {Role: "assistant", Content: ""},
        },
        Tools: []interface{}{
            map[string]interface{}{
                "type": "function",
                "function": map[string]interface{}{
                    "name":        "get_weather",
                    "description": "Get weather",
                    "parameters":  map[string]interface{}{"type": "object"},
                },
            },
            map[string]interface{}{
                "type": "not_function",
            },
            "not_a_map",
        },
        ToolChoice: map[string]interface{}{"type": "auto"},
    }
    antReq := OpenAIToAnthropic(req)
    if len(antReq.Tools) != 1 {
        t.Fatalf("expected 1 tool, got %d", len(antReq.Tools))
    }
    if antReq.Tools[0].Name != "get_weather" {
        t.Errorf("expected get_weather, got %s", antReq.Tools[0].Name)
    }
    if antReq.ToolChoice == nil {
        t.Error("expected tool_choice")
    }
    if len(antReq.StopSequences) != 1 {
        t.Errorf("expected 1 stop sequence, got %d", len(antReq.StopSequences))
    }
    if antReq.Stream != true {
        t.Error("expected stream true")
    }
}

func TestOpenAIToAnthropic_ToolsWithJSONParams(t *testing.T) {
    slog.Info("test OpenAIToAnthropic_ToolsWithJSONParams")
    paramsJSON, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": map[string]interface{}{}})
    req := &ChatRequest{
        Model: "claude-3",
        Messages: []ChatMessage{
            {Role: "user", Content: "hello"},
        },
        Tools: []interface{}{
            map[string]interface{}{
                "type": "function",
                "function": map[string]interface{}{
                    "name":        "search",
                    "description": "Search",
                    "parameters":  json.RawMessage(paramsJSON),
                },
            },
        },
    }
    antReq := OpenAIToAnthropic(req)
    if len(antReq.Tools) != 1 {
        t.Fatalf("expected 1 tool, got %d", len(antReq.Tools))
    }
}

func TestOpenAIToAnthropic_AssistantWithEmptyContent(t *testing.T) {
    slog.Info("test OpenAIToAnthropic_AssistantWithEmptyContent")
    req := &ChatRequest{
        Model: "claude-3",
        Messages: []ChatMessage{
            {Role: "assistant", Content: ""},
        },
    }
    antReq := OpenAIToAnthropic(req)
    if len(antReq.Messages) != 1 {
        t.Fatalf("expected 1 message, got %d", len(antReq.Messages))
    }
    if len(antReq.Messages[0].Content) != 1 || antReq.Messages[0].Content[0].Type != "text" {
        t.Errorf("assistant with empty content should get text block")
    }
}

func TestOpenAIToAnthropic_NoOptional(t *testing.T) {
    slog.Info("test OpenAIToAnthropic_NoOptional")
    req := &ChatRequest{
        Model: "claude-3",
        Messages: []ChatMessage{
            {Role: "user", Content: "hi"},
        },
    }
    antReq := OpenAIToAnthropic(req)
    if antReq.MaxTokens != 4096 {
        t.Errorf("expected default 4096, got %d", antReq.MaxTokens)
    }
    if antReq.Temperature != nil {
        t.Error("expected nil temperature")
    }
    if antReq.TopP != nil {
        t.Error("expected nil topP")
    }
}

func TestAnthropicToOpenAIChatRequest_NonStringSystem(t *testing.T) {
    slog.Info("test AnthropicToOpenAIChatRequest_NonStringSystem")
    antReq := &AnthropicRequest{
        Model:     "claude-3",
        MaxTokens: 1024,
        System:    map[string]interface{}{"type": "text", "text": "system prompt"},
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}}},
        },
    }
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if len(chatReq.Messages) < 2 {
        t.Fatalf("expected at least 2 messages, got %d", len(chatReq.Messages))
    }
    if chatReq.Messages[0].Role != "system" {
        t.Errorf("expected system role, got %s", chatReq.Messages[0].Role)
    }
}

func TestAnthropicToOpenAIChatRequest_WithTools(t *testing.T) {
    slog.Info("test AnthropicToOpenAIChatRequest_WithTools")
    topP := 0.9
    antReq := &AnthropicRequest{
        Model:       "claude-3",
        MaxTokens:   2048,
        TopP:        &topP,
        StopSequences: []string{"stop"},
        System:      "system prompt",
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}}},
        },
        Tools: []AnthropicTool{
            {Name: "search", Description: "Search the web", InputSchema: json.RawMessage(`{"type":"object"}`)},
        },
        ToolChoice: map[string]interface{}{"type": "auto"},
    }
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if chatReq.Tools == nil {
        t.Fatal("expected tools")
    }
    tools, ok := chatReq.Tools.([]interface{})
    if !ok || len(tools) != 1 {
        t.Fatalf("expected 1 tool, got %v", chatReq.Tools)
    }
    if chatReq.ToolChoice == nil {
        t.Error("expected tool_choice")
    }
    if len(chatReq.Stop) != 1 {
        t.Errorf("expected 1 stop, got %d", len(chatReq.Stop))
    }
}

func TestAnthropicToOpenAIChatRequest_NoSystem(t *testing.T) {
    slog.Info("test AnthropicToOpenAIChatRequest_NoSystem")
    antReq := &AnthropicRequest{
        Model:     "claude-3",
        MaxTokens: 1024,
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}}},
        },
    }
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if len(chatReq.Messages) != 1 {
        t.Fatalf("expected 1 message (no system), got %d", len(chatReq.Messages))
    }
}

func TestAnthropicToOpenAIChatRequest_EmptyStringSystem(t *testing.T) {
    slog.Info("test AnthropicToOpenAIChatRequest_EmptyStringSystem")
    antReq := &AnthropicRequest{
        Model:     "claude-3",
        MaxTokens: 1024,
        System:    "",
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}}},
        },
    }
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if len(chatReq.Messages) != 1 {
        t.Fatalf("expected 1 message (empty system skipped), got %d", len(chatReq.Messages))
    }
}

func TestAnthropicMessage_UnmarshalStringContent(t *testing.T) {
    slog.Info("test AnthropicMessage_UnmarshalStringContent")
    body := `{"role":"user","content":"reply ok"}`
    var msg AnthropicMessage
    if err := json.Unmarshal([]byte(body), &msg); err != nil {
        t.Fatalf("string content must unmarshal without error: %v", err)
    }
    if msg.Role != "user" {
        t.Fatalf("expected role user, got %q", msg.Role)
    }
    if len(msg.Content) != 1 {
        t.Fatalf("expected string content normalized to 1 block, got %d", len(msg.Content))
    }
    if msg.Content[0].Type != "text" || msg.Content[0].Text != "reply ok" {
        t.Fatalf("expected text block 'reply ok', got %+v", msg.Content[0])
    }
}

func TestAnthropicMessage_UnmarshalArrayContent(t *testing.T) {
    slog.Info("test AnthropicMessage_UnmarshalArrayContent")
    body := `{"role":"user","content":[{"type":"text","text":"hi"}]}`
    var msg AnthropicMessage
    if err := json.Unmarshal([]byte(body), &msg); err != nil {
        t.Fatalf("array content must unmarshal without error: %v", err)
    }
    if len(msg.Content) != 1 || msg.Content[0].Text != "hi" {
        t.Fatalf("expected 1 block 'hi', got %+v", msg.Content)
    }
}

func TestAnthropicRequest_UnmarshalFullStringContent(t *testing.T) {
    slog.Info("test AnthropicRequest_UnmarshalFullStringContent")
    body := `{"model":"claude-3","max_tokens":8,"messages":[{"role":"user","content":"ok"}]}`
    var req AnthropicRequest
    if err := json.Unmarshal([]byte(body), &req); err != nil {
        t.Fatalf("full request with string content must parse: %v", err)
    }
    if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 {
        t.Fatalf("expected 1 message with 1 normalized block, got %+v", req.Messages)
    }
}

func TestEmbeddingRequest_UnmarshalStringInput(t *testing.T) {
    slog.Info("test EmbeddingRequest_UnmarshalStringInput")
    body := `{"model":"bge-m3","input":"hello world"}`
    var req EmbeddingRequest
    if err := json.Unmarshal([]byte(body), &req); err != nil {
        t.Fatalf("string input must unmarshal without error: %v", err)
    }
    if len(req.Input) != 1 || req.Input[0] != "hello world" {
        t.Fatalf("expected [hello world], got %+v", req.Input)
    }
}

func TestEmbeddingRequest_UnmarshalArrayInput(t *testing.T) {
    slog.Info("test EmbeddingRequest_UnmarshalArrayInput")
    body := `{"model":"bge-m3","input":["a","b"]}`
    var req EmbeddingRequest
    if err := json.Unmarshal([]byte(body), &req); err != nil {
        t.Fatalf("array input must unmarshal without error: %v", err)
    }
    if len(req.Input) != 2 || req.Input[0] != "a" || req.Input[1] != "b" {
        t.Fatalf("expected [a b], got %+v", req.Input)
    }
}

func TestEmbeddingRequest_UnmarshalEmptyInput(t *testing.T) {
    slog.Info("test EmbeddingRequest_UnmarshalEmptyInput")
    body := `{"model":"bge-m3"}`
    var req EmbeddingRequest
    if err := json.Unmarshal([]byte(body), &req); err != nil {
        t.Fatalf("missing input must unmarshal without error: %v", err)
    }
    if len(req.Input) != 0 {
        t.Fatalf("expected empty input, got %+v", req.Input)
    }
}
