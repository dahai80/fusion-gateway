package server

import (
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
)

// bridgeThinkingSignature (#329): OpenAI-compatible backends (fusion-mlx
// thinking models) return reasoning in `reasoning_content` with no signature
// concept. Anthropic protocol expects a signature on thinking blocks; the
// bridge stamps this fixed placeholder. No local party verifies it (the
// gateway forwards history back to fusion-mlx in OpenAI format), and a
// non-empty value keeps the block keys present on the wire.
const bridgeThinkingSignature = "ZnVzaW9uLWdhdGV3YXktYnJpZGdlLXN5bnRoZXRpYy1zaWduYXR1cmU="

// #234: the /v1/messages fallback path (openai-compatible backends such as
// fusion-mlx — providers that do NOT implement MessagesProvider) forwards the
// request as an OpenAI ChatCompletion but previously returned the OpenAI
// response VERBATIM to the Anthropic-protocol client. Claude Code received
// HTTP 200 + a ChatCompletion body ("body is JSON but not a Message") and
// StreamNoEventsError on the stream path. This bridge converts the return
// trip: OpenAI ChatCompletion JSON -> Anthropic Message, and OpenAI chunk SSE
// -> Anthropic event SSE (message_start / content_block_* / message_delta /
// message_stop), preserving the inner handler's retry/watchdog/usage logic.

// bridgeToolCall mirrors the OpenAI tool-call shape used by both the
// non-stream message and the stream delta.
type bridgeToolCall struct {
    Index    int    `json:"index"`
    ID       string `json:"id"`
    Name     string `json:"name"`
    Args     string `json:"args"`
    Type     string `json:"type"`
    Function struct {
        Name      string `json:"name"`
        Arguments string `json:"arguments"`
    } `json:"function"`
}

type bridgeUsage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
}

type bridgeChatChoice struct {
    Message struct {
        Content          interface{}      `json:"content"`
        ReasoningContent string           `json:"reasoning_content"`
        ToolCalls        []bridgeToolCall `json:"tool_calls"`
    } `json:"message"`
    FinishReason string `json:"finish_reason"`
}

type bridgeChatCompletion struct {
    ID      string             `json:"id"`
    Model   string             `json:"model"`
    Choices []bridgeChatChoice `json:"choices"`
    Usage   *bridgeUsage       `json:"usage"`
    Error   *bridgeErrorBody   `json:"error"`
}

type bridgeErrorBody struct {
    Message string `json:"message"`
    Type    string `json:"type"`
}

// anthropicStopReasonFromOpenAI maps an OpenAI finish_reason onto the
// Anthropic stop_reason vocabulary.
func anthropicStopReasonFromOpenAI(fr string) string {
    switch fr {
    case "length":
        return "max_tokens"
    case "tool_calls", "function_call":
        return "tool_use"
    case "stop_sequence":
        return "stop_sequence"
    default:
        return "end_turn"
    }
}

func newBridgeMessageID() string {
    b := make([]byte, 12)
    _, _ = rand.Read(b)
    return "msg_" + hex.EncodeToString(b)
}

