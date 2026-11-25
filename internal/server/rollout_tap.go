package server

// #608 rollout tap capture: a ResponseWriter wrapper that mirrors the chat
// response bytes (streaming SSE or non-stream JSON) into an in-memory
// accumulator, parsed once at handler end into a rollout.Record and handed
// to the rollout.Tap sink. Isolation contract: parsing happens off the hot
// write path's critical section (single append under cap), and every tap
// failure is swallowed — the proxied request never fails because of the tap.

import (
    "bytes"
    "encoding/json"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/rollout"
)

const rolloutCaptureDefaultMaxBytes = 512 << 10

// rolloutTapCapture wraps the handler writer, keeping a capped copy of the
// response body for tap parsing. When the cap is hit the copy stops growing
// (truncated flag set) and the real response keeps flowing untouched.
type rolloutTapCapture struct {
    http.ResponseWriter
    buf       bytes.Buffer
    max       int
    truncated bool
    // tenant is the resolved tenant id for the record (real gateway tenant,
    // falling back to the auth-key name; "anonymous" when neither exists).
    tenant string
}

func newRolloutTapCapture(w http.ResponseWriter, maxBytes int, tenant string) *rolloutTapCapture {
    if maxBytes <= 0 {
        maxBytes = rolloutCaptureDefaultMaxBytes
    }
    return &rolloutTapCapture{ResponseWriter: w, max: maxBytes, tenant: tenant}
}

// Write mirrors bytes into the capped buffer and forwards to the real
// writer. Mirror errors are impossible (bytes.Buffer never fails); the
// forwarding result is returned unchanged.
func (c *rolloutTapCapture) Write(p []byte) (int, error) {
    if !c.truncated {
        room := c.max - c.buf.Len()
        if room > 0 {
            if len(p) > room {
                c.buf.Write(p[:room])
                c.truncated = true
            } else {
                c.buf.Write(p)
            }
        } else {
            c.truncated = true
        }
    }
    return c.ResponseWriter.Write(p)
}

// rolloutSSEChunk is the minimal OpenAI stream chunk shape the tap parses.
// Delta.ToolCalls carries streamed tool-call fragments (#720): a call opens
// with id+name in one chunk, then function.arguments append across chunks
// keyed by Index — the same accumulation the client-side aggregator does.
type rolloutSSEChunk struct {
    Choices []struct {
        Delta struct {
            Content   interface{}        `json:"content"`
            ToolCalls []rolloutToolFragment `json:"tool_calls"`
        } `json:"delta"`
        FinishReason *string `json:"finish_reason"`
    } `json:"choices"`
}

// rolloutToolFragment is one streamed tool_call piece (OpenAI shape).
type rolloutToolFragment struct {
    Index    int    `json:"index"`
    ID       string `json:"id"`
    Type     string `json:"type"`
    Function struct {
        Name      string `json:"name"`
        Arguments string `json:"arguments"`
    } `json:"function"`
}

// rolloutToolAccum merges streamed tool fragments by Index into complete
// OpenAI tool_calls. Arguments concatenate (providers stream them in
// slices); id/name/type take the first non-empty value seen.
type rolloutToolAccum struct {
    order []int
    parts map[int]*rolloutToolFragment
}

func newRolloutToolAccum() *rolloutToolAccum {
    return &rolloutToolAccum{parts: make(map[int]*rolloutToolFragment)}
}

func (a *rolloutToolAccum) Add(f rolloutToolFragment) {
    p, ok := a.parts[f.Index]
    if !ok {
        cp := f
        a.parts[f.Index] = &cp
        a.order = append(a.order, f.Index)
        return
    }
    if p.ID == "" {
        p.ID = f.ID
    }
    if p.Type == "" {
        p.Type = f.Type
    }
    if p.Function.Name == "" {
        p.Function.Name = f.Function.Name
    }
    p.Function.Arguments += f.Function.Arguments
}

// Emit renders the accumulated calls as a JSON array (nil when empty).
func (a *rolloutToolAccum) Emit() json.RawMessage {
    if len(a.order) == 0 {
        return nil
    }
    calls := make([]rolloutToolFragment, 0, len(a.order))
    for _, i := range a.order {
        calls = append(calls, *a.parts[i])
    }
    raw, err := json.Marshal(calls)
    if err != nil {
        return nil
    }
    return raw
}

// rolloutContentString flattens an interface{} content field (string, or a
// JSON shape) into a string. Non-string content marshals to compact JSON so
// nothing is silently dropped.
func rolloutContentString(v interface{}) string {
    switch t := v.(type) {
    case nil:
        return ""
    case string:
        return t
    default:
        raw, err := json.Marshal(t)
        if err != nil {
            return ""
        }
        return string(raw)
    }
}

