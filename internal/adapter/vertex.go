package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// VertexProvider forwards Anthropic Messages requests to Google Cloud Vertex
// AI (issue #40). Auth is a GCP service-account OAuth2 access token obtained
// by exchanging a self-signed RS256 JWT at https://oauth2.googleapis.com/token.
// The token is cached and refreshed ~5 min before expiry. Credentials are read
// from the gateway-side environment only — VERTEX_SERVICE_ACCOUNT_JSON (inline
// JSON) or GOOGLE_APPLICATION_CREDENTIALS (path to a JSON key file) — never
// echoed to fusion-code.
//
// Endpoint (Anthropic on Vertex "rawPredict"):
//   POST {baseURL}/v1/projects/{project}/locations/{region}/publishers/anthropic/models/{model}:rawPredict
// base_url defaults to https://{region}-aiplatform.googleapis.com
type VertexProvider struct {
	name       string
	project    string
	region     string
	baseURL    string
	httpClient *http.Client

	mu          sync.Mutex
	saJSON      []byte
	cachedToken string
	tokenExpiry time.Time
	tokenClient *http.Client
}

type gcpServiceAccount struct {
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

func NewVertexProvider(name string, backendCfg config.BackendConfig) *VertexProvider {
	region := os.Getenv("VERTEX_REGION")
	if region == "" {
		region = os.Getenv("GOOGLE_CLOUD_REGION")
	}
	if region == "" {
		region = "us-central1"
	}
	project := os.Getenv("VERTEX_PROJECT_ID")
	if project == "" {
		project = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	baseURL := backendCfg.BaseURL
	if baseURL == "" {
		baseURL = "https://" + region + "-aiplatform.googleapis.com"
	}
	timeout := backendCfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &VertexProvider{
		name:        name,
		project:     project,
		region:      region,
		baseURL:     baseURL,
		httpClient:  &http.Client{Timeout: timeout, Transport: TransportForBackend(backendCfg)},
		tokenClient: &http.Client{Timeout: 30 * time.Second, Transport: TransportForBackend(backendCfg)},
	}
}

func (p *VertexProvider) Name() string { return p.name }

func (p *VertexProvider) HealthCheck(ctx context.Context) error {
	if _, err := p.loadServiceAccount(); err != nil {
		return fmt.Errorf("vertex: %w", err)
	}
	if p.project == "" {
		return fmt.Errorf("vertex: missing VERTEX_PROJECT_ID / GOOGLE_CLOUD_PROJECT env")
	}
	return nil
}

// loadServiceAccount reads the service-account JSON from
// VERTEX_SERVICE_ACCOUNT_JSON (inline) or GOOGLE_APPLICATION_CREDENTIALS
// (path). Cached after first read.
func (p *VertexProvider) loadServiceAccount() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.saJSON) > 0 {
		return p.saJSON, nil
	}
	if inline := os.Getenv("VERTEX_SERVICE_ACCOUNT_JSON"); inline != "" {
		p.saJSON = []byte(inline)
		return p.saJSON, nil
	}
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read service account file: %w", err)
		}
		p.saJSON = data
		return p.saJSON, nil
	}
	return nil, fmt.Errorf("missing VERTEX_SERVICE_ACCOUNT_JSON or GOOGLE_APPLICATION_CREDENTIALS env")
}

// accessToken returns a cached OAuth2 access token, refreshing it when it is
// within 5 minutes of expiry. The token is obtained via a self-signed RS256
// JWT (jwt/v5, already a repo dependency) exchanged at the SA token_uri.
func (p *VertexProvider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.cachedToken != "" && time.Now().Add(5*time.Minute).Before(p.tokenExpiry) {
		tok := p.cachedToken
		p.mu.Unlock()
		return tok, nil
	}
	p.mu.Unlock()

	saBytes, err := p.loadServiceAccount()
	if err != nil {
		return "", err
	}
	var sa gcpServiceAccount
	if err := json.Unmarshal(saBytes, &sa); err != nil {
		return "", fmt.Errorf("parse service account json: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", fmt.Errorf("service account json missing client_email/private_key")
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return "", fmt.Errorf("parse sa private key: %w", err)
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    sa.ClientEmail,
		Subject:   sa.ClientEmail,
		Audience:  []string{tokenURI},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(1 * time.Hour)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = sa.PrivateKeyID
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign sa jwt: %w", err)
	}

	form := "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=" + signed
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.tokenClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b := ReadErrorBody(resp)
		return "", fmt.Errorf("token exchange status %d: %s", resp.StatusCode, string(b))
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&tokResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in token response")
	}
	expiry := time.Now()
	if tokResp.ExpiresIn > 0 {
		expiry = expiry.Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	} else {
		expiry = expiry.Add(55 * time.Minute)
	}

	p.mu.Lock()
	p.cachedToken = tokResp.AccessToken
	p.tokenExpiry = expiry
	p.mu.Unlock()
	slog.Info("vertex oauth2 token acquired", "expires_in", tokResp.ExpiresIn)
	return tokResp.AccessToken, nil
}

