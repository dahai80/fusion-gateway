package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestMessagesProvider_AnthropicSatisfies(t *testing.T) {
	var _ MessagesProvider = (*AnthropicProvider)(nil)
	var _ MessagesProvider = (*BedrockProvider)(nil)
	var _ MessagesProvider = (*VertexProvider)(nil)
	var _ MessagesProvider = (*FoundryProvider)(nil)
}

func TestMessagesHTTPError_Format(t *testing.T) {
	e := &MessagesHTTPError{StatusCode: 429, RequestID: "req_abc", Body: `{"error":"throttled"}`}
	if e.Error() == "" {
		t.Fatal("expected non-empty error string")
	}
	e2 := &MessagesHTTPError{StatusCode: 500, Body: "boom"}
	if e2.Error() == "" {
		t.Fatal("expected non-empty error string without request-id")
	}
}

func TestExtractUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "rid-123")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"throttled"}`))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	e := extractUpstreamError(resp)
	if e.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", e.StatusCode)
	}
	if e.RequestID != "rid-123" {
		t.Fatalf("expected rid-123, got %q", e.RequestID)
	}
	if e.Body != `{"error":"throttled"}` {
		t.Fatalf("unexpected body: %q", e.Body)
	}
}

func TestParseAnthropicEventStreamRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"stop_reason\":\"end_turn\",\"usage\":{\"output_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ch := make(chan AnthropicStreamEvent, 16)
	parseAnthropicEventStreamRaw(resp.Body, ch)
	close(ch)
	var types []string
	for ev := range ch {
		types = append(types, ev.Type)
	}
	if len(types) != 4 {
		t.Fatalf("expected 4 events, got %d (%v)", len(types), types)
	}
	if types[0] != "message_start" || types[3] != "message_stop" {
		t.Fatalf("unexpected event order: %v", types)
	}
}

func TestAnthropicEventsToChunks(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 8)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1", Usage: AnthropicUsage{OutputTokens: 0}}}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"text_delta","text":"hello"}`)}
	evCh <- AnthropicStreamEvent{Type: "message_delta", StopReason: "end_turn", Usage: &AnthropicUsage{OutputTokens: 1}}
	close(evCh)
	ch := make(chan StreamChunk, 8)
	anthropicEventsToChunks(evCh, ch, "claude-x")
	close(ch)
	var n int
	var sawFinish bool
	for c := range ch {
		n++
		if c.Choices != nil && c.Choices[0].FinishReason != nil && *c.Choices[0].FinishReason == "stop" {
			sawFinish = true
		}
	}
	if n != 2 {
		t.Fatalf("expected 2 chunks, got %d", n)
	}
	if !sawFinish {
		t.Fatal("expected a finish chunk with stop")
	}
}

func TestB64Decode(t *testing.T) {
	out, err := b64Decode("aGVsbG8=")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hello" {
		t.Fatalf("expected hello, got %s", string(out))
	}
}

func TestBedrockModelPath_EncodesColon(t *testing.T) {
	got := bedrockModelPath("anthropic.claude-3-5-sonnet-20240620-v1:0")
	if got != "anthropic.claude-3-5-sonnet-20240620-v1%3A0" {
		t.Fatalf("expected colon encoded, got %s", got)
	}
}

func TestBedrockProvider_MissingCreds(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	p := NewBedrockProvider("bedrock", config.BackendConfig{})
	_, err := p.Messages(context.Background(), &AnthropicRequest{Model: "anthropic.claude-3-5-sonnet-20240620-v1:0"})
	if err == nil {
		t.Fatal("expected missing-credentials error")
	}
}
