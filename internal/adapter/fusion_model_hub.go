package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

type FusionModelHubProvider struct {
	name       string
	baseURL    *url.URL
	apiKey     string
	timeout    time.Duration
	proxy      *httputil.ReverseProxy
	backendCfg config.BackendConfig
}

func NewFusionModelHubProvider(name string, backendCfg config.BackendConfig) *FusionModelHubProvider {
	parsedURL, err := url.Parse(backendCfg.BaseURL)
	if err != nil {
		slog.Error("fusion-model-hub: invalid base_url", "name", name, "base_url", backendCfg.BaseURL, "error", err)
		parsedURL, _ = url.Parse("http://127.0.0.1:11444")
	}

	timeout := 30 * time.Second
	if backendCfg.Timeout > 0 {
		timeout = backendCfg.Timeout
	}

	p := &FusionModelHubProvider{
		name:       name,
		baseURL:    parsedURL,
		apiKey:     backendCfg.APIKey,
		timeout:    timeout,
		backendCfg: backendCfg,
	}

	p.proxy = &httputil.ReverseProxy{
		Director:      p.director,
		Transport:     &http.Transport{MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second},
		FlushInterval: -1,
		ErrorHandler:  p.errorHandler,
	}

	slog.Info("fusion-model-hub provider created", "name", name, "base_url", parsedURL.String(), "timeout", timeout)
	return p
}

func (p *FusionModelHubProvider) director(req *http.Request) {
	req.URL.Scheme = p.baseURL.Scheme
	req.URL.Host = p.baseURL.Host
	req.Host = p.baseURL.Host

	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "fusion-gateway/model-hub-proxy")
	}
}

func (p *FusionModelHubProvider) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("fusion-model-hub proxy error", "method", r.Method, "path", r.URL.Path, "error", err)
	http.Error(w, fmt.Sprintf("model-hub proxy error: %v", err), http.StatusBadGateway)
}

func (p *FusionModelHubProvider) ReverseProxy() *httputil.ReverseProxy {
	return p.proxy
}

func (p *FusionModelHubProvider) BaseURL() string {
	return p.baseURL.String()
}

func (p *FusionModelHubProvider) Name() string {
	return p.name
}

func (p *FusionModelHubProvider) HealthCheck(ctx context.Context) error {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL.String()+"/health", nil)
	if err != nil {
		return fmt.Errorf("model-hub health check: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("model-hub health check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model-hub health check: status %d", resp.StatusCode)
	}
	return nil
}

func (p *FusionModelHubProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return nil, fmt.Errorf("fusion-model-hub does not support Chat; use reverse proxy via /api/v1")
}

func (p *FusionModelHubProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	return nil, fmt.Errorf("fusion-model-hub does not support StreamChat; use reverse proxy via /api/v1")
}

func (p *FusionModelHubProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, fmt.Errorf("fusion-model-hub does not support Embedding; use reverse proxy via /api/v1")
}

func (p *FusionModelHubProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	return nil, fmt.Errorf("fusion-model-hub does not support Rerank; use reverse proxy via /api/v1")
}

func (p *FusionModelHubProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return nil, fmt.Errorf("fusion-model-hub does not support ListModels; use reverse proxy via /api/v1")
}
