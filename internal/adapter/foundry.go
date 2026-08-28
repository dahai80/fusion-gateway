package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// FoundryProvider forwards Anthropic Messages requests to an Azure-hosted
// Anthropic endpoint (Azure AI Foundry / Anthropic on Azure, issue #40).
// Auth is either an Azure API key (AZURE_API_KEY / AZURE_OPENAI_API_KEY →
// "api-key" header) or an Entra access token (AZURE_ACCESS_TOKEN →
// "Authorization: Bearer"). Credentials are read from the gateway-side
// environment only, never echoed to fusion-code.
//
// The Anthropic-on-Azure endpoint accepts the Anthropic Messages payload at
// {base_url}/v1/messages and returns native Anthropic responses/SSE, so this
// adapter is a thin auth+forward shim with no body transformation.
type FoundryProvider struct {
	name             string
	baseURL          string
	httpClient       *http.Client
	streamHTTPClient *http.Client
	apiKey           string
	accessToken      string
}

func NewFoundryProvider(name string, backendCfg config.BackendConfig) *FoundryProvider {
	apiKey := backendCfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("AZURE_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("AZURE_OPENAI_API_KEY")
	}
	accessToken := os.Getenv("AZURE_ACCESS_TOKEN")
	timeout := backendCfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	// R3 (audit): dual-client — streamHTTPClient unbounded so long generation
	// >120s is not truncated; keeps capped transport ResponseHeaderTimeout so
	// a dead upstream fails fast at TTFB. Non-stream Messages stays bounded.
	// Mirrors openai_compatible.go.
	baseTransport := TransportForBackend(backendCfg)
	streamTransport := cloneStreamTransportForBackend(baseTransport, timeout, backendCfg.BaseURL)
	return &FoundryProvider{
		name:             name,
		baseURL:          backendCfg.BaseURL,
		httpClient:       &http.Client{Timeout: timeout, Transport: baseTransport},
		streamHTTPClient: &http.Client{Timeout: 0, Transport: streamTransport},
		apiKey:           apiKey,
		accessToken:      accessToken,
	}
}

func (p *FoundryProvider) Name() string { return p.name }

func (p *FoundryProvider) HealthCheck(ctx context.Context) error {
	if p.apiKey == "" && p.accessToken == "" {
		return fmt.Errorf("foundry: missing AZURE_API_KEY or AZURE_ACCESS_TOKEN env")
	}
	if p.baseURL == "" {
		return fmt.Errorf("foundry: base_url not configured")
	}
	return nil
}

func (p *FoundryProvider) setAuth(req *http.Request) {
	if p.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.accessToken)
		return
	}
	if p.apiKey != "" {
		req.Header.Set("api-key", p.apiKey)
	}
}

func (p *FoundryProvider) messagesURL() string {
	return p.baseURL + "/v1/messages"
}

func (p *FoundryProvider) Messages(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal foundry messages request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messagesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create foundry messages request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	p.setAuth(httpReq)
	InjectFusionHeaders(ctx, httpReq)
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("foundry messages request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, extractUpstreamError(resp)
	}
	var antResp AnthropicResponse
	if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&antResp); err != nil {
		return nil, fmt.Errorf("decode foundry messages response: %w", err)
	}
	return &antResp, nil
}

func (p *FoundryProvider) StreamMessages(ctx context.Context, req *AnthropicRequest) (<-chan AnthropicStreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal foundry stream messages request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messagesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create foundry stream messages request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	p.setAuth(httpReq)
	InjectFusionHeaders(ctx, httpReq)
	// R3: stream path uses the unbounded-timeout client.
	resp, err := p.streamHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("foundry stream messages failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errUp := extractUpstreamError(resp)
		resp.Body.Close()
		return nil, errUp
	}
	ch := make(chan AnthropicStreamEvent, 64)
	safego.Go("foundry_stream", func() {
		defer close(ch)
		defer resp.Body.Close()
		// RR8: ctx-watcher closes resp.Body on client cancel, unblocking the
		// body.Read inside parseAnthropicEventStreamRaw (which takes no ctx).
		// A stalled upstream keeps Read blocked indefinitely, hanging the
		// goroutine + connection. Closing the body forces an immediate read
		// error and a clean exit. Mirrors node_adapter.go.
		stopBodyWatch := make(chan struct{})
		defer close(stopBodyWatch)
		safego.Go("foundry_stream_cancel_watch", func() {
			select {
			case <-ctx.Done():
				slog.Debug("foundry stream canceled by client, closing body", "error", ctx.Err())
				resp.Body.Close()
			case <-stopBodyWatch:
			}
		})
		parseAnthropicEventStreamRaw(resp.Body, ch)
	})
	return ch, nil
}

func (p *FoundryProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	antReq := OpenAIToAnthropic(req)
	antReq.Stream = false
	resp, err := p.Messages(ctx, antReq)
	if err != nil {
		return nil, err
	}
	return AnthropicToOpenAI(resp), nil
}

func (p *FoundryProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	antReq := OpenAIToAnthropic(req)
	antReq.Stream = true
	evCh, err := p.StreamMessages(ctx, antReq)
	if err != nil {
		return nil, err
	}
	ch := make(chan StreamChunk, 64)
	safego.Go("foundry_stream_relay", func() {
		defer close(ch)
		anthropicEventsToChunks(evCh, ch, req.Model)
	})
	return ch, nil
}

func (p *FoundryProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, fmt.Errorf("foundry: embeddings not supported")
}

func (p *FoundryProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	return nil, fmt.Errorf("foundry: rerank not supported")
}

func (p *FoundryProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "claude-3-5-sonnet", Object: "model", OwnedBy: "azure-foundry"},
		{ID: "claude-3-5-haiku", Object: "model", OwnedBy: "azure-foundry"},
		{ID: "claude-sonnet-4", Object: "model", OwnedBy: "azure-foundry"},
	}, nil
}
