package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestAggregateAnthropicStreamEvents_Text(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 16)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1", Model: "glm5.2", Role: "assistant", Usage: AnthropicUsage{InputTokens: 10, OutputTokens: 0}}}
	evCh <- AnthropicStreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &AnthropicContentBlock{Type: "text"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"text_delta","text":"hello "}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"text_delta","text":"world"}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_stop", Index: 0}
	evCh <- AnthropicStreamEvent{Type: "message_delta", StopReason: "end_turn", Usage: &AnthropicUsage{OutputTokens: 2}}
	evCh <- AnthropicStreamEvent{Type: "message_stop"}
	close(evCh)

	resp, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_1" || resp.Model != "glm5.2" || resp.Role != "assistant" {
		t.Fatalf("unexpected header: %+v", resp)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("expected end_turn, got %s", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "hello world" {
		t.Fatalf("unexpected content: %+v", resp.Content)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestAggregateAnthropicStreamEvents_ThinkingAndSignature(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 16)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_2", Model: "glm5.2"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &AnthropicContentBlock{Type: "thinking"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"thinking_delta","thinking":"reason "}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"thinking_delta","thinking":"here"}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"signature_delta","signature":"sig-abc"}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_stop", Index: 0}
	evCh <- AnthropicStreamEvent{Type: "message_stop"}
	close(evCh)

	resp, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(resp.Content))
	}
	if resp.Content[0].Type != "thinking" || resp.Content[0].Thinking != "reason here" || resp.Content[0].Signature != "sig-abc" {
		t.Fatalf("unexpected thinking block: %+v", resp.Content[0])
	}
}

func TestAggregateAnthropicStreamEvents_ToolUse(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 16)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_3", Model: "glm5.2"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "tool_1", Name: "get_weather"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"input_json_delta","partial_json":"{\"city\""}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"input_json_delta","partial_json":":\"Paris\"}"}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_stop", Index: 0}
	evCh <- AnthropicStreamEvent{Type: "message_delta", StopReason: "tool_use", Usage: &AnthropicUsage{OutputTokens: 5}}
	evCh <- AnthropicStreamEvent{Type: "message_stop"}
	close(evCh)

	resp, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" {
		t.Fatalf("expected tool_use, got %s", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" {
		t.Fatalf("unexpected content: %+v", resp.Content)
	}
	if resp.Content[0].Name != "get_weather" || resp.Content[0].ID != "tool_1" {
		t.Fatalf("unexpected tool identity: %+v", resp.Content[0])
	}
	if string(resp.Content[0].Input) != `{"city":"Paris"}` {
		t.Fatalf("unexpected tool input: %s", string(resp.Content[0].Input))
	}
}

func TestAggregateAnthropicStreamEvents_ErrorEvent(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 4)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_4"}}
	evCh <- AnthropicStreamEvent{Type: "error", Delta: json.RawMessage(`{"error":{"type":"overloaded_error","message":"Upstream overloaded"}}`)}
	close(evCh)

	_, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 0)
	if err == nil {
		t.Fatal("expected error from error event")
	}
	if !contains(err.Error(), "Upstream overloaded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAggregateAnthropicStreamEvents_EmptyStreamDefaultsEndTurn(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 2)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_5"}}
	close(evCh)

	resp, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("expected default end_turn, got %s", resp.StopReason)
	}
	if resp.ID != "msg_5" {
		t.Fatalf("expected msg_5, got %s", resp.ID)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- issue #69: Aggregate idle watchdog for the non-stream path ---

// TestAggregateAnthropicStreamEvents_IdleTimeout verifies the idle watchdog
// fires when the upstream stalls without closing the channel: a non-stream
// /v1/messages request that internally streams must not block forever. With a
// short idleTimeout and a channel that never receives, Aggregate must return an
// error referencing the idle cancellation within a bounded time.
func TestAggregateAnthropicStreamEvents_IdleTimeout(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent)
	defer close(evCh)

	start := time.Now()
	_, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 50*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected idle watchdog error, got nil")
	}
	if !contains(err.Error(), "idle") {
		t.Fatalf("expected error mentioning idle, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("idle watchdog did not fire promptly, elapsed %v", elapsed)
	}
	slog.Info("TestAggregateAnthropicStreamEvents_IdleTimeout passed", "elapsed", elapsed)
}

// TestAggregateAnthropicStreamEvents_CtxCancel verifies an externally canceled
// context aborts the aggregate loop even mid-stream (e.g. client gave up on the
// non-stream request while the upstream is still generating).
func TestAggregateAnthropicStreamEvents_CtxCancel(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 4)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_c"}}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var err error
	go func() {
		_, err = AggregateAnthropicStreamEvents(ctx, evCh, 5*time.Second)
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregate did not return after ctx cancel")
	}
	if err == nil {
		t.Fatal("expected canceled error, got nil")
	}
	if !contains(err.Error(), "canceled") {
		t.Fatalf("expected error mentioning canceled, got %v", err)
	}
	slog.Info("TestAggregateAnthropicStreamEvents_CtxCancel passed")
}

