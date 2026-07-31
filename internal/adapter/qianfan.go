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

type QianfanProvider struct {
    name        string
    baseURL     string
    apiKey      string
    accessToken string
    httpClient  *http.Client
}

func NewQianfanProvider(name string, backendCfg config.BackendConfig) *QianfanProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 { timeout = 120 * time.Second }
    return &QianfanProvider{
        name: name, baseURL: backendCfg.BaseURL, apiKey: backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *QianfanProvider) Name() string { return p.name }
func (p *QianfanProvider) HealthCheck(ctx context.Context) error { return nil }

func (p *QianfanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("qianfan chat failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("qianfan chat status %d: %s", resp.StatusCode, string(b))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil { return nil, fmt.Errorf("decode qianfan response: %w", err) }
    return &chatResp, nil
}

func (p *QianfanProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("qianfan stream failed: %w", err) }
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("qianfan stream status %d: %s", resp.StatusCode, string(b))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("qianfan_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        dec := json.NewDecoder(resp.Body)
        for {
            var chunk StreamChunk
            if err := dec.Decode(&chunk); err != nil {
                if err != io.EOF { slog.Error("qianfan sse error", "error", err) }
                return
            }
            select { case ch <- chunk: default: return }
        }
    })
    return ch, nil
}

func (p *QianfanProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embeddings", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("qianfan embedding failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("qianfan embedding status %d: %s", resp.StatusCode, string(b))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil { return nil, fmt.Errorf("decode qianfan embedding: %w", err) }
    return &embResp, nil
}

func (p *QianfanProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("qianfan: rerank not supported")
}

func (p *QianfanProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil { return nil, fmt.Errorf("qianfan list models failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return nil, fmt.Errorf("qianfan list models status %d", resp.StatusCode) }
    var lr struct { Data []ModelInfo `json:"data"` }
    if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil { return nil, fmt.Errorf("decode qianfan models: %w", err) }
    return lr.Data, nil
}

func (p *QianfanProvider) setAuth(req *http.Request) {
    if p.accessToken != "" {
        req.Header.Set("Authorization", "Bearer "+p.accessToken)
    } else if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
}