func (p *VertexProvider) rawPredictURL(model string) string {
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		p.baseURL, p.project, p.region, model)
}

func (p *VertexProvider) Messages(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal vertex messages request: %w", err)
	}
	resp, err := p.doRawPredict(ctx, req.Model, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, extractUpstreamError(resp)
	}
	var antResp AnthropicResponse
	if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&antResp); err != nil {
		return nil, fmt.Errorf("decode vertex messages response: %w", err)
	}
	return &antResp, nil
}

func (p *VertexProvider) StreamMessages(ctx context.Context, req *AnthropicRequest) (<-chan AnthropicStreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal vertex stream messages request: %w", err)
	}
	resp, err := p.doRawPredict(ctx, req.Model, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		errUp := extractUpstreamError(resp)
		resp.Body.Close()
		return nil, errUp
	}
	ch := make(chan AnthropicStreamEvent, 64)
	safego.Go("vertex_stream", func() {
		defer close(ch)
		defer resp.Body.Close()
		// RR8: ctx-watcher closes resp.Body on client cancel, unblocking the
		// body.Read inside parseAnthropicEventStreamRaw (which takes no ctx).
		// A stalled upstream keeps Read blocked indefinitely, hanging the
		// goroutine + connection. Closing the body forces an immediate read
		// error and a clean exit. Mirrors node_adapter.go.
		stopBodyWatch := make(chan struct{})
		defer close(stopBodyWatch)
		safego.Go("vertex_stream_cancel_watch", func() {
			select {
			case <-ctx.Done():
				slog.Debug("vertex stream canceled by client, closing body", "error", ctx.Err())
				resp.Body.Close()
			case <-stopBodyWatch:
			}
		})
		parseAnthropicEventStreamRaw(resp.Body, ch)
	})
	return ch, nil
}

func (p *VertexProvider) doRawPredict(ctx context.Context, model string, body []byte) (*http.Response, error) {
	tok, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.rawPredictURL(model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create vertex request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)
	InjectFusionHeaders(ctx, httpReq)
	return p.httpClient.Do(httpReq)
}

func (p *VertexProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	antReq := OpenAIToAnthropic(req)
	antReq.Stream = false
	resp, err := p.Messages(ctx, antReq)
	if err != nil {
		return nil, err
	}
	return AnthropicToOpenAI(resp), nil
}

func (p *VertexProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	antReq := OpenAIToAnthropic(req)
	antReq.Stream = true
	evCh, err := p.StreamMessages(ctx, antReq)
	if err != nil {
		return nil, err
	}
	ch := make(chan StreamChunk, 64)
	safego.Go("vertex_stream_relay", func() {
		defer close(ch)
		anthropicEventsToChunks(evCh, ch, req.Model)
	})
	return ch, nil
}

func (p *VertexProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, fmt.Errorf("vertex: embeddings not supported via this adapter")
}

func (p *VertexProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	return nil, fmt.Errorf("vertex: rerank not supported")
}

func (p *VertexProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "claude-3-5-sonnet@20241022", Object: "model", OwnedBy: "vertex"},
		{ID: "claude-3-5-haiku@20241022", Object: "model", OwnedBy: "vertex"},
		{ID: "claude-sonnet-4@20250514", Object: "model", OwnedBy: "vertex"},
	}, nil
}