// TestAggregateAnthropicStreamEvents_IdleZeroSkips verifies backward compat:
// idleTimeout=0 disables the watchdog and a normal complete stream aggregates
// unchanged (the original pure-blocking behavior).
func TestAggregateAnthropicStreamEvents_IdleZeroSkips(t *testing.T) {
	evCh := make(chan AnthropicStreamEvent, 8)
	evCh <- AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_z", Model: "glm5.2"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &AnthropicContentBlock{Type: "text"}}
	evCh <- AnthropicStreamEvent{Type: "content_block_delta", Index: 0, Delta: json.RawMessage(`{"type":"text_delta","text":"ok"}`)}
	evCh <- AnthropicStreamEvent{Type: "content_block_stop", Index: 0}
	evCh <- AnthropicStreamEvent{Type: "message_stop"}
	close(evCh)

	resp, err := AggregateAnthropicStreamEvents(context.Background(), evCh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_z" {
		t.Fatalf("expected msg_z, got %s", resp.ID)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "ok" {
		t.Fatalf("unexpected content: %+v", resp.Content)
	}
	slog.Info("TestAggregateAnthropicStreamEvents_IdleZeroSkips passed")
}

// TestRR12_ParseAnthropicEventStreamRaw_OversizeLineClosesStream verifies the
// B6/RR12 fix: a single SSE line exceeding the 1 MiB cap must make the parser
// RETURN (close the stream), not set lineBuf=nil and keep reading. The old
// lineBuf=nil path resumed reading at an arbitrary mid-line byte offset,
// producing half-JSON "data:" lines forever (SSE has no length framing) — the
// stream never terminated, the client spun with no valid content and no error.
// Returning is the only correct recovery: the byte position is already
// desynchronized, resync is impossible.
func TestRR12_ParseAnthropicEventStreamRaw_OversizeLineClosesStream(t *testing.T) {
	// A single "data: " line with >1MiB of payload and NO trailing newline.
	// The parser appends every Read into lineBuf; with no '\n' it never flushes
	// a line, so lineBuf grows past maxLineSize in one shot.
	oversize := bytes.Repeat([]byte("x"), (1<<20)+4096)
	body := bytesReader(append([]byte("data: "), oversize...))

	ch := make(chan AnthropicStreamEvent, 16)
	done := make(chan struct{})
	go func() {
		parseAnthropicEventStreamRaw(body, ch)
		close(done)
	}()

	select {
	case <-done:
		// parser returned on oversize line — correct (RR12 fix)
	case <-time.After(3 * time.Second):
		t.Fatal("parseAnthropicEventStreamRaw hung on oversize line; lineBuf=nil path kept reading instead of returning")
	}
	// Channel should be closeable (parser exited); drain anything buffered.
	close(ch)
	for range ch {
	}
}

// TestRR12_ParseBedrockEventStream_OversizeLineClosesStream is the same guard
// for the Bedrock event-stream parser, which had the identical lineBuf=nil bug.
func TestRR12_ParseBedrockEventStream_OversizeLineClosesStream(t *testing.T) {
	oversize := bytes.Repeat([]byte("x"), (1<<20)+4096)
	body := bytesReader(append([]byte("data: "), oversize...))

	p := &BedrockProvider{name: "bedrock"}
	ch := make(chan AnthropicStreamEvent, 16)
	done := make(chan struct{})
	go func() {
		p.parseBedrockEventStream(body, ch)
		close(done)
	}()

	select {
	case <-done:
		// parser returned on oversize line — correct (RR12 fix)
	case <-time.After(3 * time.Second):
		t.Fatal("parseBedrockEventStream hung on oversize line; lineBuf=nil path kept reading instead of returning")
	}
	close(ch)
	for range ch {
	}
}

// TestRR12_OversizeLine_HalfJSONNotEmitted proves the desync symptom is gone:
// after the oversize line triggers return, NO half-JSON event leaks onto the
// channel. The old bug would keep parsing mid-line bytes as "data:" lines and
// emit junk. With the fix the channel receives zero events (parser returned
// before any line completed).
func TestRR12_OversizeLine_HalfJSONNotEmitted(t *testing.T) {
	// Oversize line, then (unreachable in the fixed parser) a valid event. If
	// the parser wrongly continued past the cap, it might emit a malformed
	// event from the tail. The fix returns before reaching the valid event.
	oversize := bytes.Repeat([]byte("x"), (1<<20)+4096)
	body := bytesReader(append(append([]byte("data: "), oversize...), []byte("\n\ndata: {\"type\":\"message_stop\"}\n\n")...))

	ch := make(chan AnthropicStreamEvent, 16)
	parseAnthropicEventStreamRaw(body, ch)
	close(ch)
	var count int
	for range ch {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 events (parser returns on oversize, never reaches tail), got %d — half-JSON leaked", count)
	}
}
