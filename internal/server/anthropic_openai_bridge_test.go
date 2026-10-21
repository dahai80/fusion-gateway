package server

import (
    "encoding/json"
    "net/http/httptest"
    "strings"
    "testing"
)

// TestOpenAICompletionToMessage_Text covers the #234 non-stream fix: a
// ChatCompletion body becomes an Anthropic Message (type/role/content/usage).
func TestOpenAICompletionToMessage_Text(t *testing.T) {
    raw := []byte(`{"id":"chatcmpl-abc","object":"chat.completion","model":"qwen3-4b",
        "choices":[{"index":0,"message":{"role":"assistant","content":"hello world"},"finish_reason":"stop"}],
        "usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`)
    out, status := openAICompletionToAnthropicMessage(raw, 200)
    if status != 200 {
        t.Fatalf("status = %d", status)
    }
    var msg struct {
        Type       string   `json:"type"`
        Role       string   `json:"role"`
        Model      string   `json:"model"`
        StopReason string   `json:"stop_reason"`
        Content    []struct {
            Type string `json:"type"`
            Text string `json:"text"`
        } `json:"content"`
        Usage struct {
            InputTokens  int `json:"input_tokens"`
            OutputTokens int `json:"output_tokens"`
        } `json:"usage"`
    }
    if err := json.Unmarshal(out, &msg); err != nil {
        t.Fatalf("unmarshal: %v (%s)", err, out)
    }
    if msg.Type != "message" || msg.Role != "assistant" || msg.Model != "qwen3-4b" {
        t.Fatalf("envelope = %+v", msg)
    }
    if msg.StopReason != "end_turn" {
        t.Fatalf("stop_reason = %q", msg.StopReason)
    }
    if len(msg.Content) != 1 || msg.Content[0].Type != "text" || msg.Content[0].Text != "hello world" {
        t.Fatalf("content = %+v", msg.Content)
    }
    if msg.Usage.InputTokens != 12 || msg.Usage.OutputTokens != 7 {
        t.Fatalf("usage = %+v", msg.Usage)
    }
}

// TestOpenAICompletionToMessage_ToolCalls: tool_calls become tool_use blocks
// and finish_reason tool_calls maps to stop_reason tool_use.
func TestOpenAICompletionToMessage_ToolCalls(t *testing.T) {
    raw := []byte(`{"id":"chatcmpl-t","model":"m",
        "choices":[{"index":0,"message":{"role":"assistant","content":null,
            "tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},
            "finish_reason":"tool_calls"}]}`)
    out, _ := openAICompletionToAnthropicMessage(raw, 200)
    var msg struct {
        StopReason string `json:"stop_reason"`
        Content    []struct {
            Type  string          `json:"type"`
            ID    string          `json:"id"`
            Name  string          `json:"name"`
            Input json.RawMessage `json:"input"`
        } `json:"content"`
    }
    if err := json.Unmarshal(out, &msg); err != nil {
        t.Fatalf("unmarshal: %v (%s)", err, out)
    }
    if msg.StopReason != "tool_use" {
        t.Fatalf("stop_reason = %q", msg.StopReason)
    }
    if len(msg.Content) != 1 || msg.Content[0].Type != "tool_use" || msg.Content[0].Name != "get_weather" {
        t.Fatalf("content = %+v", msg.Content)
    }
    if string(msg.Content[0].Input) != `{"city":"sf"}` {
        t.Fatalf("input = %s", msg.Content[0].Input)
    }
}

// TestOpenAICompletionToMessage_StopReasonMap: length → max_tokens.
func TestOpenAICompletionToMessage_StopReasonMap(t *testing.T) {
    raw := []byte(`{"id":"x","model":"m","choices":[{"message":{"content":"hi"},"finish_reason":"length"}]}`)
    out, _ := openAICompletionToAnthropicMessage(raw, 200)
    if !strings.Contains(string(out), `"stop_reason":"max_tokens"`) {
        t.Fatalf("missing max_tokens: %s", out)
    }
}

