package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "strings"

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
        httpClient: &http.Client{Timeout: timeout, Transport: TransportForBackend(backendCfg)},
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
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("chat request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("chat returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var chatResp ChatResponse
    // RR9 (audit P0): bound the success-path read. A misbehaving or MITM
    // backend returning a multi-GB 200 JSON drove the unbounded Decode to OOM
    // the gateway. 10 MiB cap via LimitResponseReader; truncation surfaces as a
    // decode error rather than silent OOM.
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&chatResp); err != nil {
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
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("stream chat request failed: %w", err)
    }

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("stream chat returned status %d: %s", resp.StatusCode, string(respBody))
    }

    ch := make(chan StreamChunk, 64)

    safego.Go("openai_compatible_stream", func() {
        defer close(ch)
        defer resp.Body.Close()

        // RR8: ctx-watcher closes resp.Body on client cancel, unblocking the
        // body.Read inside parseSSEStream. Without this, a quiet/stalled
        // upstream keeps Read blocked indefinitely — ctx.Done() is only
        // checked on the send arm (after a Read returns), so a stall that
        // never delivers a byte hangs the goroutine, leaks the connection,
        // and holds the slot until the 180s watchdog. Closing the body forces
        // an immediate read error and a clean exit. Mirrors node_adapter.go.
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        safego.Go("openai_compatible_stream_cancel_watch", func() {
            select {
            case <-ctx.Done():
                slog.Debug("openai compatible stream canceled by client, closing body", "error", ctx.Err())
                resp.Body.Close()
            case <-stopBodyWatch:
            }
        })

        parseSSEStream(ctx, resp.Body, ch)
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
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("embedding request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("embedding returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var embResp EmbeddingResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&embResp); err != nil {
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
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("rerank request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("rerank returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var rerankResp RerankResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&rerankResp); err != nil {
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
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&listResp); err != nil {
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
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("image request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("image request returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var imgResp ImageResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&imgResp); err != nil {
        return nil, fmt.Errorf("decode image response: %w", err)
    }
    return &imgResp, nil
}

func parseSSEStream(ctx context.Context, body io.Reader, ch chan<- StreamChunk) {
    // F1 fix: proper SSE line-by-line parsing — json.Decoder.Decode() fails on "data: " prefix
    buf := make([]byte, 4096)
    var lineBuf []byte
    const maxLineSize = 1 << 20 // 1 MiB cap per line to prevent unbounded growth
    for {
        n, err := body.Read(buf)
        if n > 0 {
            lineBuf = append(lineBuf, buf[:n]...)
            if len(lineBuf) > maxLineSize {
                // B6: returning (not lineBuf=nil) closes the channel — the
                // client observes truncation rather than the parser silently
                // dropping buffered bytes and resyncing mid-JSON. The old
                // lineBuf=nil kept reading from an arbitrary byte offset,
                // producing half-JSON "data:" lines forever with no resync
                // marker (SSE has no length framing).
                slog.Error("sse line exceeded max size, closing stream", "size", len(lineBuf), "max", maxLineSize)
                return
            }
        }
        for {
            idx := bytes.IndexByte(lineBuf, byte('\n'))
            if idx < 0 {
                break
            }
            line := string(bytes.TrimSpace(lineBuf[:idx]))
            lineBuf = lineBuf[idx+1:]
            if line == "" || strings.HasPrefix(line, ":") {
                continue
            }
            if !strings.HasPrefix(line, "data: ") {
                continue
            }
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" {
                return
            }
            var chunk StreamChunk
            if err := json.Unmarshal([]byte(data), &chunk); err != nil {
                slog.Warn("sse unmarshal error", "error", err, "data", data)
                continue
            }
            // F3 fix: block on send but bail on ctx cancel. The prior non-blocking
            // `default` arm fired whenever the 64-buffer filled — including when the
            // CONSUMER had stopped reading because the client canceled. It then emitted
            // a Degraded sentinel that the handler mistook for slow-upstream backpressure
            // and re-ran the full prompt non-streamed (double GPU load on fusion-mlx).
            // ctx.Done() makes cancel a silent stop, not a false backpressure signal.
            // Mirrors anthropic.go:411-416.
            select {
            case ch <- chunk:
            case <-ctx.Done():
                slog.Debug("sse stream send canceled by client", "error", ctx.Err())
                return
            }
        }
        if err != nil {
            if err != io.EOF {
                slog.Error("sse stream read error", "error", err)
            }
            return
        }
    }
}
