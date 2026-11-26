package server

// #720: tool-trajectory capture in the rollout tap. Agent traffic (office
// flywheel) must produce trainer-consumable trajectories: assistant
// tool_calls merged from streamed fragments, request tool trajectory
// preserved, tool definitions mirrored, and the redact-mode PII gate
// covering tool arguments.

import (
    "encoding/json"
    "net/http/httptest"
    "os"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/rollout"
)

func Test720_SSEToolCallFragmentsMerge(t *testing.T) {
    body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"make_doc\",\"arguments\":\"{\\\"title\\\"\"}}]}}]}\n\n" +
        "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":\\\"Q3\\\"}\"}}]}}]}\n\n" +
        "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_2\",\"type\":\"function\",\"function\":{\"name\":\"make_sheet\",\"arguments\":\"{}\"}}]}}]}\n\n" +
        "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
        "data: [DONE]\n\n"
    content, finish, toolCalls := parseSSEBody([]byte(body))
    if content != "" || finish != "tool_calls" {
        t.Fatalf("content=%q finish=%q", content, finish)
    }
    if toolCalls == nil {
        t.Fatal("tool_calls not captured from stream")
    }
    var calls []struct {
        ID       string `json:"id"`
        Function struct {
            Name      string `json:"name"`
            Arguments string `json:"arguments"`
        } `json:"function"`
    }
    if err := json.Unmarshal(toolCalls, &calls); err != nil {
        t.Fatalf("decode merged tool_calls: %v", err)
    }
    if len(calls) != 2 {
        t.Fatalf("want 2 merged calls, got %d", len(calls))
    }
    if calls[0].ID != "call_1" || calls[0].Function.Name != "make_doc" ||
        calls[0].Function.Arguments != `{"title":"Q3"}` {
        t.Fatalf("fragment merge wrong: %+v", calls[0])
    }
    if calls[1].Function.Name != "make_sheet" || calls[1].Function.Arguments != "{}" {
        t.Fatalf("second call wrong: %+v", calls[1])
    }
}

func Test720_NonStreamToolCallsCaptured(t *testing.T) {
    raw := []byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"make_doc","arguments":"{\"title\":\"Q3\"}"}}]},"finish_reason":"tool_calls"}]}`)
    content, finish, toolCalls := parseNonStreamBody(raw)
    if finish != "tool_calls" || content != "" {
        t.Fatalf("content=%q finish=%q", content, finish)
    }
    if toolCalls == nil || !strings.Contains(string(toolCalls), "call_9") {
        t.Fatalf("tool_calls not captured: %s", toolCalls)
    }
}

func Test720_RecordCarriesToolTrajectory(t *testing.T) {
    req := &adapter.ChatRequest{
        Model: "test-model",
        Tools: []interface{}{map[string]interface{}{
            "type": "function",
            "function": map[string]interface{}{"name": "make_doc"},
        }},
        Messages: []adapter.ChatMessage{
            {Role: "user", Content: "make the deck"},
            {Role: "assistant", Content: "", ToolCalls: []interface{}{map[string]interface{}{
                "id": "call_1", "type": "function",
                "function": map[string]interface{}{"name": "make_doc", "arguments": "{}"},
            }}},
            {Role: "tool", Content: "ok", ToolCallID: "call_1"},
        },
    }
    assistantTools := json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"make_doc","arguments":"{}"}}]`)
    rec := buildRolloutRecord(req, "", assistantTools, "tool_calls", time.Second, "master", "chat_completions")
    if len(rec.Tools) == 0 || !strings.Contains(string(rec.Tools), "make_doc") {
        t.Fatalf("tool definitions not mirrored: %s", rec.Tools)
    }
    if len(rec.Messages) != 4 {
        t.Fatalf("want 4 messages, got %d", len(rec.Messages))
    }
    if rec.Messages[1].ToolCalls == nil || !strings.Contains(string(rec.Messages[1].ToolCalls), "call_1") {
        t.Fatalf("assistant tool_calls lost: %s", rec.Messages[1].ToolCalls)
    }
    if rec.Messages[2].ToolCallID != "call_1" {
        t.Fatalf("tool result tool_call_id lost: %+v", rec.Messages[2])
    }
    if rec.Messages[3].Role != "assistant" || rec.Messages[3].ToolCalls == nil {
        t.Fatalf("assistant response tool_calls not attached: %+v", rec.Messages[3])
    }
}

