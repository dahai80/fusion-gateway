// Callers: pool.go BuildProviders switch "zhipu" case
// API: Provider interface (Name/HealthCheck/Chat/StreamChat/Embedding/Rerank/ListModels)
// Schema: ZhipuProvider struct{name,baseURL,apiKey,httpClient} - Bearer token auth, OpenAI-compatible
// User instruction: "这部分适配工作，要马上启动落地" — 智谱 ChatGLM adapter
package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type ZhipuProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewZhipuProvider(name string, backendCfg config.BackendConfig) *ZhipuProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &ZhipuProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *ZhipuProvider) Name() string { return p.name }

func (p *ZhipuProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("zhipu health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("zhipu health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *ZhipuProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("zhipu chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("zhipu chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode zhipu response: %w", err)
    }
    return &chatResp, nil
}

func (p *ZhipuProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("zhipu stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("zhipu stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    go func() {
        defer close(ch)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for {
            var chunk StreamChunk
            if err := dec.Decode(&chunk); err != nil {
                if err != io.EOF {
                    slog.Error("zhipu sse error", "error", err)
                }
                return
            }
            select {
            case ch <- chunk:
            default:
                slog.Warn("zhipu sse backpressure, dropping chunk")
                return
            }
        }
    }()
    return ch, nil
}

func (p *ZhipuProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("zhipu embedding failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("zhipu embedding status %d: %s", resp.StatusCode, string(b))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode zhipu embedding: %w", err)
    }
    return &embResp, nil
}

func (p *ZhipuProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("zhipu: rerank not supported")
}

func (p *ZhipuProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "glm-4", Object: "model", OwnedBy: "zhipu"},
        {ID: "glm-4-flash", Object: "model", OwnedBy: "zhipu"},
        {ID: "glm-4-plus", Object: "model", OwnedBy: "zhipu"},
        {ID: "glm-4-long", Object: "model", OwnedBy: "zhipu"},
    }, nil
}

func (p *ZhipuProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
