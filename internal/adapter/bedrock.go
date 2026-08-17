package adapter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// BedrockProvider forwards Anthropic Messages requests to AWS Bedrock,
// signing each request with AWS SigV4 (issue #40). Credentials are read from
// the gateway-side environment only — AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
// / AWS_REGION / AWS_SESSION_TOKEN — never echoed to fusion-code.
//
// Bedrock exposes two per-model endpoints:
//   POST https://bedrock-runtime.{region}.amazonaws.com/model/{modelId}/invoke
//   POST .../model/{modelId}/invoke-with-response-stream   (stream=true)
//
// The request body is the Anthropic Messages payload verbatim; the response is
// the Anthropic response verbatim, so SSE events + request-id pass through
// untouched for fusion-code's error bridge.
type BedrockProvider struct {
	name       string
	region     string
	baseURL    string // overrideable for tests; defaults to bedrock-runtime endpoint
	httpClient *http.Client
	accessKey  string
	secretKey  string
	sessionTok string
}

func NewBedrockProvider(name string, backendCfg config.BackendConfig) *BedrockProvider {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}
	baseURL := backendCfg.BaseURL
	if baseURL == "" {
		baseURL = "https://bedrock-runtime." + region + ".amazonaws.com"
	}
	timeout := backendCfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &BedrockProvider{
		name:       name,
		region:     region,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: timeout},
		accessKey:  os.Getenv("AWS_ACCESS_KEY_ID"),
		secretKey:  os.Getenv("AWS_SECRET_ACCESS_KEY"),
		sessionTok: os.Getenv("AWS_SESSION_TOKEN"),
	}
}

func (p *BedrockProvider) Name() string { return p.name }

func (p *BedrockProvider) HealthCheck(ctx context.Context) error {
	if p.accessKey == "" || p.secretKey == "" {
		return fmt.Errorf("bedrock: missing AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY env")
	}
	return nil
}

// bedrockModelPath returns the Bedrock model-id path segment. Bedrock model
// ids may contain ":" (e.g. anthropic.claude-3-5-sonnet-20240620-v1:0) which
// must be URL-encoded as %3A on the path.
func bedrockModelPath(model string) string {
	// url.PathEscape leaves ":" unescaped (legal pchar per RFC 3986), but the
	// Bedrock path API rejects the raw colon — it must be %3A.
	return strings.ReplaceAll(url.PathEscape(model), ":", "%3A")
}

func (p *BedrockProvider) messagesURL(model string, stream bool) string {
	suffix := "invoke"
	if stream {
		suffix = "invoke-with-response-stream"
	}
	return p.baseURL + "/model/" + bedrockModelPath(model) + "/" + suffix
}

func (p *BedrockProvider) Messages(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal bedrock messages request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messagesURL(req.Model, false), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create bedrock messages request: %w", err)
	}
	if err := p.signSigV4(httpReq, body); err != nil {
		return nil, err
	}
	InjectFusionHeaders(ctx, httpReq)
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock messages request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, extractUpstreamError(resp)
	}
	var antResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&antResp); err != nil {
		return nil, fmt.Errorf("decode bedrock messages response: %w", err)
	}
	return &antResp, nil
}

func (p *BedrockProvider) StreamMessages(ctx context.Context, req *AnthropicRequest) (<-chan AnthropicStreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal bedrock stream messages request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messagesURL(req.Model, true), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create bedrock stream messages request: %w", err)
	}
	if err := p.signSigV4(httpReq, body); err != nil {
		return nil, err
	}
	InjectFusionHeaders(ctx, httpReq)
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock stream messages failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errUp := extractUpstreamError(resp)
		resp.Body.Close()
		return nil, errUp
	}
	ch := make(chan AnthropicStreamEvent, 64)
	safego.Go("bedrock_stream", func() {
		defer close(ch)
		defer resp.Body.Close()
		p.parseBedrockEventStream(resp.Body, ch)
	})
	return ch, nil
}