// openAICompletionToAnthropicMessage converts a non-stream OpenAI
// ChatCompletion body into an Anthropic Message body (issue #234 fix 1). A
// non-2xx or unparseable body is converted into the Anthropic error envelope
// so the client always receives protocol-valid JSON.
func openAICompletionToAnthropicMessage(raw []byte, status int) ([]byte, int) {
    var cc bridgeChatCompletion
    if err := json.Unmarshal(raw, &cc); err != nil || (cc.ID == "" && len(cc.Choices) == 0 && cc.Error == nil) {
        // Not a ChatCompletion — treat the body as an error payload.
        return anthropicErrorEnvelope(raw, status), status
    }
    if cc.Error != nil || status >= 400 {
        return anthropicErrorEnvelope(raw, status), status
    }
    resp := adapter.AnthropicResponse{
        ID:       newBridgeMessageID(),
        Type:     "message",
        Role:     "assistant",
        Model:    cc.Model,
        Content:  []adapter.AnthropicContentBlock{},
        StopReason: "end_turn",
    }
    if len(cc.Choices) > 0 {
        ch := cc.Choices[0]
        // #329: thinking models (fusion-mlx Qwen3 etc.) return the chain of
        // thought in reasoning_content. Surface it as a thinking block BEFORE
        // the text block so agent clients can see why a turn ended.
        if rc := ch.Message.ReasoningContent; rc != "" {
            resp.Content = append(resp.Content, adapter.AnthropicContentBlock{
                Type:      "thinking",
                Thinking:  rc,
                Signature: bridgeThinkingSignature,
            })
            slog.Debug("bridge: mapped reasoning_content to thinking block",
                "model", cc.Model, "thinking_len", len(rc))
        }
        if text, ok := ch.Message.Content.(string); ok && text != "" {
            resp.Content = append(resp.Content, adapter.AnthropicContentBlock{Type: "text", Text: text})
        }
        for _, tc := range ch.Message.ToolCalls {
            name := tc.Name
            if name == "" {
                name = tc.Function.Name
            }
            args := tc.Args
            if args == "" {
                args = tc.Function.Arguments
            }
            if strings.TrimSpace(args) == "" {
                args = "{}"
            }
            resp.Content = append(resp.Content, adapter.AnthropicContentBlock{
                Type:  "tool_use",
                ID:    tc.ID,
                Name:  name,
                Input: json.RawMessage(args),
            })
        }
        resp.StopReason = anthropicStopReasonFromOpenAI(ch.FinishReason)
    }
    if cc.Usage != nil {
        resp.Usage = adapter.AnthropicUsage{
            InputTokens:  cc.Usage.PromptTokens,
            OutputTokens: cc.Usage.CompletionTokens,
        }
    }
    // #363 F2: a 200 completion with zero content blocks (no reasoning, text,
    // or tool calls) is a failed upstream — never surface it as a valid empty
    // Anthropic message (Claude Code reads content:[] end_turn as a completed
    // turn and waits for the user to type "continue").
    if len(resp.Content) == 0 {
        slog.Warn("bridge: upstream completion produced empty content, converting to error envelope",
            "model", cc.Model, "status", status)
        return anthropicErrorEnvelope(
            []byte(`{"error":{"type":"api_error","message":"upstream produced no content (thinking, text, or tool_use); the backend request failed"}}`),
            http.StatusBadGateway), http.StatusBadGateway
    }
    out, err := json.Marshal(resp)
    if err != nil {
        return anthropicErrorEnvelope(raw, status), status
    }
    return out, status
}

// anthropicErrorEnvelope wraps an arbitrary upstream error body in the
// Anthropic error shape {"type":"error","error":{...}} (issue #234 fix 3).
// A parseable OpenAI/Anthropic-style {"error":{...}} body keeps its fields;
// anything else becomes a message string.
func anthropicErrorEnvelope(raw []byte, status int) []byte {
    var probe struct {
        Error json.RawMessage `json:"error"`
    }
    if err := json.Unmarshal(raw, &probe); err == nil && len(probe.Error) > 0 {
        var detail struct {
            Message string `json:"message"`
            Type    string `json:"type"`
        }
        msg, typ := string(probe.Error), "api_error"
        if json.Unmarshal(probe.Error, &detail) == nil && detail.Message != "" {
            msg = detail.Message
            if detail.Type != "" {
                typ = detail.Type
            }
        }
        out, _ := json.Marshal(map[string]any{
            "type":  "error",
            "error": map[string]string{"type": typ, "message": msg},
        })
        return out
    }
    // mlx-style {"detail": ...} or plain text.
    out, _ := json.Marshal(map[string]any{
        "type":  "error",
        "error": map[string]string{"type": "api_error", "message": fmt.Sprintf("upstream status %d: %s", status, truncateBridgeMessage(string(raw)))},
    })
    return out
}

func truncateBridgeMessage(s string) string {
    if len(s) > 512 {
        return s[:512]
    }
    return s
}

// nonStreamBridgeWriter captures what the inner OpenAI-format handler wrote
// (status + body) so the caller can convert it before it reaches the client.
// Nothing is forwarded during the handler run.
type nonStreamBridgeWriter struct {
    header http.Header
    status int
    body   []byte
}

func newNonStreamBridgeWriter() *nonStreamBridgeWriter {
    return &nonStreamBridgeWriter{header: make(http.Header), status: http.StatusOK}
}

