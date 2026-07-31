package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type FusionMLXProvider struct {
    baseURL          string
    apiKey           string
    httpClient       *http.Client
    inFlightCounter  atomic.Int64
    lastGCTime       atomic.Int64
    cfg              config.GCConfig
    routeHeader      string
    routeHeaderValue string
    modelSet         atomic.Value
    gcPending        atomic.Bool
}

func NewFusionMLXProvider(backendCfg config.BackendConfig, routingCfg config.RoutingConfig) *FusionMLXProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }

    return &FusionMLXProvider{
        baseURL:          backendCfg.BaseURL,
        apiKey:           backendCfg.APIKey,
        httpClient:       &http.Client{Timeout: timeout},
        cfg:              backendCfg.GC,
        routeHeader:      routingCfg.Negotiation.RouteHeader,
        routeHeaderValue: routingCfg.Negotiation.RouteHeaderValue,
    }
}

func (p *FusionMLXProvider) Name() string {
    return "fusion-mlx"
}

func (p *FusionMLXProvider) InFlight() int64 {
    return p.inFlightCounter.Load()
}

func (p *FusionMLXProvider) ModelSet() map[string]bool {
    v := p.modelSet.Load()
    if v == nil {
        return nil
    }
    return v.(map[string]bool)
}

func (p *FusionMLXProvider) RefreshModelSet(ctx context.Context) {
    models, err := p.ListModels(ctx)
    if err != nil {
        slog.Warn("failed to refresh model set", "error", err)
        return
    }
    m := make(map[string]bool, len(models))
    for _, model := range models {
        m[model.ID] = true
    }
    p.modelSet.Store(m)
    slog.Info("refreshed local model set", "count", len(m))
}

