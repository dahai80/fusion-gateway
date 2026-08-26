package adapter

import (
    "bytes"
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"

	"github.com/fusion-gateway/fusion-gateway/internal/safego"
    "net/http"
    "net/url"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type VolcengineProvider struct {
    name       string
    baseURL    string
    apiKey     string
    accessKey  string
    secretKey  string
    httpClient *http.Client
}

func NewVolcengineProvider(name string, backendCfg config.BackendConfig) *VolcengineProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 { timeout = 120 * time.Second }
    return &VolcengineProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *VolcengineProvider) Name() string { return p.name }

func (p *VolcengineProvider) HealthCheck(ctx context.Context) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    if err != nil { return fmt.Errorf("create health check request: %w", err) }
    p.signRequest(req)
    resp, err := p.httpClient.Do(req)
    if err != nil { return fmt.Errorf("health check failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return fmt.Errorf("health check returned status %d", resp.StatusCode) }
    return nil
}

func (p *VolcengineProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    body, err := json.Marshal(req)
    if err != nil { return nil, fmt.Errorf("marshal chat request: %w", err) }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil { return nil, fmt.Errorf("create chat request: %w", err) }
    httpReq.Header.Set("Content-Type", "application/json")
    p.signRequest(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("chat request failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("volcengine chat returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil { return nil, fmt.Errorf("decode chat response: %w", err) }
    return &chatResp, nil
}

func (p *VolcengineProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    body, err := json.Marshal(req)
    if err != nil { return nil, fmt.Errorf("marshal stream chat request: %w", err) }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil { return nil, fmt.Errorf("create stream chat request: %w", err) }
    httpReq.Header.Set("Content-Type", "application/json")
    p.signRequest(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("volcengine stream request failed: %w", err) }
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("volcengine stream returned status %d: %s", resp.StatusCode, string(respBody))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("volcengine_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        parseSSEStream(ctx, resp.Body, ch)
    })
    return ch, nil
}

func (p *VolcengineProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    body, err := json.Marshal(req)
    if err != nil { return nil, fmt.Errorf("marshal embedding request: %w", err) }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/embeddings", bytes.NewReader(body))
    if err != nil { return nil, fmt.Errorf("create embedding request: %w", err) }
    httpReq.Header.Set("Content-Type", "application/json")
    p.signRequest(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil { return nil, fmt.Errorf("embedding request failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("volcengine embedding returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil { return nil, fmt.Errorf("decode embedding response: %w", err) }
    return &embResp, nil
}

func (p *VolcengineProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("volcengine: rerank not supported")
}

func (p *VolcengineProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
    if err != nil { return nil, fmt.Errorf("create list models request: %w", err) }
    p.signRequest(req)
    resp, err := p.httpClient.Do(req)
    if err != nil { return nil, fmt.Errorf("list models failed: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { return nil, fmt.Errorf("list models returned status %d", resp.StatusCode) }
    var listResp struct { Data []ModelInfo `json:"data"` }
    if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil { return nil, fmt.Errorf("decode models response: %w", err) }
    return listResp.Data, nil
}

func (p *VolcengineProvider) signRequest(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    if p.accessKey != "" && p.secretKey != "" {
        u, _ := url.Parse(p.baseURL + req.URL.Path)
        ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
        stringToSign := req.Method + "\n" + u.Path + "\n" + ts
        mac := hmac.New(sha256.New, []byte(p.secretKey))
        mac.Write([]byte(stringToSign))
        signature := hex.EncodeToString(mac.Sum(nil))
        req.Header.Set("X-Date", ts)
        req.Header.Set("Authorization", fmt.Sprintf("HMAC-SHA256 Credential=%s, Signature=%s", p.accessKey, signature))
    }
}
