// #319: AnthropicToOpenAIChatRequest must be a real Anthropic→OpenAI
// translation. tool_use blocks become assistant tool_calls; tool_result
// blocks become role:"tool" messages with tool_call_id — NOT generic content
// parts, which the engine's chat template rejects and silently drops,
// leaving the model blind to tool output (Claude Code infinite re-call loop).
package adapter

import (
    "encoding/json"
    "strings"
    "testing"
)

func toolConvRequest() *AnthropicRequest {
    return &AnthropicRequest{
        Model:     "test-model",
        MaxTokens: 300,
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "Read notes.txt and tell me how many lines it contains."}}},
            {Role: "assistant", Content: []AnthropicContentBlock{
                {Type: "text", Text: "I'll read the file."},
                {Type: "tool_use", ID: "tu1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/notes.txt"}`)},
            }},
            {Role: "user", Content: []AnthropicContentBlock{
                {Type: "tool_result", ToolUseID: "tu1", ACContent: "alpha\nbeta\ngamma\ndelta\n"},
                {Type: "text", Text: "So how many lines?"},
            }},
        },
        Tools: []AnthropicTool{{
            Name: "Read", Description: "Read a file",
            InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
        }},
    }
}

// AC from the issue repro: the converted request must carry the tool result
// as an OpenAI role:"tool" message and the assistant call as tool_calls.
func TestAnthropicToOpenAIChatRequest_ToolRoundTrip(t *testing.T) {
    chatReq := AnthropicToOpenAIChatRequest(toolConvRequest())

    if len(chatReq.Messages) != 4 {
        t.Fatalf("expected 4 messages (user, assistant+tool_calls, tool, user), got %d", len(chatReq.Messages))
    }

    assistant := chatReq.Messages[1]
    if assistant.Role != "assistant" {
        t.Fatalf("message[1] role = %s, want assistant", assistant.Role)
    }
    if text, ok := assistant.Content.(string); !ok || text != "I'll read the file." {
        t.Fatalf("assistant content = %v, want text block as string", assistant.Content)
    }
    calls, ok := assistant.ToolCalls.([]interface{})
    if !ok || len(calls) != 1 {
        t.Fatalf("assistant tool_calls missing, got %v", assistant.ToolCalls)
    }
    tc, _ := calls[0].(map[string]interface{})
    if tc["id"] != "tu1" || tc["type"] != "function" {
        t.Fatalf("tool_call id/type = %v/%v, want tu1/function", tc["id"], tc["type"])
    }
    fn, _ := tc["function"].(map[string]interface{})
    if fn["name"] != "Read" {
        t.Fatalf("tool_call function name = %v, want Read", fn["name"])
    }
    if args, _ := fn["arguments"].(string); !strings.Contains(args, "notes.txt") {
        t.Fatalf("tool_call arguments = %v, want the input JSON string", fn["arguments"])
    }

    toolMsg := chatReq.Messages[2]
    if toolMsg.Role != "tool" {
        t.Fatalf("message[2] role = %s, want tool", toolMsg.Role)
    }
    if toolMsg.ToolCallID != "tu1" {
        t.Fatalf("tool_call_id = %s, want tu1", toolMsg.ToolCallID)
    }
    if content, _ := toolMsg.Content.(string); !strings.Contains(content, "delta") {
        t.Fatalf("tool message content = %v, want the tool result text", toolMsg.Content)
    }

    trailing := chatReq.Messages[3]
    if trailing.Role != "user" {
        t.Fatalf("message[3] role = %s, want user (remaining text)", trailing.Role)
    }

    // Serialized shape must be valid OpenAI: no tool_use/tool_result parts.
    raw, err := json.Marshal(chatReq)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if strings.Contains(string(raw), "tool_result") || strings.Contains(string(raw), "\"tool_use\"") {
        t.Fatalf("serialized request still carries Anthropic block types: %s", string(raw))
    }
}

// Tool result with block-array content (text + image) flattens to text with
// an image placeholder — never dropped silently.
func TestAnthropicToOpenAIChatRequest_ToolResultBlockContent(t *testing.T) {
    antReq := toolConvRequest()
    antReq.Messages[2].Content = []AnthropicContentBlock{{
        Type:      "tool_result",
        ToolUseID: "tu1",
        ACContent: []interface{}{
            map[string]interface{}{"type": "text", "text": "line1"},
            map[string]interface{}{"type": "image", "source": map[string]interface{}{"data": "x"}},
            map[string]interface{}{"type": "text", "text": "line2"},
        },
    }}

    chatReq := AnthropicToOpenAIChatRequest(antReq)
    var toolMsg *ChatMessage
    for i := range chatReq.Messages {
        if chatReq.Messages[i].Role == "tool" {
            toolMsg = &chatReq.Messages[i]
        }
    }
    if toolMsg == nil {
        t.Fatalf("no role:tool message produced")
    }
    content, _ := toolMsg.Content.(string)
    if !strings.Contains(content, "line1") || !strings.Contains(content, "line2") {
        t.Fatalf("text parts lost: %q", content)
    }
    if !strings.Contains(content, "[image omitted]") {
        t.Fatalf("image part must become a placeholder, got: %q", content)
    }
}

// tool_choice mapping: any → required, tool → named function, auto → auto.
func TestAnthropicToOpenAIChatRequest_ToolChoice(t *testing.T) {
    antReq := toolConvRequest()
    antReq.ToolChoice = map[string]interface{}{"type": "any"}
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if chatReq.ToolChoice != "required" {
        t.Fatalf("any → %v, want required", chatReq.ToolChoice)
    }

    antReq.ToolChoice = map[string]interface{}{"type": "tool", "name": "Read"}
    chatReq = AnthropicToOpenAIChatRequest(antReq)
    m, ok := chatReq.ToolChoice.(map[string]interface{})
    if !ok || m["type"] != "function" {
        t.Fatalf("tool → %v, want named function form", chatReq.ToolChoice)
    }
    fn, _ := m["function"].(map[string]interface{})
    if fn["name"] != "Read" {
        t.Fatalf("named function = %v, want Read", fn["name"])
    }
}

// Plain conversations must convert exactly as before (regression guard).
func TestAnthropicToOpenAIChatRequest_PlainMessagesUnchanged(t *testing.T) {
    antReq := &AnthropicRequest{
        Model:     "m",
        MaxTokens: 10,
        Messages: []AnthropicMessage{
            {Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}},
            {Role: "assistant", Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}}},
        },
    }
    chatReq := AnthropicToOpenAIChatRequest(antReq)
    if len(chatReq.Messages) != 2 {
        t.Fatalf("expected 2 messages, got %d", len(chatReq.Messages))
    }
    if chatReq.Messages[0].Content != "hi" || chatReq.Messages[1].Content != "hello" {
        t.Fatalf("plain text conversion changed: %v", chatReq.Messages)
    }
    if chatReq.Messages[1].ToolCalls != nil {
        t.Fatalf("no tool_use in source — ToolCalls must stay nil")
    }
}
