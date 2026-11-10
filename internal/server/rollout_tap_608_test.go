package server

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/rollout"
)

// Issue #608: rollout tap — mirror production chat traffic into a JSONL sink
// for fusion-trainer RL datasets. Isolation contract: the tap never blocks,
// fails, or slows the proxied request.

func rollout608Tap(t *testing.T, mode string, tenants []string) (*rollout.Tap, string) {
    t.Helper()
    path := filepath.Join(t.TempDir(), "rollouts.jsonl")
    tap := rollout.NewTap(rollout.TapConfig{
        Enabled:     true,
        ContentMode: mode,
        Tenants:     tenants,
        SinkPath:    path,
    })
    return tap, path
}

func rollout608WaitRecords(t *testing.T, path string, want int) []rollout.Record {
    t.Helper()
    deadline := time.Now().Add(3 * time.Second)
    for time.Now().Before(deadline) {
        raw, err := os.ReadFile(path)
        if err == nil {
            var recs []rollout.Record
            for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
                if line == "" {
                    continue
                }
                var rec rollout.Record
                if err := json.Unmarshal([]byte(line), &rec); err != nil {
                    t.Fatalf("sink line is not valid schema-v1 json: %v", err)
                }
                recs = append(recs, rec)
            }
            if len(recs) >= want {
                return recs
            }
        }
        time.Sleep(10 * time.Millisecond)
    }
    t.Fatalf("sink %s never reached %d records", path, want)
    return nil
}

func rollout608NonStreamReq(t *testing.T, s *Server, tenantID string, keyCfg *config.AuthKeyConfig) *httptest.ResponseRecorder {
    t.Helper()
    body := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"hello world"}],"stream":false}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    ctx := config.WithSnapshot(req.Context(), s.cfg)
    if tenantID != "" {
        ctx = adapter.WithTenant(ctx, tenantID)
    }
    if keyCfg != nil {
        ctx = middleware.ContextWithPrincipal(ctx, &middleware.Principal{KeyConfig: keyCfg})
    }
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)
    return rec
}

func rollout608ChatResponse() *adapter.ChatResponse {
    return &adapter.ChatResponse{
        ID:      "chatcmpl-608",
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   "qwen2.5-7b",
        Choices: []adapter.ChatChoice{{
            Index:        0,
            Message:      map[string]interface{}{"role": "assistant", "content": "hi there"},
            FinishReason: "stop",
        }},
    }
}

func Test608_NonStreamRecordLandsInSink(t *testing.T) {
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, nil)
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, chatResp: rollout608ChatResponse()})
    s.rolloutTap = tap

    rec := rollout608NonStreamReq(t, s, "team-a", nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("chat code = %d body=%s", rec.Code, rec.Body.String())
    }
    recs := rollout608WaitRecords(t, path, 1)
    r := recs[0]
    if r.SchemaVersion != 1 {
        t.Errorf("schema_version = %d", r.SchemaVersion)
    }
    if !strings.HasPrefix(r.RolloutID, "ro-") {
        t.Errorf("rollout_id = %q", r.RolloutID)
    }
    if r.TenantID != "team-a" {
        t.Errorf("tenant_id = %q", r.TenantID)
    }
    if r.Model != "qwen2.5-7b" {
        t.Errorf("model = %q", r.Model)
    }
    if r.FinishReason != "stop" {
        t.Errorf("finish_reason = %q", r.FinishReason)
    }
    if len(r.Messages) != 2 || r.Messages[0].Role != "user" || r.Messages[0].Content != "hello world" || r.Messages[1].Role != "assistant" || r.Messages[1].Content != "hi there" {
        t.Errorf("messages = %+v", r.Messages)
    }
}

func Test608_StreamRecordLandsInSink(t *testing.T) {
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, nil)
    s := newTestServer()
    s.cfg.Config.Routing.Mode = "local"
    s.rolloutTap = tap
    ch := make(chan adapter.StreamChunk, 4)
    ch <- adapter.StreamChunk{Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]interface{}{"content": "hel"}}}}
    ch <- adapter.StreamChunk{Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]interface{}{"content": "lo"}}}}
    ch <- adapter.StreamChunk{Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]interface{}{}, FinishReason: strPtr("stop")}}}
    close(ch)
    s.pool.Register("fusion-mlx", &mockProvider{name: "fusion-mlx", healthy: true, streamCh: ch}, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})

    body := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"hi"}],"stream":true}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("stream code = %d body=%s", rec.Code, rec.Body.String())
    }
    recs := rollout608WaitRecords(t, path, 1)
    r := recs[0]
    if r.FinishReason != "stop" {
        t.Errorf("finish_reason = %q", r.FinishReason)
    }
    if len(r.Messages) != 2 || r.Messages[1].Content != "hello" {
        t.Errorf("messages = %+v", r.Messages)
    }
}