func (b *nonStreamBridgeWriter) Header() http.Header { return b.header }

func (b *nonStreamBridgeWriter) WriteHeader(code int) {
    if b.status == http.StatusOK {
        b.status = code
    }
}

func (b *nonStreamBridgeWriter) Write(p []byte) (int, error) {
    b.body = append(b.body, p...)
    return len(p), nil
}

// finalize converts the captured OpenAI body and writes the Anthropic
// protocol response to the real writer.
func (b *nonStreamBridgeWriter) finalize(w http.ResponseWriter) {
    for k, vs := range b.header {
        for _, v := range vs {
            w.Header().Add(k, v)
        }
    }
    out, status := openAICompletionToAnthropicMessage(b.body, b.status)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _, _ = w.Write(out)
}

// anthropicSSEBridge is a ResponseWriter interposer that converts OpenAI
// chunk SSE frames into Anthropic event SSE frames on the fly (issue #234
// fix 2). The inner handleStreamChat keeps its full logic (retry, watchdog,
// usage chunk, [DONE], metrics); only the bytes crossing the wire change.
type anthropicSSEBridge struct {
    http.ResponseWriter
    status     int
    headerSent bool
    pending    []byte
    msgID      string
    model      string
    nextIndex  int
    thinkOpen  bool
    thinkIdx   int
    textOpen   bool
    textIdx    int
    toolBlocks map[int]int // openai tool call index -> anthropic block index
    openBlocks map[int]bool
    stopReason string
    inTok      int
    outTok     int
    stopped    bool
    // #363 F2: errEmitted tracks whether an error event was forwarded (the
    // stream is then already terminal — no empty message may follow); sawContent
    // tracks whether any real content block (thinking/text/tool_use) was ever
    // emitted. A stream ending with neither is a failed upstream, not a
    // "completed empty turn" — Claude Code treats content:[] end_turn as a
    // finished turn and waits for the user to type "continue".
    errEmitted bool
    sawContent bool
}

func newAnthropicSSEBridge(w http.ResponseWriter) *anthropicSSEBridge {
    return &anthropicSSEBridge{
        ResponseWriter: w,
        status:         http.StatusOK,
        thinkOpen:      false,
        thinkIdx:       -1,
        textOpen:       false,
        textIdx:        -1,
        toolBlocks:     make(map[int]int),
        openBlocks:     make(map[int]bool),
    }
}

func (b *anthropicSSEBridge) Header() http.Header { return b.ResponseWriter.Header() }

// WriteHeader defers: the pre-stream JSON error path (writeChatFailedError)
// sets a non-200 status that Finalize must convert into the Anthropic error
// envelope, while the SSE path simply replays the recorded status on first
// frame write (same lazy-header semantics as net/http).
func (b *anthropicSSEBridge) WriteHeader(code int) {
    b.status = code
}

func (b *anthropicSSEBridge) Write(p []byte) (int, error) {
    b.pending = append(b.pending, p...)
    for {
        idx := indexDoubleNewline(b.pending)
        if idx < 0 {
            break
        }
        frame := string(b.pending[:idx])
        b.pending = b.pending[idx+2:]
        b.handleFrame(frame)
    }
    return len(p), nil
}

func (b *anthropicSSEBridge) Flush() {
    if f, ok := b.ResponseWriter.(http.Flusher); ok {
        f.Flush()
    }
}

// indexDoubleNewline finds "\n\n" (SSE frame separator) in b.
func indexDoubleNewline(b []byte) int {
    for i := 0; i+1 < len(b); i++ {
        if b[i] == '\n' && b[i+1] == '\n' {
            return i
        }
    }
    return -1
}

