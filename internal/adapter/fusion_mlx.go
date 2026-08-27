package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "net/http"
    "net/http/httputil"
    "net/url"
    "strings"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// ErrLocalSlotFull is returned by every fusion-mlx inference method
// (Chat/StreamChat/Embedding/Rerank) when the local concurrency hard cap
// (max_concurrent) is reached. It is the single source of truth for the
// RR4 hard slot cap: the counter check and increment happen as one atomic
// CAS in tryInFlightAcquire, closing the TOCTOU window the engine's P5
// advisory read-only check left open. Handlers catch this sentinel and
// divert the request to cloud instead of overflowing fusion-mlx's UMA/KV
// cache. A nil/zero max means no cap (legacy behavior), so this error is
// never returned in that mode.
var ErrLocalSlotFull = errors.New("local inference slot full: max_concurrent reached, divert to cloud")

type FusionMLXProvider struct {
    baseURL          string
    apiKey           string
    httpClient       *http.Client
    inFlightCounter  atomic.Int64
    maxConcurrent    int64
    lastGCTime       atomic.Int64
    cfg              config.GCConfig
    routeHeader      string
    routeHeaderValue string
    modelSet         atomic.Value
    gcPending        atomic.Bool
    reverseProxy     *httputil.ReverseProxy
    reverseProxyOnce atomic.Bool
}

func NewFusionMLXProvider(backendCfg config.BackendConfig, routingCfg config.RoutingConfig) *FusionMLXProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }

    // Trim a trailing slash so baseURL+"/v1/..." never produces a double slash.
    // The UDS convention uses the dummy host http://unix/ (trailing slash), and
    // every direct path join (Chat/StreamChat/ListModels/HealthCheck/...) would
    // otherwise build http://unix//v1/chat/completions, which uvicorn 404s.
    // ReverseProxy is unaffected (it url.Parses baseURL and only reads
    // scheme/host, leaving the request path intact).
    base := strings.TrimRight(backendCfg.BaseURL, "/")
    // RR4: pin the local concurrency hard cap on the provider so every
    // inference path enforces it atomically at Acquire time. The engine's
    // P5 check is advisory (reads under RLock, increments later) and left a
    // TOCTOU window; this is the authoritative gate. <=0 = no cap (legacy).
    maxConcurrent := int64(routingCfg.LocalPriority.MaxConcurrent)
    if maxConcurrent < 0 {
        maxConcurrent = 0
    }
    return &FusionMLXProvider{
        baseURL:          base,
        apiKey:           backendCfg.APIKey,
        httpClient:       &http.Client{Timeout: timeout, Transport: TransportForBackend(backendCfg)},
        maxConcurrent:    maxConcurrent,
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

// MaxConcurrent returns the local hard concurrency cap (RR4). 0 = uncapped.
func (p *FusionMLXProvider) MaxConcurrent() int64 {
    return p.maxConcurrent
}

// tryInFlightAcquire is the RR4 hard slot cap: the check (Load >= max) and
// the increment (Add(1)) happen atomically against the same counter, closing
// the TOCTOU window the engine's P5 advisory read-only check left open (N
// concurrent Decide calls each observed inFlight < max, all routed local,
// overshooting by N-1). Returns (release, true) on success or (nil, false)
// when the cap is reached — callers then return ErrLocalSlotFull so the
// handler diverts to cloud. max <= 0 means no cap (legacy behavior): always
// succeeds, mirroring the old inFlightAcquire. The CAS loop guards against
// a race where two goroutines pass the Load check simultaneously: each Add
// re-reads, and if the post-increment value exceeds max the acquirer backs
// out (Add(-1)) and retries, bounding overshoot to zero.
func (p *FusionMLXProvider) tryInFlightAcquire() (func(), bool) {
    if p.maxConcurrent <= 0 {
        p.inFlightCounter.Add(1)
        return func() { p.inFlightCounter.Add(-1) }, true
    }
    for {
        cur := p.inFlightCounter.Load()
        if cur >= p.maxConcurrent {
            return nil, false
        }
        // Increment-then-recheck: atomic Add returns the new value, so if
        // another goroutine incremented between our Load and Add, we detect
        // the overshoot and back out. This is a single counter, so the
        // Load+Add pair is race-free against itself.
        after := p.inFlightCounter.Add(1)
        if after > p.maxConcurrent {
            p.inFlightCounter.Add(-1)
            // Retry: a slot may free up, or confirm full on next Load.
            continue
        }
        return func() { p.inFlightCounter.Add(-1) }, true
    }
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

// MLXHealthDetail is the authoritative model-loaded state parsed from the
// fusion-mlx /health endpoint. fusion-mlx returns model_loaded/loaded_models
// here regardless of process liveness, so this is the signal downstream
// consumers (fusion-design check-mlx, fusion-studio) must rely on instead of
// a bare 200 (#59).
type MLXHealthDetail struct {
    ProcessAlive  bool
    ModelLoaded   bool
    LoadedModels  []string
    FetchError    error
}

// HealthDetail probes fusion-mlx /health and decodes the model_loaded/
// loaded_models fields. A non-200 or transport error sets ProcessAlive=false
// and returns the error in the struct (never a Go error return) so callers
// can render a structured health payload without a panic/recover dance.
func (p *FusionMLXProvider) HealthDetail(ctx context.Context) MLXHealthDetail {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/health", nil)
    if err != nil {
        slog.Warn("fusion-mlx health detail request build failed", "error", err)
        return MLXHealthDetail{FetchError: err}
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        slog.Warn("fusion-mlx health detail fetch failed", "error", err)
        return MLXHealthDetail{FetchError: err}
    }
    defer resp.Body.Close()

    detail := MLXHealthDetail{ProcessAlive: resp.StatusCode == http.StatusOK}
    if !detail.ProcessAlive {
        detail.FetchError = fmt.Errorf("fusion-mlx /health returned status %d", resp.StatusCode)
        slog.Warn("fusion-mlx process not healthy", "status", resp.StatusCode)
        return detail
    }

    var body struct {
        Status       string   `json:"status"`
        Ready        bool     `json:"ready"`
        ModelLoaded  bool     `json:"model_loaded"`
        LoadedModels []string `json:"loaded_models"`
    }
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&body); err != nil {
        slog.Warn("fusion-mlx health body decode failed", "error", err)
        detail.FetchError = err
        return detail
    }
    detail.ModelLoaded = body.ModelLoaded
    detail.LoadedModels = body.LoadedModels
    if detail.LoadedModels == nil {
        detail.LoadedModels = []string{}
    }
    slog.Debug("fusion-mlx health detail", "model_loaded", detail.ModelLoaded, "loaded_models", detail.LoadedModels)
    return detail
}

func (p *FusionMLXProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    release, ok := p.tryInFlightAcquire()
    if !ok {
        slog.Info("local slot full, chat diverting to cloud", "model", req.Model, "in_flight", p.InFlight(), "max", p.maxConcurrent)
        return nil, ErrLocalSlotFull
    }
    defer release()

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
    if p.routeHeader != "" {
        httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)
    }
    InjectFusionHeaders(ctx, httpReq)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("chat request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("chat request returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var chatResp ChatResponse
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode chat response: %w", err)
    }

    return &chatResp, nil
}

