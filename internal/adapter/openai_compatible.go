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

type OpenAICompatibleProvider struct {
    name      string
    baseURL   string
    apiKey    string
    httpClient *http.Client
}

func NewOpenAICompatibleProvider(name string, backendCfg config.BackendConfig) *OpenAICompatibleProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }

    return &OpenAICompatibleProvider{
        name:      name,
        baseURL:   backendCfg.BaseURL,
        apiKey:    backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *OpenAICompatibleProvider) Name() string {
    return p.name
}

func (p *OpenAICompatibleProvider) HealthCheck(ctx context.Context) error {
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

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal chat request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create chat request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("chat request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("chat returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode chat response: %w", err)
    }

    return &chatResp, nil
}

func (p *OpenAICompatibleProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal stream chat request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create stream chat request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("stream chat request failed: %w", err)
    }

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("stream chat returned status %d: %s", resp.StatusCode, string(respBody))
    }

    ch := make(chan StreamChunk, 64)

    safego.Go("openai_compatible_stream", func() {
        defer close(ch)
        defer resp.Body.Close()

        p.parseSSEStream(resp.Body, ch)
    })

    return ch, nil
}

func (p *OpenAICompatibleProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal embedding request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embeddings", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create embedding request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("embedding request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("embedding returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode embedding response: %w", err)
    }

    return &embResp, nil
}

func (p *OpenAICompatibleProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal rerank request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/rerank", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create rerank request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("rerank request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("rerank returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var rerankResp RerankResponse
    if err := json.NewDecoder(resp.Body).Decode(&rerankResp); err != nil {
        return nil, fmt.Errorf("decode rerank response: %w", err)
    }

    return &rerankResp, nil
}

func (p *OpenAICompatibleProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    if err != nil {
        return nil, fmt.Errorf("create list models request: %w", err)
    }

    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("list models failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("list models returned status %d", resp.StatusCode)
    }

    var listResp struct {
        Data []ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
        return nil, fmt.Errorf("decode models response: %w", err)
    }

    return listResp.Data, nil
}

func (p *OpenAICompatibleProvider) Images(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal image request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/images/generations", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create image request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("image request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("image request returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var imgResp ImageResponse
    if err := json.NewDecoder(resp.Body).Decode(&imgResp); err != nil {
        return nil, fmt.Errorf("decode image response: %w", err)
    }
    return &imgResp, nil
}

func (p *OpenAICompatibleProvider) parseSSEStream(body io.Reader, ch chan<- StreamChunk) {
    decoder := json.NewDecoder(body)
    for {
        var chunk StreamChunk
        if err := decoder.Decode(&chunk); err != nil {
            if err != io.EOF {
                slog.Error("sse stream decode error", "provider", p.name, "error", err)
            }
            return
        }
        select {
        case ch <- chunk:
        default:
            slog.Warn("sse backpressure: channel full, degrading to non-streaming", "provider", p.name)
            degradedChunk := StreamChunk{Degraded: true}
            select {
            case ch <- degradedChunk:
            default:
            }
            return
        }
    }
}