// handleFrame converts one SSE frame. data: [DONE] finalizes the message.
// data: {chunk} is parsed as an OpenAI chunk and re-emitted as Anthropic
// events; comment/keepalive frames pass through untouched.
func (b *anthropicSSEBridge) handleFrame(frame string) {
    var dataLines []string
    for _, line := range strings.Split(frame, "\n") {
        if strings.HasPrefix(line, "data:") {
            dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
        }
    }
    if len(dataLines) == 0 {
        // Comment/keepalive or unknown field frame — forward verbatim.
        b.forward(frame)
        return
    }
    data := strings.Join(dataLines, "\n")
    if strings.TrimSpace(data) == "[DONE]" {
        b.finalizeMessage()
        return
    }
    var cc struct {
        ID      string `json:"id"`
        Model   string `json:"model"`
        Choices []struct {
            Delta struct {
                Content          interface{}      `json:"content"`
                ReasoningContent string           `json:"reasoning_content"`
                ToolCalls        []bridgeToolCall `json:"tool_calls"`
            } `json:"delta"`
            FinishReason *string `json:"finish_reason"`
        } `json:"choices"`
        Usage *bridgeUsage     `json:"usage"`
        Error *bridgeErrorBody `json:"error"`
    }
    if err := json.Unmarshal([]byte(data), &cc); err != nil {
        // Not an OpenAI chunk — forward verbatim (client may tolerate).
        b.forward(frame)
        return
    }
    // #259: JSON that parses but carries no OpenAI-chunk semantics (ID, choices,
    // usage, error) is not a chunk either — e.g. the native ping event the
    // keepalive branch emits for Anthropic downstreams. Without this the bridge
    // swallowed its own pings: unmarshal succeeded, nothing emitted, nothing
    // forwarded. Forward verbatim.
    if cc.ID == "" && len(cc.Choices) == 0 && cc.Usage == nil && cc.Error == nil {
        b.forward(frame)
        return
    }
    if cc.Error != nil {
        b.emitError(cc.Error)
        return
    }
    b.ensureMessageStart(cc.ID, cc.Model)
    if cc.Usage != nil {
        b.inTok = cc.Usage.PromptTokens
        b.outTok = cc.Usage.CompletionTokens
    }
    emitted := false
    for _, ch := range cc.Choices {
        if ch.Delta.ReasoningContent != "" {
            b.emitThinkingDelta(ch.Delta.ReasoningContent)
            emitted = true
        }
        if text, ok := ch.Delta.Content.(string); ok && text != "" {
            b.emitTextDelta(text)
            emitted = true
        }
        for _, tc := range ch.Delta.ToolCalls {
            b.emitToolDelta(tc)
            emitted = true
        }
        if ch.FinishReason != nil && *ch.FinishReason != "" {
            b.stopReason = anthropicStopReasonFromOpenAI(*ch.FinishReason)
            b.closeOpenBlocks()
            emitted = true
        }
    }
    // #259: fusion-mlx (PR #1295) emits prefill heartbeats as legal
    // empty-delta chunks. The bridge swallowed them, so a 100-200s prefill
    // produced zero Anthropic events after message_start and Claude Code's
    // ~60s TTFB timer cancelled the stream. Forward such a chunk as a native
    // ping event (the aggregate loop's contract). The synthetic usage chunk
    // (include_usage) carries zero choices — it is data, not a heartbeat,
    // and must not ping.
    if !emitted && len(cc.Choices) > 0 {
        b.forward("event: ping\ndata: {\"type\":\"ping\"}\n\n")
    }
}

// ensureMessageStart emits message_start exactly once, immediately on the
// first upstream chunk (Claude Code times out waiting for the first event —
// issue #234 timeline: minutes of prefill silence).
func (b *anthropicSSEBridge) ensureMessageStart(id, model string) {
    if b.msgID != "" {
        return
    }
    b.msgID = newBridgeMessageID()
    if id != "" {
        b.msgID = "msg_" + strings.TrimPrefix(id, "chatcmpl-")
    }
    b.model = model
    msg := adapter.AnthropicResponse{
        ID:       b.msgID,
        Type:     "message",
        Role:     "assistant",
        Model:    b.model,
        Content:  []adapter.AnthropicContentBlock{},
        StopReason: "end_turn",
    }
    raw, _ := json.Marshal(adapter.AnthropicStreamEvent{Type: "message_start", Message: &msg})
    b.forward("event: message_start\ndata: " + string(raw) + "\n\n")
}

