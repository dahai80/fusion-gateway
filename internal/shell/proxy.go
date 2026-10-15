package shell

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "net/http/httputil"
    "net/url"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

// upstreamFn resolves a module id to its proxy upstream base URL ("_" when
// the module has no proxy mapping). A function — not a static map — so a
// config hot-reload takes effect without rebuilding the proxy.
type upstreamFn func(moduleID string) string

// Proxy forwards /shell/<module_id>/... requests to the module's upstream
// and degrades failures into the standardized error envelope (PRD §7.2):
// backend down is a 503 envelope with retryable=true, NEVER a bare 502 or a
// hung connection. The body cap (E5 lesson) bounds proxied request bodies.
type Proxy struct {
    resolveUpstream upstreamFn
    transports      sync.Map // module id -> *http.Client (built once per module)
    bodyCap         int64
}

func NewProxy(resolveUpstream upstreamFn) *Proxy {
    return &Proxy{resolveUpstream: resolveUpstream, bodyCap: 256 << 20}
}

// moduleClient builds (once) the HTTP client for one module upstream. Goes
// through httpx.TransportForBackend (RR11/RR5 lessons): MaxConnsPerHost cap
// + ResponseHeaderTimeout + dial timeout — never a bare &http.Client{}.
// Module upstreams are loopback in the union-web topology, so the local
// predicate applies and the header timeout stretches to 300s (mirrors #208).
func (p *Proxy) moduleClient(moduleID, upstream string) (*http.Client, error) {
    if c, ok := p.transports.Load(moduleID); ok {
        return c.(*http.Client), nil
    }
    u, err := url.Parse(upstream)
    if err != nil || u.Scheme == "" || u.Host == "" {
        return nil, fmt.Errorf("module %s upstream %q is not a valid absolute URL", moduleID, upstream)
    }
    bc := config.BackendConfig{Type: "openai-compatible", BaseURL: upstream}
    client := &http.Client{Transport: httpx.TransportForBackend(bc)}
    actual, loaded := p.transports.LoadOrStore(moduleID, client)
    if loaded {
        // Another goroutine won the race; ours is discarded (same config).
        transport, ok := client.Transport.(interface{ CloseIdleConnections() })
        if ok {
            transport.CloseIdleConnections()
        }
    }
    return actual.(*http.Client), nil
}

// ServeHTTP handles one /shell/<module_id>/... request. Auth (session token)
// is enforced by the server-side wrap before reaching here.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    moduleID, rest, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/shell/"), "/")
    if moduleID == "" {
        WriteEnvelope(w, http.StatusNotFound, "module_not_found", "missing module id in /shell/ path", "", false)
        return
    }
    if !found && rest == "" {
        // /shell/<id> exactly — treat as module root.
        rest = ""
    }

    upstream := p.resolveUpstream(moduleID)
    if upstream == "" || upstream == "_" {
        WriteEnvelope(w, http.StatusNotFound, "module_not_found",
            fmt.Sprintf("module %q has no proxy upstream configured", moduleID), moduleID, false)
        return
    }

    client, err := p.moduleClient(moduleID, upstream)
    if err != nil {
        slog.Error("shell proxy upstream invalid", "module", moduleID, "upstream", upstream, "error", err)
        WriteEnvelope(w, http.StatusInternalServerError, "upstream_invalid", err.Error(), moduleID, false)
        return
    }

    target, _ := url.Parse(upstream)
    rp := &httputil.ReverseProxy{
        Rewrite: func(pr *httputil.ProxyRequest) {
            pr.SetURL(target)
            pr.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + "/" + rest
            pr.Out.URL.RawPath = ""
            pr.Out.Host = target.Host
        },
        Transport: client.Transport,
        FlushInterval: 100 * time.Millisecond, // stream-friendly
        ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
            // A5: structured degradation, never a bare 502. Distinguish a
            // client cancellation (do not spam ERROR) from a real upstream
            // failure.
            if r.Context().Err() != nil {
                slog.Info("shell proxy request canceled by client", "module", moduleID, "path", rest)
                return
            }
            slog.Error("shell proxy upstream unavailable",
                "module", moduleID, "upstream", upstream, "path", rest, "error", err)
            WriteEnvelope(w, http.StatusServiceUnavailable, "backend_unavailable",
                fmt.Sprintf("module %q backend unavailable: %v", moduleID, err), moduleID, true)
        },
    }

    // E5 lesson: bound the proxied body so a huge upload cannot OOM the
    // gateway.
    if r.Body != nil {
        r.Body = http.MaxBytesReader(w, r.Body, p.bodyCap)
    }
    rp.ServeHTTP(w, r)
}

// HealthPoller polls each registered manifest's health URL and records
// online/unreachable observations in the registry. One lifecycle worker for
// the whole registry (R1 lesson: no bare ticker goroutine).
type HealthPoller struct {
    registry *Registry
    interval time.Duration
    client   *http.Client
}

func NewHealthPoller(registry *Registry, interval time.Duration) *HealthPoller {
    if interval <= 0 {
        interval = 30 * time.Second
    }
    bc := config.BackendConfig{Type: "openai-compatible", BaseURL: "http://127.0.0.1:1"}
    return &HealthPoller{
        registry: registry,
        interval: interval,
        client:   &http.Client{Transport: httpx.TransportForBackend(bc), Timeout: 5 * time.Second},
    }
}

// Run blocks until ctx is canceled; wired through lifecycle.Start.
func (h *HealthPoller) Run(ctx context.Context) {
    ticker := time.NewTicker(h.interval)
    defer ticker.Stop()
    h.pollAll(ctx)
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            h.pollAll(ctx)
        }
    }
}

func (h *HealthPoller) pollAll(ctx context.Context) {
    for _, view := range h.registry.List() {
        if view.Health == "" {
            continue
        }
        reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        err := h.probe(reqCtx, view.Health)
        cancel()
        h.registry.RecordHealth(view.ID, err == nil)
        if err != nil {
            slog.Debug("shell module health probe failed", "module", view.ID, "health", view.Health, "error", err)
        }
    }
}

func (h *HealthPoller) probe(ctx context.Context, healthURL string) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
    if err != nil {
        return err
    }
    resp, err := h.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 500 {
        return fmt.Errorf("health probe status %d", resp.StatusCode)
    }
    return nil
}
