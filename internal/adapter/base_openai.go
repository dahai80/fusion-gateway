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

// baseOpenAICompatible is the shared implementation for Bearer-auth
// OpenAI-compatible vendor shims (deepseek/moonshot/baichuan/dashscope/
// hunyuan/minimax/zhipu/qianfan/stepfun/yi). Each shim embeds this struct and
// inherits the identical stream-pump + ctx-watcher + error-body + transport
// behavior, declaring only its auth shape (when non-Bearer) and model list.
//
// EI7: prior to this base, every shim copy-pasted the StreamChat body. RR8
// (ctx-watcher) was added to openai_compatible.go + openrouter.go but never
// propagated to the 11 vendor shims, so a stalled upstream on any of them
// blocked body.Read indefinitely (ctx.Done is only checked on the send arm) —
// a live connection + slot leak. Embedding the base means RR8/RR11/RR9/B6
// horizontal fixes land once, for all shims, by construction.
//
// volcengine is NOT a bearer-auth OpenAI-compatible shim (HMAC-SHA256 signing)
// and is excluded from this base.
type baseOpenAICompatible struct {
    name             string
    baseURL          string
    apiKey           string
    httpClient       *http.Client
    streamHTTPClient *http.Client
}

func newBaseOpenAICompatible(name string, backendCfg config.BackendConfig) baseOpenAICompatible {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    // R3 (audit): dual-client — streamHTTPClient has no overall Timeout (caps
    // full body read, truncating long generation >120s) but keeps the capped
    // transport's ResponseHeaderTimeout so a dead upstream fails fast at TTFB.
    // Non-stream Chat/Embedding/Rerank stay on the bounded httpClient. Covers
    // all 11 vendor shims via the shared base. Mirrors openai_compatible.go.
    baseTransport := TransportForBackend(backendCfg)
    streamTransport := cloneStreamTransportForBackend(baseTransport, timeout, backendCfg.BaseURL)
    return baseOpenAICompatible{
        name:             name,
        baseURL:          backendCfg.BaseURL,
        apiKey:           backendCfg.APIKey,
        httpClient:       &http.Client{Timeout: timeout, Transport: baseTransport},
        streamHTTPClient: &http.Client{Timeout: 0, Transport: streamTransport},
    }
}

func (b *baseOpenAICompatible) baseName() string { return b.name }
func (b *baseOpenAICompatible) baseClient() *http.Client { return b.httpClient }

// setBearerAuth sets the standard Bearer Authorization header. Shims whose
// auth is plain Bearer (all 11 here) get this for free via baseChat/baseStream.
// A shim with a non-Bearer scheme overrides and passes its own setAuth into
// the base methods — none of the current 11 need that.
func (b *baseOpenAICompatible) setBearerAuth(req *http.Request) {
    if b.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+b.apiKey)
    }
}

func (b *baseOpenAICompatible) baseChat(ctx context.Context, req *ChatRequest, vendorLabel string, setAuth func(*http.Request)) (*ChatResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("%s marshal chat request: %w", vendorLabel, err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("%s create chat request: %w", vendorLabel, err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    resp, err := b.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("%s chat request failed: %w", vendorLabel, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("%s chat returned status %d: %s", vendorLabel, resp.StatusCode, string(respBody))
    }

    var chatResp ChatResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("%s decode chat response: %w", vendorLabel, err)
    }
    return &chatResp, nil
}

func (b *baseOpenAICompatible) baseStream(ctx context.Context, req *ChatRequest, vendorLabel string, setAuth func(*http.Request)) (<-chan StreamChunk, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("%s marshal stream chat request: %w", vendorLabel, err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("%s create stream chat request: %w", vendorLabel, err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    // R3: stream path uses the unbounded-timeout client.
    resp, err := b.streamHTTPClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("%s stream chat request failed: %w", vendorLabel, err)
    }

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("%s stream chat returned status %d: %s", vendorLabel, resp.StatusCode, string(respBody))
    }

    ch := make(chan StreamChunk, 64)
    safego.Go(vendorLabel+"_stream", func() {
        defer close(ch)
        defer resp.Body.Close()

        // RR8: ctx-watcher closes resp.Body on client cancel, unblocking the
        // body.Read inside parseSSEStream. Without this a quiet/stalled
        // upstream keeps Read blocked indefinitely — ctx.Done() is only
        // checked on the send arm (after a Read returns), so a stall that
        // never delivers a byte hangs the goroutine, leaks the connection,
        // and holds the slot until the watchdog. This was the live leak on
        // all 11 vendor shims before EI7 centralized the stream body here.
        // Mirrors openai_compatible.go.
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        safego.Go(vendorLabel+"_stream_cancel_watch", func() {
            select {
            case <-ctx.Done():
                slog.Debug(vendorLabel+" stream canceled by client, closing body", "error", ctx.Err())
                resp.Body.Close()
            case <-stopBodyWatch:
            }
        })

        parseSSEStream(ctx, resp.Body, ch)
    })

    return ch, nil
}

func (b *baseOpenAICompatible) baseEmbedding(ctx context.Context, req *EmbeddingRequest, vendorLabel string, setAuth func(*http.Request)) (*EmbeddingResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("%s marshal embedding request: %w", vendorLabel, err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/embeddings", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("%s create embedding request: %w", vendorLabel, err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    resp, err := b.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("%s embedding request failed: %w", vendorLabel, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("%s embedding returned status %d: %s", vendorLabel, resp.StatusCode, string(respBody))
    }

    var embResp EmbeddingResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("%s decode embedding response: %w", vendorLabel, err)
    }
    return &embResp, nil
}

func (b *baseOpenAICompatible) baseRerank(ctx context.Context, req *RerankRequest, vendorLabel string, setAuth func(*http.Request)) (*RerankResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("%s marshal rerank request: %w", vendorLabel, err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/rerank", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("%s create rerank request: %w", vendorLabel, err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    setAuth(httpReq)
    InjectFusionHeaders(ctx, httpReq)

    resp, err := b.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("%s rerank request failed: %w", vendorLabel, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("%s rerank returned status %d: %s", vendorLabel, resp.StatusCode, string(respBody))
    }

    var rerankResp RerankResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&rerankResp); err != nil {
        return nil, fmt.Errorf("%s decode rerank response: %w", vendorLabel, err)
    }
    return &rerankResp, nil
}

func (b *baseOpenAICompatible) baseHealthCheck(ctx context.Context, vendorLabel string, setAuth func(*http.Request)) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/models", nil)
    if err != nil {
        return fmt.Errorf("%s create health check request: %w", vendorLabel, err)
    }
    setAuth(req)

    resp, err := b.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("%s health check failed: %w", vendorLabel, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("%s health check returned status %d", vendorLabel, resp.StatusCode)
    }
    return nil
}

func (b *baseOpenAICompatible) baseListModels(ctx context.Context, vendorLabel string, setAuth func(*http.Request)) ([]ModelInfo, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/models", nil)
    if err != nil {
        return nil, fmt.Errorf("%s create list models request: %w", vendorLabel, err)
    }
    setAuth(req)

    resp, err := b.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("%s list models failed: %w", vendorLabel, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("%s list models returned status %d", vendorLabel, resp.StatusCode)
    }

    var listResp struct {
        Data []ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&listResp); err != nil {
        return nil, fmt.Errorf("%s decode models response: %w", vendorLabel, err)
    }
    return listResp.Data, nil
}
