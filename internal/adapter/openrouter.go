package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"

	"github.com/fusion-gateway/fusion-gateway/internal/safego"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type OpenRouterProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewOpenRouterProvider(name string, backendCfg config.BackendConfig) *OpenRouterProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 { timeout = 120 * time.Second }
    return &OpenRouterProvider{name: name, baseURL: backendCfg.BaseURL, apiKey: backendCfg.APIKey, httpClient: &http.Client{Timeout: timeout}}
}

func (p *OpenRouterProvider) Name() string { return p.name }

func (p *OpenRouterProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    p.setHeaders(req)
    resp, err := p.httpClient.Do(req)
    if err != nil { return fmt.Errorf("openrouter health check failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return fmt.Errorf("openrouter health check status %d", resp.StatusCode) }
    return nil
}

func (p *OpenRouterProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("openrouter chat failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("openrouter chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil { return nil, fmt.Errorf("decode openrouter response: %w", err) }
    return &chatResp, nil
}

func (p *OpenRouterProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("openrouter stream failed: %w", err) }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("openrouter stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("openrouter_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for {
            var chunk StreamChunk
            if err := dec.Decode(&chunk); err != nil {
                if err != io.EOF { slog.Error("openrouter sse error", "error", err) }
                return
            }
            select { case ch <- chunk: default: return }
        }
    })
    return ch, nil
}

func (p *OpenRouterProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embeddings", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("openrouter embedding failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("openrouter embedding status %d: %s", resp.StatusCode, string(b))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil { return nil, fmt.Errorf("decode openrouter embedding: %w", err) }
    return &embResp, nil
}

func (p *OpenRouterProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("openrouter: rerank not supported")
}

func (p *OpenRouterProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    p.setHeaders(req)
    resp, err := p.httpClient.Do(req)
    if err != nil { return nil, fmt.Errorf("openrouter list models failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return nil, fmt.Errorf("openrouter list models status %d", resp.StatusCode) }
    var lr struct { Data []ModelInfo `json:"data"` }
    if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil { return nil, fmt.Errorf("decode openrouter models: %w", err) }
    return lr.Data, nil
}

func (p *OpenRouterProvider) setHeaders(req *http.Request) {
    if p.apiKey != "" { req.Header.Set("Authorization", "Bearer "+p.apiKey) }
    req.Header.Set("HTTP-Referer", "https://fusion-gateway.dev")
    req.Header.Set("X-Title", "Fusion-Gateway")
}