// parseSSEBody extracts accumulated delta content + the final finish_reason
// + merged tool_calls (#720) from an SSE body of OpenAI chat chunks.
// Unparseable data lines are skipped (hybrid bodies, comments, keepalives).
func parseSSEBody(body []byte) (content string, finish string, toolCalls json.RawMessage) {
    var sb strings.Builder
    acc := newRolloutToolAccum()
    for _, line := range strings.Split(string(body), "\n") {
        line = strings.TrimSpace(line)
        if !strings.HasPrefix(line, "data:") {
            continue
        }
        payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
        if payload == "" || payload == "[DONE]" {
            continue
        }
        var chunk rolloutSSEChunk
        if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
            continue
        }
        for _, ch := range chunk.Choices {
            sb.WriteString(rolloutContentString(ch.Delta.Content))
            for _, f := range ch.Delta.ToolCalls {
                acc.Add(f)
            }
            if ch.FinishReason != nil && *ch.FinishReason != "" {
                finish = *ch.FinishReason
            }
        }
    }
    return sb.String(), finish, acc.Emit()
}

// rolloutMessageContent extracts the text of a message object (ChatRequest
// message content or a non-stream ChatChoice.Message). A bare string passes
// through; an object shape (e.g. {"role":..., "content":...} from the
// non-stream message field) contributes its "content" value only — the
// role/metadata envelope is not conversation text.
func rolloutMessageContent(v interface{}) string {
    if s, ok := v.(string); ok {
        return s
    }
    if m, ok := v.(map[string]interface{}); ok {
        if c, exists := m["content"]; exists {
            return rolloutContentString(c)
        }
    }
    return rolloutContentString(v)
}

// parseNonStreamBody extracts content + finish_reason + tool_calls from a
// full OpenAI ChatResponse JSON body.
func parseNonStreamBody(body []byte) (content string, finish string, toolCalls json.RawMessage) {
    var resp adapter.ChatResponse
    if err := json.Unmarshal(body, &resp); err != nil {
        return "", "", nil
    }
    for _, ch := range resp.Choices {
        content = rolloutMessageContent(ch.Message)
        if ch.FinishReason != "" {
            finish = ch.FinishReason
        }
        if tc, ok := ch.Message.(map[string]interface{}); ok {
            if raw, err := json.Marshal(tc["tool_calls"]); err == nil && string(raw) != "null" {
                toolCalls = raw
            }
        }
    }
    return content, finish, toolCalls
}

// buildRolloutRecord assembles the schema v1 record from the request + the
// captured response. assistantMsg is the flattened assistant reply (already
// extracted by the caller from stream or non-stream bytes); assistantTools
// is the merged tool_calls array (#720). Request tool trajectory survives:
// assistant tool_calls + role=tool results keep their tool_call_id.
func buildRolloutRecord(req *adapter.ChatRequest, assistantMsg string, assistantTools json.RawMessage, finish string, latency time.Duration, tenant string, source string) rollout.Record {
    msgs := make([]rollout.Message, 0, len(req.Messages)+1)
    for _, m := range req.Messages {
        msg := rollout.Message{Role: m.Role, Content: rolloutMessageContent(m.Content), ToolCallID: m.ToolCallID}
        if m.ToolCalls != nil {
            if raw, err := json.Marshal(m.ToolCalls); err == nil && string(raw) != "null" {
                msg.ToolCalls = raw
            }
        }
        msgs = append(msgs, msg)
    }
    if assistantMsg != "" || finish != "" || len(assistantTools) > 0 {
        msgs = append(msgs, rollout.Message{Role: "assistant", Content: assistantMsg, ToolCalls: assistantTools})
    }
    var tools json.RawMessage
    if req.Tools != nil {
        if raw, err := json.Marshal(req.Tools); err == nil && string(raw) != "null" {
            tools = raw
        }
    }
    return rollout.Record{
        TenantID:     tenant,
        Source:       source,
        Model:        req.Model,
        Tools:        tools,
        Messages:     msgs,
        FinishReason: finish,
        LatencyMS:    latency.Milliseconds(),
    }
}

// emitRolloutTapRecord records one rollout for a finished chat request.
// Called at the end of handleChatCompletions only when the capture writer
// was installed (the tap already said yes), so the only decisions left are
// parse-shape detection and empty-body bail.
func (s *Server) emitRolloutTapRecord(req *adapter.ChatRequest, capw *rolloutTapCapture, start time.Time) {
    if s.rolloutTap == nil || capw == nil {
        return
    }
    body := capw.buf.Bytes()
    if len(body) == 0 {
        slog.Warn("rollout tap captured empty response body (#608)", "model", req.Model)
        return
    }
    var assistantMsg, finish string
    var assistantTools json.RawMessage
    if strings.Contains(string(body), "data:") {
        assistantMsg, finish, assistantTools = parseSSEBody(body)
    } else {
        assistantMsg, finish, assistantTools = parseNonStreamBody(body)
    }
    rec := buildRolloutRecord(req, assistantMsg, assistantTools, finish, time.Since(start), capw.tenant, "chat_completions")
    s.rolloutTap.Record(rec)
}