// TestOpenAICompletionToMessage_ErrorBodies (#234 fix 3): mlx {"detail":...}
// and OpenAI {"error":{...}} both become the Anthropic error envelope.
func TestOpenAICompletionToMessage_ErrorBodies(t *testing.T) {
    out, status := openAICompletionToAnthropicMessage([]byte(`{"detail":"model not loaded"}`), 503)
    if status != 503 {
        t.Fatalf("status = %d", status)
    }
    if !strings.Contains(string(out), `"type":"error"`) || !strings.Contains(string(out), "model not loaded") {
        t.Fatalf("envelope = %s", out)
    }
    out2, status2 := openAICompletionToAnthropicMessage([]byte(`{"error":{"message":"boom","type":"server_error"}}`), 500)
    if status2 != 500 || !strings.Contains(string(out2), `"type":"server_error"`) || !strings.Contains(string(out2), "boom") {
        t.Fatalf("openai error conversion = %d %s", status2, out2)
    }
}

// bridgeFrames splits raw SSE output into (event, data) pairs for assertions.
func bridgeFrames(t *testing.T, raw string) [][2]string {
    t.Helper()
    var frames [][2]string
    for _, block := range strings.Split(raw, "\n\n") {
        if strings.TrimSpace(block) == "" {
            continue
        }
        var ev, data string
        for _, line := range strings.Split(block, "\n") {
            if strings.HasPrefix(line, "event: ") {
                ev = strings.TrimPrefix(line, "event: ")
            }
            if strings.HasPrefix(line, "data: ") {
                data = strings.TrimPrefix(line, "data: ")
            }
        }
        frames = append(frames, [2]string{ev, data})
    }
    return frames
}

// TestSSEBridge_TextStream: chunk SSE → full Anthropic event sequence with
// message_start first, one text block lifecycle, terminal pair.
func TestSSEBridge_TextStream(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"model\":\"qwen\",\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"model\":\"qwen\",\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"model\":\"qwen\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"model\":\"qwen\",\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\n"))
    b.Write([]byte("data: [DONE]\n\n"))

    frames := bridgeFrames(t, inner.Body.String())
    if len(frames) == 0 || frames[0][0] != "message_start" {
        t.Fatalf("first event = %v, want message_start", frames)
    }
    var seq []string
    for _, f := range frames {
        seq = append(seq, f[0])
    }
    want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
    if strings.Join(seq, ",") != strings.Join(want, ",") {
        t.Fatalf("event seq = %v", seq)
    }
    // message_delta carries stop_reason + usage.
    lastDelta := frames[len(frames)-2][1]
    if !strings.Contains(lastDelta, `"stop_reason":"end_turn"`) || !strings.Contains(lastDelta, `"output_tokens":2`) {
        t.Fatalf("message_delta = %s", lastDelta)
    }
    // index present even 0 (issue #46 regression through the bridge).
    if !strings.Contains(frames[1][1], `"index":0`) {
        t.Fatalf("content_block_start missing index 0: %s", frames[1][1])
    }
}

// TestSSEBridge_ToolStream: openai tool index maps to a tool_use block fed by
// input_json_delta frames.
func TestSSEBridge_ToolStream(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"function\":{\"name\":\"run\",\"arguments\":\"\"}}]}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"))
    b.Write([]byte("data: [DONE]\n\n"))

    raw := inner.Body.String()
    for _, want := range []string{
        `"type":"tool_use"`, `"name":"run"`, `"id":"call_9"`,
        `"type":"input_json_delta"`, `"partial_json":"{\"x\":"`,
        `"stop_reason":"tool_use"`, "event: message_stop",
    } {
        if !strings.Contains(raw, want) {
            t.Fatalf("missing %s in:\n%s", want, raw)
        }
    }
}

