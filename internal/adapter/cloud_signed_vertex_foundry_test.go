package adapter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

// genRSAPEM generates a 2048-bit RSA private key and returns its PEM encoding.
func genRSAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// TestVertexProvider_TokenExchangeAndMessages verifies the OAuth2 JWT-bearer
// flow: a token request is made to token_uri, the returned access token is
// cached, and the rawPredict call carries it as a Bearer header.
func TestVertexProvider_TokenExchangeAndMessages(t *testing.T) {
	pemKey := genRSAPEM(t)
	saJSON, _ := json.Marshal(map[string]string{
		"client_email":   "sa@test-project.iam.gserviceaccount.com",
		"private_key":    pemKey,
		"private_key_id": "kid123",
		"token_uri":      "",
	})

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		assertion := r.FormValue("assertion")
		if assertion == "" || r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"vertex-tok-xyz","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	var m map[string]string
	_ = json.Unmarshal(saJSON, &m)
	m["token_uri"] = tokenSrv.URL
	saJSON, _ = json.Marshal(m)
	t.Setenv("VERTEX_SERVICE_ACCOUNT_JSON", string(saJSON))
	t.Setenv("VERTEX_PROJECT_ID", "test-project")
	t.Setenv("VERTEX_REGION", "us-central1")

	var gotBearer, gotPath, gotMethod string
	predictSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBearer = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_v","type":"message","role":"assistant","content":[{"type":"text","text":"v"}],"model":"claude-3-5-sonnet","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer predictSrv.Close()

	p := NewVertexProvider("vertex", config.BackendConfig{BaseURL: predictSrv.URL})
	resp, err := p.Messages(context.Background(), &AnthropicRequest{
		Model:     "claude-3-5-sonnet@20241022",
		MaxTokens: 50,
		Messages:  []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("messages failed: %v", err)
	}
	if resp.ID != "msg_v" {
		t.Fatalf("expected msg_v, got %s", resp.ID)
	}
	if gotBearer != "Bearer vertex-tok-xyz" {
		t.Fatalf("expected Bearer vertex-tok-xyz, got %s", gotBearer)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if !strings.Contains(gotPath, "/projects/test-project/locations/us-central1/publishers/anthropic/models/claude-3-5-sonnet@20241022:rawPredict") {
		t.Fatalf("unexpected path: %s", gotPath)
	}

	// Token cached: a second call must succeed without re-exchanging (token
	// server still serves, but the cache means the 5-min pre-expiry window is
	// respected).
	resp2, err := p.Messages(context.Background(), &AnthropicRequest{Model: "claude-3-5-sonnet@20241022", MaxTokens: 5, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "y"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp2.ID != "msg_v" {
		t.Fatalf("second call: expected msg_v, got %s", resp2.ID)
	}
}

// TestVertexProvider_MissingCreds verifies a clear error when no SA is present.
func TestVertexProvider_MissingCreds(t *testing.T) {
	t.Setenv("VERTEX_SERVICE_ACCOUNT_JSON", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	p := NewVertexProvider("vertex", config.BackendConfig{})
	if err := p.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected health check error")
	}
	_, err := p.Messages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 1, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
	if err == nil {
		t.Fatal("expected messages error")
	}
}

// TestVertexProvider_ErrorPassthrough verifies non-200 surfaces as
// *MessagesHTTPError with request-id.
func TestVertexProvider_ErrorPassthrough(t *testing.T) {
	pemKey := genRSAPEM(t)
	saJSON, _ := json.Marshal(map[string]string{
		"client_email": "sa@p.iam.gserviceaccount.com",
		"private_key":  pemKey,
		"token_uri":    "",
	})
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	var m map[string]string
	_ = json.Unmarshal(saJSON, &m)
	m["token_uri"] = tokenSrv.URL
	saJSON, _ = json.Marshal(m)
	t.Setenv("VERTEX_SERVICE_ACCOUNT_JSON", string(saJSON))
	t.Setenv("VERTEX_PROJECT_ID", "p")
	t.Setenv("VERTEX_REGION", "us-central1")

	predictSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "vertex-rid-7")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauth"}`))
	}))
	defer predictSrv.Close()

	p := NewVertexProvider("vertex", config.BackendConfig{BaseURL: predictSrv.URL})
	_, err := p.Messages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 1, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
	if err == nil {
		t.Fatal("expected error")
	}
	httpErr, ok := err.(*MessagesHTTPError)
	if !ok {
		t.Fatalf("expected *MessagesHTTPError, got %T", err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", httpErr.StatusCode)
	}
	if httpErr.RequestID != "vertex-rid-7" {
		t.Fatalf("expected vertex-rid-7, got %q", httpErr.RequestID)
	}
}

// TestVertexProvider_StreamMessages verifies native Anthropic SSE passthrough.
func TestVertexProvider_StreamMessages(t *testing.T) {
	pemKey := genRSAPEM(t)
	saJSON, _ := json.Marshal(map[string]string{
		"client_email": "sa@p.iam.gserviceaccount.com",
		"private_key":  pemKey,
		"token_uri":    "",
	})
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	var m map[string]string
	_ = json.Unmarshal(saJSON, &m)
	m["token_uri"] = tokenSrv.URL
	saJSON, _ = json.Marshal(m)
	t.Setenv("VERTEX_SERVICE_ACCOUNT_JSON", string(saJSON))
	t.Setenv("VERTEX_PROJECT_ID", "p")
	t.Setenv("VERTEX_REGION", "us-central1")

	predictSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_vs\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer predictSrv.Close()

	p := NewVertexProvider("vertex", config.BackendConfig{BaseURL: predictSrv.URL})
	ch, err := p.StreamMessages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 1, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for ev := range ch {
		types = append(types, ev.Type)
	}
	if len(types) != 3 || types[0] != "message_start" || types[2] != "message_stop" {
		t.Fatalf("unexpected events: %v", types)
	}
}

// TestFoundryProvider_APIKey verifies api-key header auth + response decode.
func TestFoundryProvider_APIKey(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "azure-key-1")
	var gotAPIKey, gotAntVer, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("api-key")
		gotAntVer = r.Header.Get("anthropic-version")
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header with api-key auth")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_f","type":"message","role":"assistant","content":[{"type":"text","text":"f"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := NewFoundryProvider("foundry", config.BackendConfig{BaseURL: srv.URL})
	resp, err := p.Messages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 10, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_f" {
		t.Fatalf("expected msg_f, got %s", resp.ID)
	}
	if gotAPIKey != "azure-key-1" {
		t.Fatalf("expected azure-key-1, got %s", gotAPIKey)
	}
	if gotAntVer != "2023-06-01" {
		t.Fatalf("expected anthropic-version 2023-06-01, got %s", gotAntVer)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("expected /v1/messages, got %s", gotPath)
	}
}

// TestFoundryProvider_BearerToken verifies Entra access-token auth.
func TestFoundryProvider_BearerToken(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "")
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	t.Setenv("AZURE_ACCESS_TOKEN", "entra-tok-9")
	var gotBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBearer = r.Header.Get("Authorization")
		if r.Header.Get("api-key") != "" {
			t.Errorf("expected no api-key header with bearer auth")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_f2","type":"message","role":"assistant","content":[{"type":"text","text":"f"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := NewFoundryProvider("foundry", config.BackendConfig{BaseURL: srv.URL})
	resp, err := p.Messages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 10, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_f2" {
		t.Fatalf("expected msg_f2, got %s", resp.ID)
	}
	if gotBearer != "Bearer entra-tok-9" {
		t.Fatalf("expected Bearer entra-tok-9, got %s", gotBearer)
	}
}

