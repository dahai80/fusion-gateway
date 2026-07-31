// Callers: pool.go BuildProviders switch "stepfun" case
// API: Provider interface (Name/HealthCheck/Chat/StreamChat/Embedding/Rerank/ListModels)
// Schema: StepFunProvider struct{name,baseURL,apiKey,httpClient} - Bearer token auth, OpenAI-compatible
// User instruction: "这部分适配工作，要马上启动落地" — 阶跃星辰 StepFun adapter
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

type StepFunProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewStepFunProvider(name string, backendCfg config.BackendConfig) *StepFunProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &StepFunProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *StepFunProvider) Name() string { return p.name }

func (p *StepFunProvider) HealthCheck(ctx context.Context) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("stepfun health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("stepfun health check status %d", resp.StatusCode)
    }
    return nil
}

func (p *StepFunProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("stepfun chat failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("stepfun chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode stepfun response: %w", err)
    }
    return &chatResp, nil
}

func (p *StepFunProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("stepfun stream failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("stepfun stream status %d: %s", resp.StatusCode, string(b))
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
                    slog.Error("stepfun sse error", "error", err)
                }
                return
            }
            select {
            case ch <- chunk:
            default:
                slog.Warn("stepfun sse backpressure, dropping chunk")
                return
            }
        }
    }()
    return ch, nil
}

func (p *StepFunProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, fmt.Errorf("stepfun: embedding not supported")
}

func (p *StepFunProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("stepfun: rerank not supported")
}

func (p *StepFunProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "step-1-8k", Object: "model", OwnedBy: "stepfun"},
        {ID: "step-1-32k", Object: "model", OwnedBy: "stepfun"},
        {ID: "step-2-16k", Object: "model", OwnedBy: "stepfun"},
    }, nil
}

func (p *StepFunProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
