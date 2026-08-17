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