// TestSSEBridge_HeartbeatPing (#259): fusion-mlx prefill heartbeats arrive as
// legal empty-delta OpenAI chunks. The bridge must surface them as native
// ping events (CC's TTFB timer resets on events, not on comments), while the
// synthetic usage chunk (zero choices) stays silent.
func TestSSEBridge_HeartbeatPing(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n"))
    b.Write([]byte("data: [DONE]\n\n"))

    var seq []string
    for _, f := range bridgeFrames(t, inner.Body.String()) {
        seq = append(seq, f[0])
    }
    want := []string{
        "message_start",
        "ping", "ping",
        "content_block_start", "content_block_delta",
        "content_block_stop", "message_delta", "message_stop",
    }
    if strings.Join(seq, ",") != strings.Join(want, ",") {
        t.Fatalf("event seq = %v, want %v", seq, want)
    }
}

// TestSSEBridge_FinalizeSynth: a stream cut without [DONE] (watchdog/abort)
// still produces a protocol-valid terminal, and Finalize is idempotent.
func TestSSEBridge_FinalizeSynth(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"id\":\"c2\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
    b.Finalize()
    before := inner.Body.String()
    b.Finalize()

    frames := bridgeFrames(t, before)
    var seq []string
    for _, f := range frames {
        seq = append(seq, f[0])
    }
    want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
    if strings.Join(seq, ",") != strings.Join(want, ",") {
        t.Fatalf("synth seq = %v", seq)
    }
    if strings.Count(before, "event: message_stop") != 1 {
        t.Fatalf("duplicate message_stop after second Finalize")
    }
}

// TestSSEBridge_EmptyStream: zero upstream chunks still yields a minimal
// valid message so the client never hangs.
func TestSSEBridge_EmptyStream(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Finalize()
    raw := inner.Body.String()
    if !strings.Contains(raw, "event: message_start") || !strings.Contains(raw, "event: message_stop") {
        t.Fatalf("empty stream output = %s", raw)
    }
}

// TestSSEBridge_ErrorChunk: an in-band {"error":...} data frame converts to
// the Anthropic error event.
func TestSSEBridge_ErrorChunk(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"error\":{\"message\":\"ctx too long\",\"type\":\"invalid_request_error\"}}\n\n"))
    b.Finalize()
    raw := inner.Body.String()
    if !strings.Contains(raw, "event: error") || !strings.Contains(raw, `"type":"invalid_request_error"`) {
        t.Fatalf("error event missing: %s", raw)
    }
}

// TestSSEBridge_PreStreamJSONError: a pre-stream failure (writeChatFailedError
// path — non-200 status + OpenAI error JSON, no frames) converts into the
// Anthropic error envelope with the upstream status preserved.
func TestSSEBridge_PreStreamJSONError(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.WriteHeader(502)
    _, _ = b.Write([]byte(`{"error":{"message":"upstream down","type":"api_error"}}`))
    b.Finalize()
    if inner.Code != 502 {
        t.Fatalf("code = %d", inner.Code)
    }
    body := inner.Body.String()
    if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "upstream down") {
        t.Fatalf("body = %s", body)
    }
    if strings.Contains(body, "event:") {
        t.Fatalf("JSON error path must not emit SSE events: %s", body)
    }
}

// TestNonStreamBridgeWriter_CapturesAndConverts: capture writer holds the
// body, finalize writes converted JSON to the real writer.
func TestNonStreamBridgeWriter_CapturesAndConverts(t *testing.T) {
    real := httptest.NewRecorder()
    cap := newNonStreamBridgeWriter()
    cap.WriteHeader(200)
    _, _ = cap.Write([]byte(`{"id":"c","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
    cap.finalize(real)
    if real.Code != 200 {
        t.Fatalf("code = %d", real.Code)
    }
    if !strings.Contains(real.Body.String(), `"type":"message"`) || !strings.Contains(real.Body.String(), `"text":"ok"`) {
        t.Fatalf("body = %s", real.Body.String())
    }
}
