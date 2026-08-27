package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

type FusionKBProvider struct {
    name       string
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewFusionKBProvider(name string, backendCfg config.BackendConfig) *FusionKBProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &FusionKBProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout, Transport: TransportForBackend(backendCfg)},
    }
}

func (p *FusionKBProvider) Name() string { return p.name }

func (p *FusionKBProvider) HealthCheck(ctx context.Context) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    if err != nil {
        return fmt.Errorf("create health check request: %w", err)
    }
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("health check returned status %d", resp.StatusCode)
    }
    return nil
}

func (p *FusionKBProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal kb chat request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create kb chat request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("kb chat request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("kb chat returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode kb chat response: %w", err)
    }
    return &chatResp, nil
}

func (p *FusionKBProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal kb stream request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create kb stream request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("kb stream request failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("kb stream returned status %d: %s", resp.StatusCode, string(respBody))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("kb-stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        // RR8: ctx-watcher closes resp.Body on client cancel, unblocking the
        // body.Read inside parseSSEStream. A stalled upstream keeps Read
        // blocked — ctx.Done() is only checked on the send arm, so a stall
        // that never delivers a byte hangs the goroutine + connection. Closing
        // the body forces an immediate read error and a clean exit. Mirrors
        // node_adapter.go.
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        safego.Go("kb_stream_cancel_watch", func() {
            select {
            case <-ctx.Done():
                slog.Debug("fusion-kb stream canceled by client, closing body", "error", ctx.Err())
                resp.Body.Close()
            case <-stopBodyWatch:
            }
        })
        parseSSEStream(ctx, resp.Body, ch)
    })
    return ch, nil
}

func (p *FusionKBProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal kb embedding request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embeddings", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create kb embedding request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("kb embedding request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("kb embedding returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode kb embedding response: %w", err)
    }
    return &embResp, nil
}

func (p *FusionKBProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal kb rerank request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/rerank", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create kb rerank request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("kb rerank request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("kb rerank returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var rerankResp RerankResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&rerankResp); err != nil {
        return nil, fmt.Errorf("decode kb rerank response: %w", err)
    }
    return &rerankResp, nil
}

func (p *FusionKBProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    if err != nil {
        return nil, fmt.Errorf("create kb list models request: %w", err)
    }
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("kb list models failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("kb list models returned status %d", resp.StatusCode)
    }
    var listResp struct {
        Data []ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&listResp); err != nil {
        slog.Debug("kb list models decode failed", "error", err)
        return nil, nil
    }
    return listResp.Data, nil
}
