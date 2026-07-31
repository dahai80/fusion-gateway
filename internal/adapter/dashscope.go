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

type DashScopeProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewDashScopeProvider(name string, backendCfg config.BackendConfig) *DashScopeProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &DashScopeProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *DashScopeProvider) Name() string { return p.name }

func (p *DashScopeProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("dashscope health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("dashscope health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *DashScopeProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("dashscope chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("dashscope chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode dashscope response: %w", err)
    }
    return &chatResp, nil
}

func (p *DashScopeProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("dashscope stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("dashscope stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("dashscope_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for {
            var chunk StreamChunk
            if err := dec.Decode(&chunk); err != nil {
                if err != io.EOF {
                    slog.Error("dashscope sse error", "error", err)
                }
                return
            }
            select {
            case ch <- chunk:
            default:
                slog.Warn("dashscope sse backpressure, dropping chunk")
                return
            }
        }
    })
    return ch, nil
}

func (p *DashScopeProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("dashscope embedding failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("dashscope embedding status %d: %s", resp.StatusCode, string(b))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode dashscope embedding: %w", err)
    }
    return &embResp, nil
}

func (p *DashScopeProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("dashscope: rerank not supported")
}

func (p *DashScopeProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "qwen-turbo", Object: "model", OwnedBy: "dashscope"},
        {ID: "qwen-plus", Object: "model", OwnedBy: "dashscope"},
        {ID: "qwen-max", Object: "model", OwnedBy: "dashscope"},
        {ID: "qwen-long", Object: "model", OwnedBy: "dashscope"},
    }, nil
}

func (p *DashScopeProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