// emitThinkingDelta (#329): reasoning_content deltas open a thinking block
// (always the first block — reasoning streams before content) and replay as
// thinking_delta events.
func (b *anthropicSSEBridge) emitThinkingDelta(text string) {
    b.sawContent = true
    if !b.thinkOpen {
        b.thinkOpen = true
        b.thinkIdx = b.nextIndex
        b.nextIndex++
        b.openBlocks[b.thinkIdx] = true
        slog.Debug("bridge: thinking block opened", "model", b.model, "index", b.thinkIdx)
        raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
            Type: "content_block_start", Index: b.thinkIdx,
            ContentBlock: &adapter.AnthropicContentBlock{Type: "thinking", Thinking: ""},
        })
        b.forward("event: content_block_start\ndata: " + string(raw) + "\n\n")
    }
    delta, _ := json.Marshal(map[string]any{"type": "thinking_delta", "thinking": text})
    raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
        Type: "content_block_delta", Index: b.thinkIdx,
        Delta: delta,
    })
    b.forward("event: content_block_delta\ndata: " + string(raw) + "\n\n")
}

// closeThinkingBlock seals the thinking block with a signature_delta before
// its content_block_stop — the Anthropic event shape clients expect. Called
// lazily when the first text/tool delta arrives and at stream end.
func (b *anthropicSSEBridge) closeThinkingBlock() {
    if !b.thinkOpen {
        return
    }
    b.thinkOpen = false
    delete(b.openBlocks, b.thinkIdx)
    sig, _ := json.Marshal(map[string]any{"type": "signature_delta", "signature": bridgeThinkingSignature})
    raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
        Type: "content_block_delta", Index: b.thinkIdx,
        Delta: sig,
    })
    b.forward("event: content_block_delta\ndata: " + string(raw) + "\n\n")
    stop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": b.thinkIdx})
    b.forward("event: content_block_stop\ndata: " + string(stop) + "\n\n")
}

func (b *anthropicSSEBridge) emitTextDelta(text string) {
    b.sawContent = true
    b.closeThinkingBlock()
    if !b.textOpen {
        b.textOpen = true
        b.textIdx = b.nextIndex
        b.nextIndex++
        b.openBlocks[b.textIdx] = true
        raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
            Type: "content_block_start", Index: b.textIdx,
            ContentBlock: &adapter.AnthropicContentBlock{Type: "text", Text: ""},
        })
        b.forward("event: content_block_start\ndata: " + string(raw) + "\n\n")
    }
    delta, _ := json.Marshal(map[string]any{"type": "text_delta", "text": text})
    raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
        Type: "content_block_delta", Index: b.textIdx,
        Delta: delta,
    })
    b.forward("event: content_block_delta\ndata: " + string(raw) + "\n\n")
}

func (b *anthropicSSEBridge) emitToolDelta(tc bridgeToolCall) {
    b.sawContent = true
    b.closeThinkingBlock()
    blockIdx, seen := b.toolBlocks[tc.Index]
    if !seen {
        blockIdx = b.nextIndex
        b.nextIndex++
        b.toolBlocks[tc.Index] = blockIdx
        b.openBlocks[blockIdx] = true
        name := tc.Name
        if name == "" {
            name = tc.Function.Name
        }
        raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
            Type: "content_block_start", Index: blockIdx,
            ContentBlock: &adapter.AnthropicContentBlock{Type: "tool_use", ID: tc.ID, Name: name, Input: json.RawMessage("{}")},
        })
        b.forward("event: content_block_start\ndata: " + string(raw) + "\n\n")
    }
    args := tc.Args
    if args == "" {
        args = tc.Function.Arguments
    }
    if args != "" {
        delta, _ := json.Marshal(map[string]any{"type": "input_json_delta", "partial_json": args})
        raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
            Type: "content_block_delta", Index: blockIdx, Delta: delta,
        })
        b.forward("event: content_block_delta\ndata: " + string(raw) + "\n\n")
    }
}

// closeOpenBlocks emits content_block_stop for every open block, ascending.
func (b *anthropicSSEBridge) closeOpenBlocks() {
    // Thinking block closes with its signature_delta first (#329).
    b.closeThinkingBlock()
    if len(b.openBlocks) == 0 {
        return
    }
    for idx := 0; idx < b.nextIndex; idx++ {
        if !b.openBlocks[idx] {
            continue
        }
        delete(b.openBlocks, idx)
        raw, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": idx})
        b.forward("event: content_block_stop\ndata: " + string(raw) + "\n\n")
    }
}

