// Callers: pool.go BuildProviders switch "hunyuan" case
// API: Provider interface (Name/HealthCheck/Chat/StreamChat/Embedding/Rerank/ListModels)
// Schema: HunyuanProvider struct{name,baseURL,apiKey,httpClient} - Bearer token auth, OpenAI-compatible
// User instruction: "这部分适配工作，要马上启动落地" — 混元 Hunyuan adapter
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

type HunyuanProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewHunyuanProvider(name string, backendCfg config.BackendConfig) *HunyuanProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &HunyuanProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *HunyuanProvider) Name() string { return p.name }

func (p *HunyuanProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("hunyuan health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("hunyuan health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *HunyuanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("hunyuan chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b := readErrorBody(resp)
        return nil, fmt.Errorf("hunyuan chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode hunyuan response: %w", err)
    }
    return &chatResp, nil
}

func (p *HunyuanProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("hunyuan stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b := readErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("hunyuan stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("hunyuan_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        parseSSEStream(ctx, resp.Body, ch)
    })
    return ch, nil
}

func (p *HunyuanProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("hunyuan embedding failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b := readErrorBody(resp)
        return nil, fmt.Errorf("hunyuan embedding status %d: %s", resp.StatusCode, string(b))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode hunyuan embedding: %w", err)
    }
    return &embResp, nil
}

func (p *HunyuanProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("hunyuan: rerank not supported")
}

func (p *HunyuanProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "hunyuan-lite", Object: "model", OwnedBy: "hunyuan"},
        {ID: "hunyuan-standard", Object: "model", OwnedBy: "hunyuan"},
        {ID: "hunyuan-pro", Object: "model", OwnedBy: "hunyuan"},
        {ID: "hunyuan-turbo", Object: "model", OwnedBy: "hunyuan"},
    }, nil
}

func (p *HunyuanProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
