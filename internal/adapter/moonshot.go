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

type MoonshotProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewMoonshotProvider(name string, backendCfg config.BackendConfig) *MoonshotProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &MoonshotProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *MoonshotProvider) Name() string { return p.name }

func (p *MoonshotProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("moonshot health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("moonshot health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *MoonshotProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("moonshot chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b := readErrorBody(resp)
        return nil, fmt.Errorf("moonshot chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode moonshot response: %w", err)
    }
    return &chatResp, nil
}

func (p *MoonshotProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("moonshot stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b := readErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("moonshot stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("moonshot_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        parseSSEStream(ctx, resp.Body, ch)
    })
    return ch, nil
}

func (p *MoonshotProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, fmt.Errorf("moonshot: embedding not supported")
}

func (p *MoonshotProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("moonshot: rerank not supported")
}

func (p *MoonshotProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "moonshot-v1-8k", Object: "model", OwnedBy: "moonshot"},
        {ID: "moonshot-v1-32k", Object: "model", OwnedBy: "moonshot"},
        {ID: "moonshot-v1-128k", Object: "model", OwnedBy: "moonshot"},
    }, nil
}

func (p *MoonshotProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