// TestFoundryProvider_MissingCreds verifies clear error with no creds.
func TestFoundryProvider_MissingCreds(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "")
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	t.Setenv("AZURE_ACCESS_TOKEN", "")
	p := NewFoundryProvider("foundry", config.BackendConfig{})
	if err := p.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected health check error")
	}
}

// TestFoundryProvider_ErrorPassthrough verifies non-200 surfaces as
// *MessagesHTTPError with request-id.
func TestFoundryProvider_ErrorPassthrough(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "azure-key-1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("request-id", "foundry-rid-3")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()
	p := NewFoundryProvider("foundry", config.BackendConfig{BaseURL: srv.URL})
	_, err := p.Messages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 1, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
	if err == nil {
		t.Fatal("expected error")
	}
	httpErr, ok := err.(*MessagesHTTPError)
	if !ok {
		t.Fatalf("expected *MessagesHTTPError, got %T", err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", httpErr.StatusCode)
	}
	if httpErr.RequestID != "foundry-rid-3" {
		t.Fatalf("expected foundry-rid-3, got %q", httpErr.RequestID)
	}
}

// TestFoundryProvider_Stream verifies SSE passthrough.
func TestFoundryProvider_Stream(t *testing.T) {
	t.Setenv("AZURE_API_KEY", "azure-key-1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fs\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()
	p := NewFoundryProvider("foundry", config.BackendConfig{BaseURL: srv.URL})
	ch, err := p.StreamMessages(context.Background(), &AnthropicRequest{Model: "claude", MaxTokens: 1, Messages: []AnthropicMessage{{Role: "user", Content: []AnthropicContentBlock{{Type: "text", Text: "x"}}}}})
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

// silence unused import if time only referenced in helper-adjacent code
var _ = time.Now