func Test608_RedactModeRedactsContent(t *testing.T) {
    tap, path := rollout608Tap(t, "", nil) // empty mode = redact default
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, chatResp: rollout608ChatResponse()})
    s.rolloutTap = tap

    rollout608NonStreamReq(t, s, "team-a", nil)
    recs := rollout608WaitRecords(t, path, 1)
    r := recs[0]
    for i, m := range r.Messages {
        if m.Content != fmt.Sprintf("[redacted:%d]", len("hello world")) && i == 0 {
            t.Errorf("user content not redacted: %q", m.Content)
        }
    }
    if r.Messages[1].Content != "[redacted:8]" {
        t.Errorf("assistant content not redacted: %q", r.Messages[1].Content)
    }
    if strings.Contains(r.Messages[0].Content, "hello") {
        t.Errorf("raw content leaked in redact mode: %q", r.Messages[0].Content)
    }
}

func Test608_TenantNotAllowlistedNoRecord(t *testing.T) {
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, []string{"team-a"})
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, chatResp: rollout608ChatResponse()})
    s.rolloutTap = tap

    rollout608NonStreamReq(t, s, "team-b", nil)
    time.Sleep(200 * time.Millisecond)
    if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
        t.Fatalf("expected no records for non-allowlisted tenant, got %s", raw)
    }
}

func Test608_KeyOverrideDenyBeatsAllow(t *testing.T) {
    deny := false
    allow := true
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, []string{"k1"})
    s := newTestServerWithProvider("test-cloud", &mockProvider{name: "test-cloud", healthy: true, chatResp: rollout608ChatResponse()})
    s.rolloutTap = tap

    rollout608NonStreamReq(t, s, "k1", &config.AuthKeyConfig{Name: "k1", RolloutTap: &deny})
    time.Sleep(150 * time.Millisecond)
    if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
        t.Fatalf("key override deny must beat tenant allow, got %s", raw)
    }

    // Explicit allow on an unnamed-plane tenant records.
    rollout608NonStreamReq(t, s, "k2", &config.AuthKeyConfig{Name: "k2", RolloutTap: &allow})
    recs := rollout608WaitRecords(t, path, 1)
    if recs[0].TenantID != "k2" {
        t.Errorf("tenant_id = %q", recs[0].TenantID)
    }
}

func Test608_CaptureWriterCapsAndForwards(t *testing.T) {
    var forwarded strings.Builder
    base := writerFunc{fn: func(p []byte) (int, error) { return forwarded.Write(p) }}
    capw := newRolloutTapCapture(base, 16, "t")
    big := strings.Repeat("x", 100)
    n, err := capw.Write([]byte(big))
    if err != nil || n != len(big) {
        t.Fatalf("forwarded write = %d, %v", n, err)
    }
    if forwarded.String() != big {
        t.Fatalf("real response truncated — isolation contract broken")
    }
    if capw.buf.Len() != 16 || !capw.truncated {
        t.Fatalf("capture cap failed: len=%d truncated=%v", capw.buf.Len(), capw.truncated)
    }
}

func Test608_SSEParserAccumulatesDeltas(t *testing.T) {
    body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n" +
        ": keepalive\n\n" +
        "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"}}]}\n\n" +
        "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
        "data: [DONE]\n\n"
    content, finish := parseSSEBody([]byte(body))
    if content != "hello" || finish != "stop" {
        t.Fatalf("content=%q finish=%q", content, finish)
    }
}

func Test608_NonStreamParserReadsChoices(t *testing.T) {
    resp := rollout608ChatResponse()
    raw, _ := json.Marshal(resp)
    content, finish := parseNonStreamBody(raw)
    if finish != "stop" {
        t.Fatalf("finish=%q", finish)
    }
    if !strings.Contains(content, "hi there") {
        t.Fatalf("content=%q", content)
    }
}

type writerFunc struct {
    fn func([]byte) (int, error)
}

func (f writerFunc) Write(p []byte) (int, error) { return f.fn(p) }

func (f writerFunc) Header() http.Header         { return http.Header{} }
func (f writerFunc) WriteHeader(int)             {}