func (p *FusionMLXProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    // RR4: hard slot cap at the real Acquire point. If the local cap is
    // reached, return ErrLocalSlotFull before opening any connection so the
    // handler can divert to cloud (A4-style) instead of overflowing mlx.
    release, ok := p.tryInFlightAcquire()
    if !ok {
        slog.Info("local slot full, stream chat diverting to cloud", "model", req.Model, "in_flight", p.InFlight(), "max", p.maxConcurrent)
        return nil, ErrLocalSlotFull
    }
    defer func() {
        // release is nil after goroutine takes ownership
        if release != nil {
            release()
        }
    }()

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
    if p.routeHeader != "" {
        httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)
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
    goroutineRelease := release
    release = nil // goroutine takes ownership

    safego.Go("fusion_mlx_stream_chat", func() {
        defer close(ch)
        defer goroutineRelease()
        defer resp.Body.Close()

        // RR8: ctx-watcher closes resp.Body on client cancel, unblocking the
        // body.Read inside parseSSEStream. A stalled local engine keeps Read
        // blocked — ctx.Done() is only checked on the send arm (after a Read
        // returns), so a stall that never delivers a byte hangs the goroutine,
        // leaks the connection, and holds the local slot. Closing the body
        // forces an immediate read error and a clean exit + slot release.
        // Mirrors node_adapter.go.
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        safego.Go("fusion_mlx_stream_chat_cancel_watch", func() {
            select {
            case <-ctx.Done():
                slog.Debug("fusion-mlx stream chat canceled by client, closing body", "error", ctx.Err())
                resp.Body.Close()
            case <-stopBodyWatch:
            }
        })

        parseSSEStream(ctx, resp.Body, ch)
    })

    return ch, nil
}

