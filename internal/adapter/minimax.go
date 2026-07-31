// Callers: pool.go BuildProviders switch "minimax" case
// API: Provider interface (Name/HealthCheck/Chat/StreamChat/Embedding/Rerank/ListModels)
// Schema: MinimaxProvider struct{name,baseURL,apiKey,httpClient} - Bearer token auth, OpenAI-compatible
// User instruction: "这部分适配工作，要马上启动落地" — Minimax adapter
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

type MinimaxProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewMinimaxProvider(name string, backendCfg config.BackendConfig) *MinimaxProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &MinimaxProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *MinimaxProvider) Name() string { return p.name }

func (p *MinimaxProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("minimax health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("minimax health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *MinimaxProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("minimax chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("minimax chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode minimax response: %w", err)
    }
    return &chatResp, nil
}

func (p *MinimaxProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("minimax stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("minimax stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("minimax_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for {
            var chunk StreamChunk
            if err := dec.Decode(&chunk); err != nil {
                if err != io.EOF {
                    slog.Error("minimax sse error", "error", err)
                }
                return
            }
            select {
            case ch <- chunk:
            default:
                slog.Warn("minimax sse backpressure, dropping chunk")
                return
            }
        }
    })
    return ch, nil
}

func (p *MinimaxProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, fmt.Errorf("minimax: embedding not supported")
}

func (p *MinimaxProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("minimax: rerank not supported")
}

func (p *MinimaxProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "MiniMax-Text-01", Object: "model", OwnedBy: "minimax"},
        {ID: "abab6.5s-chat", Object: "model", OwnedBy: "minimax"},
        {ID: "abab6.5-chat", Object: "model", OwnedBy: "minimax"},
    }, nil
}

func (p *MinimaxProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
