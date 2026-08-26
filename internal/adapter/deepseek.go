package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"

	"github.com/fusion-gateway/fusion-gateway/internal/safego"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type DeepSeekProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewDeepSeekProvider(name string, backendCfg config.BackendConfig) *DeepSeekProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 { timeout = 120 * time.Second }
    return &DeepSeekProvider{name: name, baseURL: backendCfg.BaseURL, apiKey: backendCfg.APIKey, httpClient: &http.Client{Timeout: timeout}}
}

func (p *DeepSeekProvider) Name() string { return p.name }

func (p *DeepSeekProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil { return fmt.Errorf("deepseek health check failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return fmt.Errorf("deepseek health check status %d", resp.StatusCode) }
    return nil
}

func (p *DeepSeekProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("deepseek chat failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b := ReadErrorBody(resp)
        return nil, fmt.Errorf("deepseek chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil { return nil, fmt.Errorf("decode deepseek response: %w", err) }
    return &chatResp, nil
}

func (p *DeepSeekProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("deepseek stream failed: %w", err) }
    if resp.StatusCode != http.StatusOK {
        b := ReadErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("deepseek stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("deepseek_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        parseSSEStream(ctx, resp.Body, ch)
    })
    return ch, nil
}

func (p *DeepSeekProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, fmt.Errorf("deepseek: embedding not supported via native adapter, use openai-compatible")
}

func (p *DeepSeekProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("deepseek: rerank not supported")
}

func (p *DeepSeekProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "deepseek-chat", Object: "model", OwnedBy: "deepseek"},
        {ID: "deepseek-reasoner", Object: "model", OwnedBy: "deepseek"},
    }, nil
}

func (p *DeepSeekProvider) setAuth(req *http.Request) {
    if p.apiKey != "" { req.Header.Set("Authorization", "Bearer "+p.apiKey) }
}
