package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "net/url"
    "os"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// AzureOpenAIProvider (#324): Azure OpenAI chat/completions under the
// deployment calling convention — the requested model maps to a deployment
// name and the URL becomes
// {base_url}/openai/deployments/{deployment}/chat/completions?api-version=X.
// Auth is Azure's "api-key" header (never Bearer). The effective
// model_version (Azure reports the underlying model behind the deployment in
// the response "model" field) is surfaced on ModelVersion so downstream
// audit records (trace_id, prompt_version, model_version) work.
//
// Deployment mapping: backends.<name>.deployments is an optional
// requested-model -> deployment-name map. Unmapped models use the request
// model id verbatim as the deployment name, so a 1:1 alias needs no config.
type AzureOpenAIProvider struct {
    name             string
    baseURL          string
    apiKey           string
    apiVersion       string
    deployments      map[string]string
    models           []string
    httpClient       *http.Client
    streamHTTPClient *http.Client
}

func NewAzureOpenAIProvider(name string, backendCfg config.BackendConfig) *AzureOpenAIProvider {
    apiKey := backendCfg.APIKey
    if apiKey == "" {
        apiKey = os.Getenv("AZURE_OPENAI_API_KEY")
    }
    apiVersion := backendCfg.APIVersion
    if apiVersion == "" {
        apiVersion = "2024-10-21"
    }
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    // R3 dual-client: streamHTTPClient unbounded so long generation >120s is
    // not truncated; keeps the capped transport ResponseHeaderTimeout. Mirrors
    // base_openai.go / foundry.go.
    baseTransport := TransportForBackend(backendCfg)
    streamTransport := cloneStreamTransportForBackend(baseTransport, timeout, backendCfg.BaseURL)
    slog.Info("building azure-openai provider", "name", name, "base_url", backendCfg.BaseURL, "api_version", apiVersion, "deployments", len(backendCfg.Deployments))
    return &AzureOpenAIProvider{
        name:             name,
        baseURL:          strings.TrimRight(backendCfg.BaseURL, "/"),
        apiKey:           apiKey,
        apiVersion:       apiVersion,
        deployments:      backendCfg.Deployments,
        models:           backendCfg.Models,
        httpClient:       &http.Client{Timeout: timeout, Transport: baseTransport},
        streamHTTPClient: &http.Client{Timeout: 0, Transport: streamTransport},
    }
}

func (p *AzureOpenAIProvider) Name() string { return p.name }

// deploymentFor resolves the requested model to an Azure deployment name.
// Explicit deployments map wins; otherwise the model id is the deployment
// name (Azure convention for 1:1 aliases).
func (p *AzureOpenAIProvider) deploymentFor(model string) string {
    if d, ok := p.deployments[model]; ok && d != "" {
        return d
    }
    return model
}

func (p *AzureOpenAIProvider) setAuth(req *http.Request) {
    if p.apiKey != "" {
        req.Header.Set("api-key", p.apiKey)
    }
}

func (p *AzureOpenAIProvider) deploymentURL(deployment, path string) string {
    u := p.baseURL + "/openai/deployments/" + url.PathEscape(deployment) + path
    sep := "?"
    if strings.Contains(u, "?") {
        sep = "&"
    }
    return u + sep + "api-version=" + url.QueryEscape(p.apiVersion)
}

func (p *AzureOpenAIProvider) HealthCheck(ctx context.Context) error {
    if p.apiKey == "" {
        return fmt.Errorf("azure-openai: missing api_key / AZURE_OPENAI_API_KEY")
    }
    if p.baseURL == "" {
        return fmt.Errorf("azure-openai: base_url not configured")
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/openai/models?api-version="+url.QueryEscape(p.apiVersion), nil)
    if err != nil {
        return fmt.Errorf("azure-openai: create health check request: %w", err)
    }
    p.setAuth(req)
    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("azure-openai: health check failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("azure-openai: health check returned status %d", resp.StatusCode)
    }
    return nil
}

func (p *AzureOpenAIProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    deployment := p.deploymentFor(req.Model)
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("azure-openai marshal chat request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.deploymentURL(deployment, "/chat/completions"), bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("azure-openai create chat request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("azure-openai chat request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("azure-openai chat returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var chatResp ChatResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("azure-openai decode chat response: %w", err)
    }
    // #324: Azure's "model" field reports the underlying model version behind
    // the deployment. Surface it as model_version; keep the requested model as
    // "model" so clients see what they asked for.
    chatResp.ModelVersion = chatResp.Model
    chatResp.Model = req.Model
    slog.Debug("azure-openai chat completed", "deployment", deployment, "requested_model", req.Model, "model_version", chatResp.ModelVersion)
    return &chatResp, nil
}

func (p *AzureOpenAIProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    deployment := p.deploymentFor(req.Model)
    req.Stream = true
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("azure-openai marshal stream chat request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.deploymentURL(deployment, "/chat/completions"), bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("azure-openai create stream chat request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.streamHTTPClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("azure-openai stream chat request failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("azure-openai stream chat returned status %d: %s", resp.StatusCode, string(respBody))
    }

    inner := make(chan StreamChunk, 64)
    ch := make(chan StreamChunk, 64)
    safego.Go("azure_openai_stream", func() {
        defer close(inner)
        defer resp.Body.Close()
        // RR8: ctx-watcher closes resp.Body on client cancel so a stalled
        // upstream cannot block body.Read indefinitely. Mirrors foundry.go.
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        safego.Go("azure_openai_stream_cancel_watch", func() {
            select {
            case <-ctx.Done():
                slog.Debug("azure-openai stream canceled by client, closing body", "error", ctx.Err())
                resp.Body.Close()
            case <-stopBodyWatch:
            }
        })
        parseSSEStream(ctx, resp.Body, inner)
    })
    // #324: relay stamping model_version (the effective Azure model version
    // carried on the upstream chunks' "model" field) onto every chunk.
    safego.Go("azure_openai_stream_relay", func() {
        defer close(ch)
        var modelVersion string
        for chunk := range inner {
            if modelVersion == "" && chunk.Model != "" && chunk.Model != req.Model {
                modelVersion = chunk.Model
                slog.Debug("azure-openai stream model_version", "deployment", deployment, "model_version", modelVersion)
            }
            chunk.ModelVersion = modelVersion
            if chunk.Model != "" {
                chunk.Model = req.Model
            }
            select {
            case ch <- chunk:
            case <-ctx.Done():
                return
            }
        }
    })
    return ch, nil
}

func (p *AzureOpenAIProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    deployment := p.deploymentFor(req.Model)
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("azure-openai marshal embedding request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.deploymentURL(deployment, "/embeddings"), bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("azure-openai create embedding request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    p.setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("azure-openai embedding request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("azure-openai embedding returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var embResp EmbeddingResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("azure-openai decode embedding response: %w", err)
    }
    return &embResp, nil
}

func (p *AzureOpenAIProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("azure-openai: rerank not supported")
}

func (p *AzureOpenAIProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    models := make([]ModelInfo, 0, len(p.models))
    for _, m := range p.models {
        models = append(models, ModelInfo{ID: m, Object: "model", OwnedBy: "azure-openai"})
    }
    return models, nil
}