// parseBedrockEventStream parses the Bedrock response-stream format. Bedrock
// wraps each Anthropic event in an AWS event-stream frame of the shape:
//   {"payload": <anthropic-event-json>}
// emitted as SSE "data:" lines. We extract the inner Anthropic event so the
// downstream handler sees native Anthropic events unchanged. A plain Anthropic
// SSE "data:" line is also accepted so unit tests can reuse the simple format.
func (p *BedrockProvider) parseBedrockEventStream(body io.Reader, ch chan<- AnthropicStreamEvent) {
	buf := make([]byte, 4096)
	var lineBuf []byte
	const maxLineSize = 1 << 20
	for {
		n, err := body.Read(buf)
		if n > 0 {
			lineBuf = append(lineBuf, buf[:n]...)
			if len(lineBuf) > maxLineSize {
				slog.Error("bedrock stream line exceeded max size, discarding", "size", len(lineBuf))
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
			event := p.decodeBedrockPayload(data)
			if event == nil {
				continue
			}
			select {
			case ch <- *event:
			default:
				slog.Warn("bedrock stream backpressure")
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Error("bedrock stream read error", "error", err)
			}
			break
		}
	}
}

// decodeBedrockPayload accepts either a raw Anthropic event JSON or a Bedrock
// wrapper {"payload": {...}} / {"bytes": "<base64>"} and returns the inner
// Anthropic event. Returns nil on unmarshal failure (logged by caller loop).
func (p *BedrockProvider) decodeBedrockPayload(data string) *AnthropicStreamEvent {
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
		Bytes   string          `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(data), &wrapper); err == nil {
		if len(wrapper.Payload) > 0 {
			var ev AnthropicStreamEvent
			if err := json.Unmarshal(wrapper.Payload, &ev); err == nil {
				return &ev
			}
		}
		if wrapper.Bytes != "" {
			decoded, err := b64Decode(wrapper.Bytes)
			if err == nil {
				var ev AnthropicStreamEvent
				if err := json.Unmarshal(decoded, &ev); err == nil {
					return &ev
				}
			}
		}
	}
	var ev AnthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &ev); err == nil {
		return &ev
	}
	slog.Warn("bedrock stream event unmarshal error", "data", data)
	return nil
}

// Embedding/Rerank/Chat/StreamChat are served by the OpenAI-conversion path in
// the server handler (AnthropicToOpenAIChatRequest); Bedrock is reached
// natively only through /v1/messages. ListModels returns the common Anthropic
// model ids so /v1/models and the allowlist work without a Bedrock ListModels
// API call.
func (p *BedrockProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	antReq := OpenAIToAnthropic(req)
	antReq.Stream = false
	resp, err := p.Messages(ctx, antReq)
	if err != nil {
		return nil, err
	}
	return AnthropicToOpenAI(resp), nil
}

func (p *BedrockProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	antReq := OpenAIToAnthropic(req)
	antReq.Stream = true
	evCh, err := p.StreamMessages(ctx, antReq)
	if err != nil {
		return nil, err
	}
	ch := make(chan StreamChunk, 64)
	safego.Go("bedrock_stream_relay", func() {
		defer close(ch)
		anthropicEventsToChunks(evCh, ch, req.Model)
	})
	return ch, nil
}

func (p *BedrockProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, fmt.Errorf("bedrock: embeddings not supported")
}

func (p *BedrockProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	return nil, fmt.Errorf("bedrock: rerank not supported")
}

func (p *BedrockProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "anthropic.claude-3-5-sonnet-20240620-v1:0", Object: "model", OwnedBy: "bedrock"},
		{ID: "anthropic.claude-3-5-haiku-20241022-v1:0", Object: "model", OwnedBy: "bedrock"},
		{ID: "anthropic.claude-sonnet-4-20250514-v1:0", Object: "model", OwnedBy: "bedrock"},
	}, nil
}

// signSigV4 attaches an AWS SigV4 Authorization header to req. Payload hash is
// computed from body. Service is "bedrock"; region from provider config/env.
func (p *BedrockProvider) signSigV4(req *http.Request, body []byte) error {
	if p.accessKey == "" || p.secretKey == "" {
		return fmt.Errorf("bedrock: missing AWS credentials (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY)")
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if p.sessionTok != "" {
		req.Header.Set("X-Amz-Security-Token", p.sessionTok)
	}

	host := req.URL.Host
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	signedHeaders, canonicalHeaders := buildCanonicalHeaders(req, host)
	credentialScope := dateStamp + "/" + p.region + "/bedrock/aws4_request"
	canonicalRequest := req.Method + "\n" +
		canonicalURI + "\n" +
		req.URL.RawQuery + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		payloadHash

	stringToSign := "AWS4-HMAC-SHA256\n" +
		amzDate + "\n" +
		credentialScope + "\n" +
		sha256Hex([]byte(canonicalRequest))

	signingKey := p.deriveSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authz := "AWS4-HMAC-SHA256 Credential=" + p.accessKey + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders +
		", Signature=" + signature
	req.Header.Set("Authorization", authz)
	return nil
}

func (p *BedrockProvider) deriveSigningKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+p.secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(p.region))
	kService := hmacSHA256(kRegion, []byte("bedrock"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func buildCanonicalHeaders(req *http.Request, host string) (signed, canonical string) {
	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256"),
		"x-amz-date":           req.Header.Get("X-Amz-Date"),
	}
	if tok := req.Header.Get("X-Amz-Security-Token"); tok != "" {
		headers["x-amz-security-token"] = tok
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	// stable sorted order
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	var sb strings.Builder
	var sh strings.Builder
	for i, k := range keys {
		sb.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
		sh.WriteString(k)
		if i < len(keys)-1 {
			sh.WriteString(";")
		}
	}
	return sh.String(), sb.String()
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