func (p *FusionMLXProvider) HealthCheck(ctx context.Context) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/health", nil)
    if err != nil {
        return fmt.Errorf("create health check request: %w", err)
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

func (p *FusionMLXProvider) ReadyCheck(ctx context.Context) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/readyz", nil)
    if err != nil {
        return fmt.Errorf("create ready check request: %w", err)
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("ready check failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("ready check returned status %d", resp.StatusCode)
    }

    return nil
}

func (p *FusionMLXProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    p.inFlightCounter.Add(1)
    defer p.inFlightCounter.Add(-1)

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
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("chat request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("chat request returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode chat response: %w", err)
    }

    return &chatResp, nil
}

func (p *FusionMLXProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    p.inFlightCounter.Add(1)

    body, err := json.Marshal(req)
    if err != nil {
        p.inFlightCounter.Add(-1)
        return nil, fmt.Errorf("marshal stream chat request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        p.inFlightCounter.Add(-1)
        return nil, fmt.Errorf("create stream chat request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        p.inFlightCounter.Add(-1)
        return nil, fmt.Errorf("stream chat request failed: %w", err)
    }

    if resp.StatusCode != http.StatusOK {
        p.inFlightCounter.Add(-1)
        respBody, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("stream chat returned status %d: %s", resp.StatusCode, string(respBody))
    }

    ch := make(chan StreamChunk, 64)

    go func() {
        defer close(ch)
        defer p.inFlightCounter.Add(-1)
        defer resp.Body.Close()

        p.parseSSEStream(resp.Body, ch)
    }()

    return ch, nil
}

func (p *FusionMLXProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    p.inFlightCounter.Add(1)
    defer p.inFlightCounter.Add(-1)

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
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

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

func (p *FusionMLXProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    p.inFlightCounter.Add(1)
    defer p.inFlightCounter.Add(-1)

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
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

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

func (p *FusionMLXProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
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

func (p *FusionMLXProvider) Cancel(requestID string) {
    for {
        current := p.inFlightCounter.Load()
        if current <= 0 {
            slog.Warn("cancel called but in-flight counter already zero", "request_id", requestID)
            return
        }
        if p.inFlightCounter.CompareAndSwap(current, current-1) {
            break
        }
    }

    // Optional: safe GC when in-flight reaches zero
    if p.inFlightCounter.Load() == 0 && p.cfg.Enabled {
        now := time.Now().Unix()
        lastGC := p.lastGCTime.Load()
        minInterval := int64(p.cfg.MinIdleSinceLastGC.Seconds())
        if minInterval == 0 {
            minInterval = 300 // default 5 minutes
        }
        if now-lastGC > minInterval {
            go p.SafeGC()
        }
    }
}

func (p *FusionMLXProvider) SafeGC() {
    if p.inFlightCounter.Load() != 0 {
        slog.Warn("skip gc: in-flight requests exist", "count", p.inFlightCounter.Load())
        return
    }

    if !p.gcPending.CompareAndSwap(true, false) {
        slog.Debug("skip gc: gcPending flag not set or already consumed")
        return
    }

    slog.Info("triggering safe gc on fusion-mlx")

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/v1/gc", nil)
    if err != nil {
        slog.Error("create gc request failed", "error", err)
        return
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        slog.Error("gc request failed", "error", err)
        return
    }
    defer resp.Body.Close()

    p.lastGCTime.Store(time.Now().Unix())

    if resp.StatusCode == http.StatusOK {
        slog.Info("gc completed successfully")
    } else {
        slog.Warn("gc returned non-ok status", "status", resp.StatusCode)
    }
}

func (p *FusionMLXProvider) TriggerGCWhenIdle() {
    if !p.gcPending.CompareAndSwap(false, true) {
        slog.Info("gc already queued, skipping")
        return
    }
    slog.Info("gc queued, will execute when in-flight reaches zero", "in_flight", p.inFlightCounter.Load())

    go func() {
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()
        timeout := time.After(5 * time.Minute)

        for {
            select {
            case <-ticker.C:
                if p.inFlightCounter.Load() == 0 {
                    p.SafeGC()
                    return
                }
            case <-timeout:
                slog.Warn("gc queue timed out after 5 minutes, clearing pending flag")
                p.gcPending.Store(false)
                return
            }
        }
    }()
}

func (p *FusionMLXProvider) StartIdleGCTimer(stopCh <-chan struct{}) {
    if !p.cfg.Enabled {
        return
    }

    idleThreshold := p.cfg.MinIdleSinceLastGC
    if idleThreshold == 0 {
        idleThreshold = 5 * time.Minute
    }

    checkInterval := idleThreshold / 2
    if checkInterval < 10*time.Second {
        checkInterval = 10 * time.Second
    }

    go func() {
        ticker := time.NewTicker(checkInterval)
        defer ticker.Stop()

        var idleSince time.Time

        for {
            select {
            case <-stopCh:
                slog.Info("idle gc timer stopped")
                return
            case <-ticker.C:
                if p.inFlightCounter.Load() == 0 {
                    if idleSince.IsZero() {
                        idleSince = time.Now()
                    } else if time.Since(idleSince) >= idleThreshold {
                        lastGC := time.Unix(p.lastGCTime.Load(), 0)
                        if time.Since(lastGC) >= idleThreshold {
                            slog.Info("idle gc timer triggered", "idle_duration", time.Since(idleSince))
                            p.SafeGC()
                        }
                        idleSince = time.Time{}
                    }
                } else {
                    idleSince = time.Time{}
                }
            }
        }
    }()
}

func (p *FusionMLXProvider) parseSSEStream(body io.Reader, ch chan<- StreamChunk) {
    decoder := json.NewDecoder(body)
    for {
        var chunk StreamChunk
        if err := decoder.Decode(&chunk); err != nil {
            if err != io.EOF {
                slog.Error("sse stream decode error", "error", err)
            }
            return
        }
        select {
        case ch <- chunk:
        default:
            slog.Warn("sse backpressure: channel full, degrading to non-streaming")
            degradedChunk := StreamChunk{Degraded: true}
            select {
            case ch <- degradedChunk:
            default:
            }
            return
        }
    }
}
