package adapter

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #580: parseAnthropicSSE dropped tool-call events when relaying an
// Anthropic-protocol upstream as OpenAI-style chunks — only text_delta was
// forwarded, so streamed tool-use requests produced a text preface and zero
// delta.tool_calls while the non-stream path returned them intact.

// 580SSEBody builds the Anthropic SSE payload: a text block then a tool_use
// block fed via input_json_delta, ending with stop_reason=tool_use.
func sseBody580(t *testing.T) [][]byte {
    t.Helper()
    ev := func(raw string) []byte { return []byte(raw) }
    return [][]byte{
        ev(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5","usage":{"input_tokens":10,"output_tokens":0}}}`),
        ev(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
        ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look."}}`),
        ev(`{"type":"content_block_stop","index":0}`),
        ev(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"insert_table","input":{}}}`),
        ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"rows\":"}}`),
        ev(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"3}"}}`),
        ev(`{"type":"content_block_stop","index":1}`),
        ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":49}}`),
        ev(`{"type":"message_stop"}`),
    }
}

func new580Provider(t *testing.T) (*AnthropicProvider, <-chan StreamChunk) {
    t.Helper()
    frames := sseBody580(t)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        writeAnthropicSSE(w, frames)
    }))
    t.Cleanup(srv.Close)
    p := NewAnthropicProvider("anthropic", config.BackendConfig{
        BaseURL: srv.URL,
        APIKey:  "test-key",
    })
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "glm-5",
        Messages: []ChatMessage{{Role: "user", Content: "insert a table"}},
    })
    if err != nil {
        t.Fatalf("StreamChat: %v", err)
    }
    return p, ch
}

// collect580 drains the stream, gathering tool_call entries by index and the
// final finish reason.
func collect580(t *testing.T, ch <-chan StreamChunk) (map[int]map[string]string, string, string) {
    t.Helper()
    toolCalls := map[int]map[string]string{}
    var finish, text string
    for c := range ch {
        for _, choice := range c.Choices {
            if choice.FinishReason != nil {
                finish = *choice.FinishReason
            }
            // Normalize through JSON: chunks are in-process structs whose
            // Delta map holds typed slices ([]map[string]any), not the
            // []any/json.RawMessage shapes a wire round-trip would produce.
            norm, _ := json.Marshal(choice.Delta)
            var m map[string]any
            if err := json.Unmarshal(norm, &m); err == nil {
                if tc, ok := m["tool_calls"].([]any); ok {
                    for _, raw := range tc {
                        entry, ok := raw.(map[string]any)
                        if !ok {
                            continue
                        }
                        idx := 0
                        switch v := entry["index"].(type) {
                        case float64:
                            idx = int(v)
                        case int:
                            idx = v
                        }
                        cur, ok := toolCalls[idx]
                        if !ok {
                            cur = map[string]string{}
                            toolCalls[idx] = cur
                        }
                        if id, ok := entry["id"].(string); ok && id != "" {
                            cur["id"] = id
                        }
                        if fn, ok := entry["function"].(map[string]any); ok {
                            if n, ok := fn["name"].(string); ok && n != "" {
                                cur["name"] = n
                            }
                            if a, ok := fn["arguments"].(string); ok {
                                cur["arguments"] += a
                            }
                        }
                    }
                }
                if s, ok := m["content"].(string); ok {
                    text += s
                }
            }
        }
    }
    return toolCalls, finish, text
}

func TestAnthropicSSE_ToolUseStream_StartDelta(t *testing.T) {
    _, ch := new580Provider(t)
    toolCalls, finish, text := collect580(t, ch)
    tc, ok := toolCalls[1]
    if !ok {
        t.Fatalf("tool_calls entry for index 1 missing (#580), got %v; text=%q", toolCalls, text)
    }
    if tc["id"] != "toolu_1" {
        t.Errorf("tool call id = %q, want toolu_1", tc["id"])
    }
    if tc["name"] != "insert_table" {
        t.Errorf("tool call name = %q, want insert_table", tc["name"])
    }
    if tc["arguments"] != `{"rows":3}` {
        t.Errorf("tool call arguments = %q, want {\"rows\":3}", tc["arguments"])
    }
    if !strings.Contains(text, "Let me look.") {
        t.Errorf("text preface lost: %q", text)
    }
    _ = finish
}

func TestAnthropicSSE_ToolUseStopReason(t *testing.T) {
    _, ch := new580Provider(t)
    _, finish, _ := collect580(t, ch)
    if finish != "tool_calls" {
        t.Fatalf("finish_reason = %q, want tool_calls (#580 ask 3: Anthropic tool_use maps to OpenAI tool_calls)", finish)
    }
}

func TestAnthropicSSE_EndTurnStillStop(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        writeAnthropicSSE(w, [][]byte{
            []byte(`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"glm-5","usage":{"input_tokens":5,"output_tokens":0}}}`),
            []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
            []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`),
            []byte(`{"type":"content_block_stop","index":0}`),
            []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`),
            []byte(`{"type":"message_stop"}`),
        })
    }))
    defer srv.Close()
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "glm-5",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    _, finish, _ := collect580(t, ch)
    if finish != "stop" {
        t.Fatalf("end_turn must still map to stop, got %q", finish)
    }
}

func TestAnthropicSSE_MaxTokensMapsToLength(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        writeAnthropicSSE(w, [][]byte{
            []byte(`{"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"glm-5","usage":{"input_tokens":5,"output_tokens":0}}}`),
            []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`),
            []byte(`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":9}}`),
            []byte(`{"type":"message_stop"}`),
        })
    }))
    defer srv.Close()
    p := NewAnthropicProvider("anthropic", config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"})
    ch, err := p.StreamChat(context.Background(), &ChatRequest{
        Model:    "glm-5",
        Messages: []ChatMessage{{Role: "user", Content: "hi"}},
    })
    if err != nil {
        t.Fatal(err)
    }
    _, finish, _ := collect580(t, ch)
    if finish != "length" {
        t.Fatalf("max_tokens must map to OpenAI length, got %q", finish)
    }
}