func (p *FusionMLXProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    release, ok := p.tryInFlightAcquire()
    if !ok {
        slog.Info("local slot full, embedding diverting to cloud", "model", req.Model, "in_flight", p.InFlight(), "max", p.maxConcurrent)
        return nil, ErrLocalSlotFull
    }
    defer release()

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
    if p.routeHeader != "" {
        httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)
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
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode embedding response: %w", err)
    }

    return &embResp, nil
}

func (p *FusionMLXProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    release, ok := p.tryInFlightAcquire()
    if !ok {
        slog.Info("local slot full, rerank diverting to cloud", "model", req.Model, "in_flight", p.InFlight(), "max", p.maxConcurrent)
        return nil, ErrLocalSlotFull
    }
    defer release()

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
    if p.routeHeader != "" {
        httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)
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
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&rerankResp); err != nil {
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
    if p.routeHeader != "" {
        req.Header.Set(p.routeHeader, p.routeHeaderValue)
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("list models failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := ReadErrorBody(resp)
        return nil, fmt.Errorf("list models returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
    }

    var listResp struct {
        Data []ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(LimitResponseReader(resp.Body)).Decode(&listResp); err != nil {
        return nil, fmt.Errorf("decode models response: %w", err)
    }

    return listResp.Data, nil
}

// ReverseProxy returns a cached httputil.ReverseProxy that forwards arbitrary
// paths to fusion-mlx verbatim (path/query/body/SSE preserved), injecting the
// gateway's Authorization + X-Fusion-Route credentials so fusion-mlx's
// route_guard admits the request (#30: admin fine-tune API proxy). Built
// lazily once; the Director only rewrites scheme/host, leaving the path intact
// so /admin/api/fine-tune/* maps 1:1 to the backend.
func (p *FusionMLXProvider) ReverseProxy() *httputil.ReverseProxy {
    if p.reverseProxyOnce.CompareAndSwap(false, true) {
        parsedURL, err := url.Parse(p.baseURL)
        if err != nil {
            slog.Error("fusion-mlx reverse proxy: invalid base_url, falling back to 127.0.0.1:11434", "base_url", p.baseURL, "error", err)
            parsedURL, _ = url.Parse("http://127.0.0.1:11434")
        }
        p.reverseProxy = &httputil.ReverseProxy{
            Director:      p.proxyDirector(parsedURL),
            Transport:     p.httpClient.Transport,
            FlushInterval: -1,
            ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
                slog.Error("fusion-mlx reverse proxy error", "method", r.Method, "path", r.URL.Path, "error", err)
                http.Error(w, fmt.Sprintf("fusion-mlx proxy error: %v", err), http.StatusBadGateway)
            },
        }
        slog.Info("fusion-mlx reverse proxy ready", "base_url", parsedURL.String())
    }
    return p.reverseProxy
}

// proxyDirector returns a Director that rewrites scheme/host to the backend and
// injects the gateway credentials. The request path is untouched.
func (p *FusionMLXProvider) proxyDirector(backend *url.URL) func(*http.Request) {
    return func(req *http.Request) {
        req.URL.Scheme = backend.Scheme
        req.URL.Host = backend.Host
        req.Host = backend.Host

        if p.apiKey != "" {
            req.Header.Set("Authorization", "Bearer "+p.apiKey)
        }
        if p.routeHeader != "" {
            req.Header.Set(p.routeHeader, p.routeHeaderValue)
        }
        if _, ok := req.Header["User-Agent"]; !ok {
            req.Header.Set("User-Agent", "fusion-gateway/mlx-proxy")
        }
    }
}

// Cancel signals a stream cancellation. It MUST NOT touch the in-flight
// counter: StreamChat hands the single release to its forward goroutine via
// `defer goroutineRelease()`, and ctx-cancel propagates to resp.Body (the
// request is built with NewRequestWithContext), so the goroutine exits and
// releases the slot on its own. A manual decrement here would double-release
// — the counter underflows to negative, silently bypassing the P5
// max_concurrent gate (negative < max → never limited) on every subsequent
// cancel (#97/#102 slot-leak root cause). Idle GC after cancel is already
// covered by StartIdleGCTimer, so this is a no-op signal for traceability.
func (p *FusionMLXProvider) Cancel(requestID string) {
    slog.Info("fusion-mlx stream cancel signaled", "request_id", requestID, "in_flight", p.inFlightCounter.Load())
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

    safego.Go("fusion_mlx_gc_queue", func() {
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
    })
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

    safego.Go("fusion_mlx_idle_gc", func() {
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
    })
}
