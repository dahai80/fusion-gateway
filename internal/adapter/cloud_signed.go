package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// MessagesProvider is implemented by backends that expose a native Anthropic
// Messages API passthrough (issue #40). The /v1/messages server handler
// dispatches to this interface when present so bedrock / vertex / foundry can
// forward signed requests without an OpenAI round-trip, preserving Anthropic
// SSE events, error status/headers, and request-id for fusion-code's error
// bridge (isApiErrorLike).
type MessagesProvider interface {
	Messages(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error)
	StreamMessages(ctx context.Context, req *AnthropicRequest) (<-chan AnthropicStreamEvent, error)
}

// MessagesHTTPError preserves the upstream HTTP status code and request-id so
// the /v1/messages handler can surface them verbatim to fusion-code instead of
// collapsing every failure into 502. Implements the error interface.
type MessagesHTTPError struct {
	StatusCode int
	RequestID  string
	Body       string
}

func (e *MessagesHTTPError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("upstream status %d (request-id %s): %s", e.StatusCode, e.RequestID, e.Body)
	}
	return fmt.Sprintf("upstream status %d: %s", e.StatusCode, e.Body)
}

// extractUpstreamError reads a non-2xx response body and request-id header
// into a *MessagesHTTPError. Callers must still close resp.Body.
func extractUpstreamError(resp *http.Response) *MessagesHTTPError {
	reqID := resp.Header.Get("x-request-id")
	if reqID == "" {
		reqID = resp.Header.Get("request-id")
	}
	body := ""
	if resp.Body != nil {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				if len(buf) > 10<<20 {
					break
				}
			}
			if err != nil {
				break
			}
		}
		body = string(buf)
	}
	return &MessagesHTTPError{StatusCode: resp.StatusCode, RequestID: reqID, Body: body}
}

// parseAnthropicEventStreamRaw parses a native Anthropic SSE stream (used by
// vertex / foundry, which forward upstream SSE verbatim) into
// AnthropicStreamEvent values. Mirrors AnthropicProvider.parseAnthropicStreamEvents
// but is a package-level func so the cloud-signed providers can share it.
// 1 MiB/line cap matches the SSE hardening convention.
func parseAnthropicEventStreamRaw(body io.Reader, ch chan<- AnthropicStreamEvent) {
	buf := make([]byte, 4096)
	var lineBuf []byte
	const maxLineSize = 1 << 20
	for {
		n, err := body.Read(buf)
		if n > 0 {
			lineBuf = append(lineBuf, buf[:n]...)
			if len(lineBuf) > maxLineSize {
				slog.Error("anthropic event stream line exceeded max size, discarding", "size", len(lineBuf))
				lineBuf = nil
			}
		}
		for {
			idx := bytes.IndexByte(lineBuf, byte('\n'))
			if idx < 0 {
				break
			}
			line := string(bytes.TrimSpace(lineBuf[:idx]))
			lineBuf = lineBuf[idx+1:]
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}
			var event AnthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				slog.Warn("anthropic event stream unmarshal error", "error", err)
				continue
			}
			select {
			case ch <- event:
			default:
				slog.Warn("anthropic event stream backpressure")
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Error("anthropic event stream read error", "error", err)
			}
			break
		}
	}
}

// anthropicEventsToChunks relays AnthropicStreamEvent values (from
// StreamMessages) into OpenAI-shaped StreamChunk values so the providers'
// StreamChat satisfies the OpenAI /v1/chat/completions streaming path. Mirrors
// the conversion in AnthropicProvider.parseAnthropicSSE.
func anthropicEventsToChunks(evCh <-chan AnthropicStreamEvent, ch chan<- StreamChunk, model string) {
	var outputTokens int
	var msgID string
	for event := range evCh {
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				msgID = event.Message.ID
				outputTokens = event.Message.Usage.OutputTokens
			}
		case "content_block_delta":
			var delta struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
			}
			if err := json.Unmarshal(event.Delta, &delta); err == nil && delta.Type == "text_delta" {
				chunk := StreamChunk{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   model,
					Choices: []ChoiceDelta{{
						Index: event.Index,
						Delta: map[string]string{"role": "assistant", "content": delta.Text},
					}},
				}
				select {
				case ch <- chunk:
				default:
					return
				}
			}
		case "message_delta":
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
			}
			stopReason := event.StopReason
			if stopReason == "end_turn" {
				stopReason = "stop"
			}
			if stopReason == "" {
				stopReason = "stop"
			}
			fr := stopReason
			chunk := StreamChunk{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []ChoiceDelta{{
					Index:        0,
					FinishReason: &fr,
				}},
				Usage: &UsageResponse{
					PromptTokens:     0,
					CompletionTokens: outputTokens,
					TotalTokens:      outputTokens,
				},
			}
			select {
			case ch <- chunk:
			default:
			}
		case "message_stop":
			return
		}
	}
}

