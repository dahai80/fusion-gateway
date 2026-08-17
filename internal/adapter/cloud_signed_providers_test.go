package adapter

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestBedrockProvider_SigV4AndMessages verifies the SigV4 Authorization
// header is well-formed (correct credential scope, signed headers, signature)
// and that a 200 Anthropic response is decoded and returned verbatim.
func TestBedrockProvider_SigV4AndMessages(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secrettest")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", "us-west-2")

	var gotAuthz, gotHost, gotPath, gotAmzDate, gotContentHash string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthz = r.Header.Get("Authorization")
		gotHost = r.Host
		gotPath = r.URL.EscapedPath()
		gotAmzDate = r.Header.Get("X-Amz-Date")
		gotContentHash = r.Header.Get("X-Amz-Content-Sha256")
		body, _ := io.ReadAll(r.Body)
		h := sha256.Sum256(body)
		_ = h
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_b_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"anthropic.claude-3-5-sonnet-20240620-v1:0","stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()

	p := NewBedrockProvider("bedrock", config.BackendConfig{BaseURL: srv.URL})
	resp, err := p.Messages(context.Background(), &AnthropicRequest{
		Model:     "anthropic.claude-3-5-sonnet-20240620-v1:0",
		MaxTokens: 100,
		Messages:  []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "ping"}}}},
	})
	if err != nil {
		t.Fatalf("messages failed: %v", err)
	}
	if resp.ID != "msg_b_1" {
		t.Fatalf("expected msg_b_1, got %s", resp.ID)
	}

	// URL path must contain the encoded model id (colon → %3A) and /invoke suffix
	if !strings.HasSuffix(gotPath, "/invoke") {
		t.Fatalf("expected /invoke path, got %s", gotPath)
	}
	if !strings.Contains(gotPath, "anthropic.claude-3-5-sonnet-20240620-v1%3A0") {
		t.Fatalf("expected encoded model id in path, got %s", gotPath)
	}

	// Authorization header structure
	if !strings.HasPrefix(gotAuthz, "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
		t.Fatalf("unexpected authz prefix: %s", gotAuthz)
	}
	if !strings.Contains(gotAuthz, "/us-west-2/bedrock/aws4_request") {
		t.Fatalf("expected us-west-2/bedrock credential scope, got: %s", gotAuthz)
	}
	if !strings.Contains(gotAuthz, "SignedHeaders=") || !strings.Contains(gotAuthz, "Signature=") {
		t.Fatalf("missing SignedHeaders/Signature: %s", gotAuthz)
	}
	if gotAmzDate == "" {
		t.Fatal("expected X-Amz-Date header")
	}
	if gotContentHash == "" {
		t.Fatal("expected X-Amz-Content-Sha256 header")
	}
	_ = gotHost
}

// TestBedrockProvider_ErrorPassthrough verifies a non-200 upstream surfaces as
// *MessagesHTTPError with status + request-id preserved (issue #40 acceptance).
func TestBedrockProvider_ErrorPassthrough(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secrettest")
	t.Setenv("AWS_REGION", "us-east-1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "bedrock-rid-9")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"throttled"}`))
	}))
	defer srv.Close()
	p := NewBedrockProvider("bedrock", config.BackendConfig{BaseURL: srv.URL})
	_, err := p.Messages(context.Background(), &AnthropicRequest{Model: "anthropic.claude-3-5-haiku-20241022-v1:0", MaxTokens: 10, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
	if err == nil {
		t.Fatal("expected error")
	}
	httpErr, ok := err.(*MessagesHTTPError)
	if !ok {
		t.Fatalf("expected *MessagesHTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", httpErr.StatusCode)
	}
	if httpErr.RequestID != "bedrock-rid-9" {
		t.Fatalf("expected bedrock-rid-9, got %q", httpErr.RequestID)
	}
}

// TestBedrockProvider_StreamMessages_BedrockWrapper verifies the Bedrock
// {"payload": {...}} event-stream wrapper is unwrapped to native Anthropic
// events.
func TestBedrockProvider_StreamMessages_BedrockWrapper(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secrettest")
	t.Setenv("AWS_REGION", "us-east-1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"payload\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg_s\",\"usage\":{\"output_tokens\":0}}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"payload\":{\"type\":\"message_stop\"}}\n\n"))
	}))
	defer srv.Close()
	p := NewBedrockProvider("bedrock", config.BackendConfig{BaseURL: srv.URL})
	ch, err := p.StreamMessages(context.Background(), &AnthropicRequest{Model: "anthropic.claude-3-5-sonnet-20240620-v1:0", MaxTokens: 10, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for ev := range ch {
		types = append(types, ev.Type)
	}
	if len(types) != 2 || types[0] != "message_start" || types[1] != "message_stop" {
		t.Fatalf("unexpected events: %v", types)
	}
}

func TestBedrockProvider_ListModels(t *testing.T) {
	p := NewBedrockProvider("bedrock", config.BackendConfig{})
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("expected non-empty model list")
	}
	if _, err := p.Embedding(context.Background(), &EmbeddingRequest{}); err == nil {
		t.Fatal("expected embedding not supported")
	}
	if _, err := p.Rerank(context.Background(), &RerankRequest{}); err == nil {
		t.Fatal("expected rerank not supported")
	}
}

// TestBedrockProvider_ChatViaMessages verifies the OpenAI Chat path routes
// through Messages (SigV4) and converts back to OpenAI shape.
func TestBedrockProvider_ChatViaMessages(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secrettest")
	t.Setenv("AWS_REGION", "us-east-1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_c","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := NewBedrockProvider("bedrock", config.BackendConfig{BaseURL: srv.URL})
	resp, err := p.Chat(context.Background(), &ChatRequest{Model: "claude", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_c" {
		t.Fatalf("expected msg_c, got %s", resp.ID)
	}
	if resp.Usage.TotalTokens != 2 {
		t.Fatalf("expected 2 total tokens, got %d", resp.Usage.TotalTokens)
	}
}
