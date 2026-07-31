// Callers: pool.go BuildProviders switch "yi" case
// API: Provider interface (Name/HealthCheck/Chat/StreamChat/Embedding/Rerank/ListModels)
// Schema: YiProvider struct{name,baseURL,apiKey,httpClient} - Bearer token auth, OpenAI-compatible
// User instruction: "这部分适配工作，要马上启动落地" — 零一万物 Yi adapter
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

type YiProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewYiProvider(name string, backendCfg config.BackendConfig) *YiProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &YiProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *YiProvider) Name() string { return p.name }

func (p *YiProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("yi health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("yi health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *YiProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("yi chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("yi chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode yi response: %w", err)
    }
    return &chatResp, nil
}

func (p *YiProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("yi stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("yi stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("yi_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for {
            var chunk StreamChunk
            if err := dec.Decode(&chunk); err != nil {
                if err != io.EOF {
                    slog.Error("yi sse error", "error", err)
                }
                return
            }
            select {
            case ch <- chunk:
            default:
                slog.Warn("yi sse backpressure, dropping chunk")
                return
            }
        }
    })
    return ch, nil
}

func (p *YiProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, fmt.Errorf("yi: embedding not supported")
}

func (p *YiProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("yi: rerank not supported")
}

func (p *YiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "yi-lightning", Object: "model", OwnedBy: "yi"},
        {ID: "yi-large", Object: "model", OwnedBy: "yi"},
        {ID: "yi-medium", Object: "model", OwnedBy: "yi"},
        {ID: "yi-spark", Object: "model", OwnedBy: "yi"},
    }, nil
}

func (p *YiProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