// b64Decode is a thin wrapper around stdlib base64 used by the Bedrock
// event-stream parser for {"bytes": "<base64>"} frames.
func b64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// AggregateAnthropicStreamEvents consumes a native Anthropic SSE event stream
// (from MessagesProvider.StreamMessages) and reconstructs a single non-stream
// AnthropicResponse. Used by the /v1/messages non-stream path to internally
// stream against reasoning upstreams (e.g. glm5.2 via LiteLLM) that withhold
// response headers until full generation completes — streaming yields a 2s
// TTFB so the gateway no longer blocks on the upstream header and trips
// Client.Timeout exceeded / client-cancel 502s. Handles text, thinking
// (thinking_delta + signature_delta), and tool_use (input_json_delta) blocks.
func AggregateAnthropicStreamEvents(evCh <-chan AnthropicStreamEvent) (*AnthropicResponse, error) {
	resp := &AnthropicResponse{
		Type:  "message",
		Role:  "assistant",
		Usage: AnthropicUsage{},
	}
	var blocks []AnthropicContentBlock
	var inputBufs map[int][]byte
	flushBlock := func(idx int, blk AnthropicContentBlock) {
		for i := len(blocks); i <= idx; i++ {
			blocks = append(blocks, AnthropicContentBlock{})
		}
		if blk.Type == "tool_use" && len(inputBufs[idx]) > 0 {
			blk.Input = inputBufs[idx]
		}
		blocks[idx] = blk
	}
	inputBufs = make(map[int][]byte)

	for ev := range evCh {
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				resp.ID = ev.Message.ID
				resp.Model = ev.Message.Model
				resp.Role = ev.Message.Role
				if ev.Message.Role == "" {
					resp.Role = "assistant"
				}
				resp.Usage.InputTokens = ev.Message.Usage.InputTokens
				resp.Usage.OutputTokens = ev.Message.Usage.OutputTokens
				resp.Usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
				resp.Usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
			}
		case "content_block_start":
			if ev.ContentBlock == nil {
				continue
			}
			idx := ev.Index
			blk := *ev.ContentBlock
			if blk.Type == "tool_use" {
				inputBufs[idx] = []byte{}
			}
			flushBlock(idx, blk)
		case "content_block_delta":
			idx := ev.Index
			if idx >= len(blocks) {
				slog.Warn("anthropic aggregate delta before block_start", "index", idx)
				continue
			}
			var delta struct {
				Type      string `json:"type"`
				Text      string `json:"text,omitempty"`
				Thinking  string `json:"thinking,omitempty"`
				Signature string `json:"signature,omitempty"`
				Partial   json.RawMessage `json:"partial_json,omitempty"`
			}
			if err := json.Unmarshal(ev.Delta, &delta); err != nil {
				slog.Warn("anthropic aggregate delta unmarshal error", "error", err)
				continue
			}
			switch delta.Type {
			case "text_delta":
				blocks[idx].Text += delta.Text
			case "thinking_delta":
				blocks[idx].Thinking += delta.Thinking
			case "signature_delta":
				blocks[idx].Signature += delta.Signature
			case "input_json_delta":
				if len(delta.Partial) > 0 {
					var frag string
					if err := json.Unmarshal(delta.Partial, &frag); err == nil {
						inputBufs[idx] = append(inputBufs[idx], frag...)
					} else {
						inputBufs[idx] = append(inputBufs[idx], delta.Partial...)
					}
				}
			}
		case "content_block_stop":
			// Flush accumulated tool_use partial_json into the block's Input.
			if buf, ok := inputBufs[ev.Index]; ok && len(buf) > 0 && ev.Index < len(blocks) {
				blocks[ev.Index].Input = buf
			}
		case "message_delta":
			if ev.Usage != nil {
				resp.Usage.OutputTokens = ev.Usage.OutputTokens
				if ev.Usage.CacheCreationInputTokens > 0 {
					resp.Usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					resp.Usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
			}
			if ev.StopReason != "" {
				resp.StopReason = ev.StopReason
			}
			if ev.StopSequence != nil {
				resp.StopSequence = ev.StopSequence
			}
		case "message_stop":
			resp.Content = blocks
			return resp, nil
		case "error":
			var errBody struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			msg := "upstream stream error"
			if err := json.Unmarshal(ev.Delta, &errBody); err == nil && errBody.Error.Message != "" {
				msg = errBody.Error.Message
			}
			slog.Error("anthropic aggregate stream error event", "message", msg)
			return nil, fmt.Errorf("anthropic stream error: %s", msg)
		}
	}
	resp.Content = blocks
	if resp.StopReason == "" {
		resp.StopReason = "end_turn"
		slog.Warn("anthropic aggregate stream ended without stop_reason, defaulting end_turn")
	}
	return resp, nil
}
