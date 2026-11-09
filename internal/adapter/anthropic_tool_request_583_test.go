package adapter

import (
    "encoding/json"
    "testing"
)

// #583: OpenAIToAnthropic dropped OpenAI tool-call transcript messages — the
// assistant turn's top-level tool_calls field was ignored and role:"tool"
// result messages passed through with a role Anthropic rejects ("tool"). A
// tool-loop client therefore replans from scratch every round and repeats the
// same action indefinitely against Anthropic-protocol providers.

func req583() *ChatRequest {
    return &ChatRequest{
        Model: "glm-5",
        Messages: []ChatMessage{
            {Role: "user", Content: "write the cells"},
            {
                Role:    "assistant",
                Content: nil,
                ToolCalls: []map[string]interface{}{
                    {
                        "id":   "call_1",
                        "type": "function",
                        "function": map[string]interface{}{
                            "name":      "write_cells",
                            "arguments": `{"start_cell":"A1"}`,
                        },
                    },
                },
            },
            {Role: "tool", ToolCallID: "call_1", Content: "Done. Wrote to Sheet1."},
        },
    }
}

func TestOpenAIToAnthropic_ToolCallsBecomeToolUseBlock(t *testing.T) {
    ant := OpenAIToAnthropic(req583())
    if len(ant.Messages) != 3 {
        t.Fatalf("messages = %d, want 3 (user, assistant, user-with-tool_result)", len(ant.Messages))
    }
    asst := ant.Messages[1]
    if asst.Role != "assistant" {
        t.Fatalf("message[1].role = %q, want assistant", asst.Role)
    }
    var toolUse *AnthropicContentBlock
    for i := range asst.Content {
        if asst.Content[i].Type == "tool_use" {
            toolUse = &asst.Content[i]
        }
    }
    if toolUse == nil {
        t.Fatalf("assistant turn missing tool_use block (#583), content=%+v", asst.Content)
    }
    if toolUse.ID != "call_1" {
        t.Errorf("tool_use.id = %q, want call_1", toolUse.ID)
    }
    if toolUse.Name != "write_cells" {
        t.Errorf("tool_use.name = %q, want write_cells", toolUse.Name)
    }
    var input map[string]interface{}
    if err := json.Unmarshal(toolUse.Input, &input); err != nil {
        t.Fatalf("tool_use.input not valid JSON: %v (%s)", err, toolUse.Input)
    }
    if input["start_cell"] != "A1" {
        t.Errorf("tool_use.input = %s, want start_cell=A1", toolUse.Input)
    }
}

func TestOpenAIToAnthropic_ToolResultRidesUserMessage(t *testing.T) {
    ant := OpenAIToAnthropic(req583())
    last := ant.Messages[len(ant.Messages)-1]
    if last.Role != "user" {
        t.Fatalf("tool result message role = %q, want user (#583: Anthropic has no tool role)", last.Role)
    }
    for _, m := range ant.Messages {
        if m.Role == "tool" {
            t.Fatalf("role %q leaked into Anthropic request", m.Role)
        }
    }
    var tr *AnthropicContentBlock
    for i := range last.Content {
        if last.Content[i].Type == "tool_result" {
            tr = &last.Content[i]
        }
    }
    if tr == nil {
        t.Fatalf("tool_result block missing (#583), content=%+v", last.Content)
    }
    if tr.ToolUseID != "call_1" {
        t.Errorf("tool_result.tool_use_id = %q, want call_1", tr.ToolUseID)
    }
    if tr.ACContent != "Done. Wrote to Sheet1." {
        t.Errorf("tool_result.content = %v, want the tool output string", tr.ACContent)
    }
}

func TestOpenAIToAnthropic_ConsecutiveToolMessagesMerged(t *testing.T) {
    req := &ChatRequest{
        Model: "glm-5",
        Messages: []ChatMessage{
            {Role: "user", Content: "do both"},
            {
                Role: "assistant",
                ToolCalls: []map[string]interface{}{
                    {"id": "call_a", "type": "function", "function": map[string]interface{}{"name": "f1", "arguments": "{}"}},
                    {"id": "call_b", "type": "function", "function": map[string]interface{}{"name": "f2", "arguments": "{}"}},
                },
            },
            {Role: "tool", ToolCallID: "call_a", Content: "result a"},
            {Role: "tool", ToolCallID: "call_b", Content: "result b"},
        },
    }
    ant := OpenAIToAnthropic(req)
    // user, assistant, user(tool_result x2) — Anthropic enforces alternating roles.
    if len(ant.Messages) != 3 {
        t.Fatalf("messages = %d, want 3 (consecutive tool messages merge into one user turn)", len(ant.Messages))
    }
    last := ant.Messages[2]
    if last.Role != "user" {
        t.Fatalf("merged turn role = %q, want user", last.Role)
    }
    results := map[string]string{}
    for _, b := range last.Content {
        if b.Type == "tool_result" {
            if s, ok := b.ACContent.(string); ok {
                results[b.ToolUseID] = s
            }
        }
    }
    if results["call_a"] != "result a" || results["call_b"] != "result b" {
        t.Fatalf("tool results = %v, want call_a=result a, call_b=result b", results)
    }
}

func TestOpenAIToAnthropic_ToolRunThenUserTurnKeepsOrder(t *testing.T) {
    req := &ChatRequest{
        Model: "glm-5",
        Messages: []ChatMessage{
            {Role: "user", Content: "go"},
            {
                Role: "assistant",
                ToolCalls: []map[string]interface{}{
                    {"id": "call_x", "type": "function", "function": map[string]interface{}{"name": "fx", "arguments": "{}"}},
                },
            },
            {Role: "tool", ToolCallID: "call_x", Content: "ok"},
            {Role: "assistant", Content: "all done"},
            {Role: "user", Content: "thanks"},
        },
    }
    ant := OpenAIToAnthropic(req)
    var roles []string
    for _, m := range ant.Messages {
        roles = append(roles, m.Role)
    }
    want := []string{"user", "assistant", "user", "assistant", "user"}
    if len(roles) != len(want) {
        t.Fatalf("roles = %v, want %v", roles, want)
    }
    for i := range want {
        if roles[i] != want[i] {
            t.Fatalf("roles = %v, want %v (tool-result turn must flush before next assistant turn)", roles, want)
        }
    }
}

func TestOpenAIToAnthropic_ToolCallsOnlyAssistantNoEmptyTextBlock(t *testing.T) {
    // Anthropic rejects empty text blocks; an assistant turn that carries only
    // tool_calls (content=null on the wire) must not grow one.
    ant := OpenAIToAnthropic(req583())
    asst := ant.Messages[1]
    for _, b := range asst.Content {
        if b.Type == "text" && b.Text == "" {
            t.Fatalf("empty text block emitted alongside tool_use: %+v", asst.Content)
        }
    }
}