func Test720_RedactModeScrubToolArguments(t *testing.T) {
    tap, path := rollout608Tap(t, rollout.ContentModeRedact, nil)
    tap.Record(rollout.Record{
        RolloutID: "ro-x", Model: "m",
        Messages: []rollout.Message{
            {Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"c1","type":"function","function":{"name":"fill_form","arguments":"{\"secret\":\"hunter2\"}"}}]`)},
        },
    })
    recs := rollout608WaitRecords(t, path, 1)
    raw, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("sink read: %v", err)
    }
    data := string(raw)
    if strings.Contains(data, "hunter2") {
        t.Fatalf("redact mode leaked tool arguments: %s", data)
    }
    if !strings.Contains(data, "fill_form") {
        t.Fatalf("tool structure must survive redact: %s", data)
    }
    var msgToolCalls json.RawMessage
    if err := json.Unmarshal(recs[0].Messages[0].ToolCalls, &msgToolCalls); err != nil {
        t.Fatalf("redacted tool_calls must stay valid JSON: %v", err)
    }
}

func Test720_RawModeKeepsToolArguments(t *testing.T) {
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, nil)
    tap.Record(rollout.Record{
        RolloutID: "ro-y", Model: "m",
        Messages: []rollout.Message{
            {Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"k\":\"v\"}"}}]`)},
        },
    })
    rollout608WaitRecords(t, path, 1)
    raw, _ := os.ReadFile(path)
    // Arguments are a JSON string inside the record — escaped in the file.
    if !strings.Contains(string(raw), `\"k\":\"v\"`) || strings.Contains(string(raw), "redacted") {
        t.Fatalf("raw mode must keep arguments verbatim: %s", raw)
    }
}

func Test720_EndToEndTapToolRequest(t *testing.T) {
    // Full emit path with a tool-using request; the sink record must carry
    // the merged assistant tool_calls (what the trainer dataset builder
    // needs from agent traffic).
    s := &Server{}
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, nil)
    s.rolloutTap = tap

    sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_e\",\"type\":\"function\",\"function\":{\"name\":\"calendar\",\"arguments\":\"{}\"}}]}}]}\n\n" +
        "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
        "data: [DONE]\n\n"
    rec := httptest.NewRecorder()
    capw := newRolloutTapCapture(rec, 0, "master")
    _, _ = capw.Write([]byte(sse))
    req := &adapter.ChatRequest{
        Model:    "test-model",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "book a room"}},
    }
    s.emitRolloutTapRecord(req, capw, time.Now(), "")
    recs := rollout608WaitRecords(t, path, 1)
    if recs[0].Messages[1].ToolCalls == nil ||
        !strings.Contains(string(recs[0].Messages[1].ToolCalls), "call_e") {
        t.Fatalf("e2e record missing tool trajectory: %+v", recs[0].Messages)
    }
}

func Test720_UpdateConfigHotReload(t *testing.T) {
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, []string{"master"})
    // Tenant removed by reload → decision flips without touching the sink.
    tap.UpdateConfig(rollout.TapConfig{Enabled: true, ContentMode: rollout.ContentModeRaw, Tenants: []string{"other"}, SinkPath: path})
    if tap.Allows("master", nil) {
        t.Fatal("removed tenant must be denied after reload")
    }
    if tap.Allows("other", nil) != true {
        t.Fatal("new tenant must be allowed after reload")
    }
    // Disable by reload → denies even for allowed tenants (sink intact).
    tap.UpdateConfig(rollout.TapConfig{Enabled: false, SinkPath: path})
    if tap.Enabled() || tap.Allows("other", nil) {
        t.Fatal("reload-disable must flip Enabled and Allows")
    }
    // Path change on re-enable re-opens the sink at the new location.
    newPath := path + ".2"
    tap.UpdateConfig(rollout.TapConfig{Enabled: true, ContentMode: rollout.ContentModeRaw, SinkPath: newPath})
    tap.Record(rollout.Record{RolloutID: "ro-z", Model: "m", Messages: []rollout.Message{{Role: "user", Content: "x"}}})
    deadline := time.Now().Add(3 * time.Second)
    for {
        raw, err := os.ReadFile(newPath)
        if err == nil && strings.Contains(string(raw), "ro-z") {
            break
        }
        if time.Now().After(deadline) {
            t.Fatalf("record never landed in reloaded sink %s", newPath)
        }
        time.Sleep(10 * time.Millisecond)
    }
}
