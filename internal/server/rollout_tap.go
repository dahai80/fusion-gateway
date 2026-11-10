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
type rolloutSSEChunk struct {
    Choices []struct {
        Delta struct {
            Content interface{} `json:"content"`
        } `json:"delta"`
        FinishReason *string `json:"finish_reason"`
    } `json:"choices"`
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
// from an SSE body of OpenAI chat chunks. Unparseable data lines are skipped
// (hybrid bodies, comments, keepalives).
func parseSSEBody(body []byte) (content string, finish string) {
    var sb strings.Builder
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
            if ch.FinishReason != nil && *ch.FinishReason != "" {
                finish = *ch.FinishReason
            }
        }
    }
    return sb.String(), finish
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

// parseNonStreamBody extracts content + finish_reason from a full OpenAI
// ChatResponse JSON body.
func parseNonStreamBody(body []byte) (content string, finish string) {
    var resp adapter.ChatResponse
    if err := json.Unmarshal(body, &resp); err != nil {
        return "", ""
    }
    for _, ch := range resp.Choices {
        content = rolloutMessageContent(ch.Message)
        if ch.FinishReason != "" {
            finish = ch.FinishReason
        }
    }
    return content, finish
}

// buildRolloutRecord assembles the schema v1 record from the request + the
// captured response. assistantMsg is the flattened assistant reply (already
// extracted by the caller from stream or non-stream bytes).
func buildRolloutRecord(req *adapter.ChatRequest, assistantMsg string, finish string, latency time.Duration, tenant string, source string) rollout.Record {
    msgs := make([]rollout.Message, 0, len(req.Messages)+1)
    for _, m := range req.Messages {
        msgs = append(msgs, rollout.Message{Role: m.Role, Content: rolloutMessageContent(m.Content)})
    }
    if assistantMsg != "" || finish != "" {
        msgs = append(msgs, rollout.Message{Role: "assistant", Content: assistantMsg})
    }
    return rollout.Record{
        TenantID:     tenant,
        Source:       source,
        Model:        req.Model,
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
    if strings.Contains(string(body), "data:") {
        assistantMsg, finish = parseSSEBody(body)
    } else {
        assistantMsg, finish = parseNonStreamBody(body)
    }
    rec := buildRolloutRecord(req, assistantMsg, finish, time.Since(start), capw.tenant, "chat_completions")
    s.rolloutTap.Record(rec)
}