// finalizeMessage emits the message_delta + message_stop terminal pair once.
// Called on data: [DONE]; Finalize() calls it again defensively for a
// handler that ended without [DONE] (synth terminal, issue #71/#75 pattern).
// #363 F2: a stream that produced no content block (no thinking, text, or
// tool_use deltas) is a failed upstream, not a completed empty turn — the
// old "minimal empty message" (content:[] + end_turn + message_stop, 200)
// made Claude Code treat the turn as finished and stall waiting for the
// user to type "continue". Such streams now surface an error event
// instead. A stream that already emitted an error event is terminal too —
// never append an empty message after it.
func (b *anthropicSSEBridge) finalizeMessage() {
    if b.stopped {
        return
    }
    b.stopped = true
    if b.errEmitted {
        slog.Warn("anthropic bridge: upstream error event already terminal, skipping empty message terminal")
        return
    }
    if !b.sawContent {
        slog.Warn("anthropic bridge: upstream produced no content, emitting error event instead of empty message",
            "chunks_seen", b.nextIndex)
        b.emitError(&bridgeErrorBody{
            Type:    "api_error",
            Message: "upstream stream closed without producing any content (thinking, text, or tool_use); the backend request failed",
        })
        return
    }
    b.closeOpenBlocks()
    outTok := b.outTok
    stop := b.stopReason
    if stop == "" {
        stop = "end_turn"
    }
    delta, _ := json.Marshal(map[string]any{"stop_reason": stop, "stop_sequence": nil})
    raw, _ := json.Marshal(adapter.AnthropicStreamEvent{
        Type:         "message_delta",
        Delta:        delta,
        StopReason:   stop,
        Usage:        &adapter.AnthropicUsage{InputTokens: b.inTok, OutputTokens: outTok},
        StopSequence: nil,
    })
    b.forward("event: message_delta\ndata: " + string(raw) + "\n\n")
    raw, _ = json.Marshal(adapter.AnthropicStreamEvent{Type: "message_stop"})
    b.forward("event: message_stop\ndata: " + string(raw) + "\n\n")
}

// UsageTotals exposes the upstream usage-chunk counts the bridge observed
// while converting frames (#351 follow-up): the caller stamps them onto the
// request log after Finalize so bridge-path rows carry real prompt/completion
// counts instead of the tokenizer estimate.
func (b *anthropicSSEBridge) UsageTotals() (inputTokens, outputTokens int) {
    return b.inTok, b.outTok
}

func (b *anthropicSSEBridge) emitError(e *bridgeErrorBody) {
    b.errEmitted = true
    typ := e.Type
    if typ == "" {
        typ = "api_error"
    }
    raw, _ := json.Marshal(map[string]any{
        "type":  "error",
        "error": map[string]string{"type": typ, "message": e.Message},
    })
    b.forward("event: error\ndata: " + string(raw) + "\n\n")
}

func (b *anthropicSSEBridge) forward(frame string) {
    if !b.headerSent {
        b.headerSent = true
        b.ResponseWriter.WriteHeader(b.status)
    }
    _, _ = b.ResponseWriter.Write([]byte(frame))
    b.Flush()
}

// Finalize closes the stream. Two terminal shapes:
//   - JSON-error path: the inner handler failed before streaming (non-200,
//     headers not sent) — convert the captured OpenAI error body into the
//     Anthropic error envelope.
//   - SSE path: flush any trailing partial frame, then synthesize the
//     message_delta + message_stop terminal when [DONE] never arrived
//     (watchdog/abort), so the client always receives a protocol-valid end.
func (b *anthropicSSEBridge) Finalize() {
    if !b.headerSent {
        b.headerSent = true
        b.ResponseWriter.WriteHeader(b.status)
        isJSONError := b.status >= 400 || len(b.pending) > 0
        if isJSONError {
            out := anthropicErrorEnvelope(b.pending, b.status)
            b.ResponseWriter.Header().Set("Content-Type", "application/json")
            _, _ = b.ResponseWriter.Write(out)
            b.pending = nil
            return
        }
        // 200 with zero bytes: an SSE stream that produced no chunks — fall
        // through to the minimal-message terminal below.
    }
    if len(b.pending) > 0 {
        frame := string(b.pending)
        b.pending = nil
        b.handleFrame(frame)
    }
    b.finalizeMessage()
}
