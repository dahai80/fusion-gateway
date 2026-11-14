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
    "github.com/fusion-gateway/fusion-gateway/internal/logthrottle"
)

// upstreamFn resolves a module id to its proxy upstream base URL and its
// upstream credential ("_" when the module has no proxy mapping). A function
// — not a static map — so a config hot-reload takes effect without
// rebuilding the proxy.
type upstreamFn func(moduleID string) (string, string)

// extUpstreamFn resolves a per-path upstream override (#256): for a request
// path (proxy-relative, leading "/") matching an endpoint the module
// declared in its manifest ext fields (searchProviders/notifications),
// return the dynamic registration's own upstream + credential. ok=false
// keeps the static upstream.
type extUpstreamFn func(moduleID, path string) (string, string, bool)

// Proxy forwards /shell/<module_id>/... requests to the module's upstream
// and degrades failures into the standardized error envelope (PRD §7.2):
// backend down is a 503 envelope with retryable=true, NEVER a bare 502 or a
// hung connection. The body cap (E5 lesson) bounds proxied request bodies.
type Proxy struct {
    resolveUpstream    upstreamFn
    resolveExtUpstream extUpstreamFn
    // revokedFn (#261 K1) reports whether a module's gateway-issued service
    // key was revoked — the proxy then serves the structured 503
    // module_key_revoked instead of forwarding an unauthenticated request.
    // nil = no revocation surface (revocation disabled / not wired).
    revokedFn func(moduleID string) bool
    // convergeFn (#669) fires when an upstream rejects the injected proxy
    // credential with 401 — the one hard signal that the "dynamic wins"
    // preference (#294) handed the proxy a credential the module no longer
    // accepts (e.g. the module regenerated its own store across a restart
    // and never consumed the gateway-issued plaintext). The server-side
    // implementation adopts the static api_key as the authoritative
    // credential so divergence self-heals instead of wedging every embedded
    // console call. nil = no convergence surface (behavior unchanged).
    convergeFn  func(moduleID, failedKey string)
    transports  sync.Map // "<moduleID>|<upstream>" -> *http.Client (built once per pair)
    bodyCap     int64
}

func NewProxy(resolveUpstream upstreamFn) *Proxy {
    return &Proxy{resolveUpstream: resolveUpstream, bodyCap: 256 << 20}
}

// SetRevokedChecker wires the service-key revocation check (#261 K1).
func (p *Proxy) SetRevokedChecker(fn func(moduleID string) bool) {
    p.revokedFn = fn
}

// SetExtResolver wires the per-path upstream override (#256). Optional —
// without it the proxy behaves exactly as before (one upstream per module).
func (p *Proxy) SetExtResolver(fn extUpstreamFn) {
    p.resolveExtUpstream = fn
}

// SetCredentialConverger wires the #669 upstream-401 convergence hook.
// Optional — without it upstream 401s pass through unchanged.
func (p *Proxy) SetCredentialConverger(fn func(moduleID, failedKey string)) {
    p.convergeFn = fn
}

// moduleClient builds (once) the HTTP client for one module upstream. Goes
// through httpx.TransportForBackend (RR11/RR5 lessons): MaxConnsPerHost cap
// + ResponseHeaderTimeout + dial timeout — never a bare &http.Client{}.
// Module upstreams are loopback in the union-web topology, so the local
// predicate applies and the header timeout stretches to 300s (mirrors #208).
// The cache key includes the upstream: a module can dial TWO upstreams in
// one lifetime now that ext paths may override the static one (#256).
func (p *Proxy) moduleClient(moduleID, upstream string) (*http.Client, error) {
    cacheKey := moduleID + "|" + upstream
    if c, ok := p.transports.Load(cacheKey); ok {
        return c.(*http.Client), nil
    }
    u, err := url.Parse(upstream)
    if err != nil || u.Scheme == "" || u.Host == "" {
        return nil, fmt.Errorf("module %s upstream %q is not a valid absolute URL", moduleID, upstream)
    }
    bc := config.BackendConfig{Type: "openai-compatible", BaseURL: upstream}
    client := &http.Client{Transport: httpx.TransportForBackend(bc)}
    actual, loaded := p.transports.LoadOrStore(cacheKey, client)
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

    // #261 K1: a revoked service key degrades immediately and structurally
    // (A5): the module can no longer authenticate its callers, so forwarding
    // would leak an unauthenticated request upstream. Shell host greys the
    // module via the unreachable mechanism in parallel.
    if p.revokedFn != nil && p.revokedFn(moduleID) {
        slog.Warn("shell proxy request rejected: module service key revoked", "module", moduleID, "path", rest)
        WriteEnvelope(w, http.StatusServiceUnavailable, "module_key_revoked",
            fmt.Sprintf("module %q service key revoked; contact the gateway admin to re-register", moduleID), moduleID, true)
        return
    }

    upstream, apiKey := p.resolveUpstream(moduleID)
    if upstream == "" || upstream == "_" {
        WriteEnvelope(w, http.StatusNotFound, "module_not_found",
            fmt.Sprintf("module %q has no proxy upstream configured", moduleID), moduleID, false)
        return
    }

    // #256: an ext endpoint declared in the module's manifest
    // (searchProviders/notifications) routes to the dynamic registration's
    // own upstream when the static config pins a different one — the engine
    // keeps serving the SPA + core API while the bridge/module process
    // serves its ext endpoints. Static otherwise wins (#232 semantics).
    // The resolver receives the FULL gateway path; modules may declare
    // endpoints either gateway-absolute (/shell/<id>/api/...) or
    // module-relative (/api/...) — the registry normalizes both (#256).
    if p.resolveExtUpstream != nil {
        if ou, okey, ok := p.resolveExtUpstream(moduleID, r.URL.Path); ok && ou != "" && ou != upstream {
            slog.Debug("shell proxy ext-path upstream override",
                "module", moduleID, "path", r.URL.Path, "upstream", ou)
            upstream, apiKey = ou, okey
        }
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
            // #222: never forward the caller's shell session token (or any
            // ambient cookie) upstream — the upstream credential is the
            // per-module static api_key, not the user identity.
            pr.Out.Header.Del("Authorization")
            pr.Out.Header.Del("Cookie")
            if apiKey != "" {
                pr.Out.Header.Set("Authorization", "Bearer "+apiKey)
            }
        },
        Transport: client.Transport,
        FlushInterval: 100 * time.Millisecond, // stream-friendly
        ModifyResponse: func(resp *http.Response) error {
            // #669: an upstream 401 against the injected proxy credential is
            // the divergence signal — the module no longer accepts the
            // gateway's "dynamic wins" credential. Fire the convergence hook
            // (stateful, self-limiting: only the failed credential is ever
            // replaced, so a wrong guess cannot flip-flop) and pass the 401
            // through; the next request already carries the adopted key.
            if resp.StatusCode == http.StatusUnauthorized && apiKey != "" && p.convergeFn != nil {
                p.convergeFn(moduleID, apiKey)
            }
            return nil
        },
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
    // Module entry documents (SPA index.html) must never be cached: a stale
    // cached entry page pins an old bundle hash (and any baked-in theme
    // defaults) regardless of what the module currently serves. Hashed
    // /assets/ responses keep their own immutable caching upstream.
    // Navigation check mirrors server.spaFallbackRequest (GET/HEAD + HTML
    // Accept) — inlined because it lives in the server package.
    if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
        strings.Contains(r.Header.Get("Accept"), "text/html") {
        w.Header().Set("Cache-Control", "no-store, max-age=0")
    }
    rp.ServeHTTP(w, r)
}

// HealthPoller polls each registered manifest's health URL and records
// online/unreachable observations in the registry. One lifecycle worker for
// the whole registry (R1 lesson: no bare ticker goroutine).
type HealthPoller struct {
    registry   *Registry
    interval   time.Duration
    baseOrigin string
    client     *http.Client
}

// baseOrigin is the gateway's own absolute origin ("http://127.0.0.1:11432").
// Manifest health fields that are bare paths ("/healthz", the #223
// same-origin convention used by the built-in admin console) are resolved
// against it before probing; empty disables resolution and those manifests
// are probe-skipped as before.
func NewHealthPoller(registry *Registry, interval time.Duration, baseOrigin string) *HealthPoller {
    if interval <= 0 {
        interval = 30 * time.Second
    }
    bc := config.BackendConfig{Type: "openai-compatible", BaseURL: "http://127.0.0.1:1"}
    return &HealthPoller{
        registry:   registry,
        interval:   interval,
        baseOrigin: strings.TrimSuffix(strings.TrimSpace(baseOrigin), "/"),
        client:     &http.Client{Transport: httpx.TransportForBackend(bc), Timeout: 5 * time.Second},
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

// relativeHealthWarn throttles the "health URL is not absolute" warning per
// module: a manifest re-registers every 15s and every poll pass would repeat it.
var relativeHealthWarn = logthrottle.New(5 * time.Minute)

// absoluteHealthURL reports whether the manifest health field can be probed.
// Several persisted manifests carry a path ("/healthz") instead of a URL; the
// poller used to hand that straight to http.Get, which fails every pass with
// "unsupported protocol scheme" at DEBUG, so those modules were permanently
// marked unreachable with nothing but debug noise to explain it.
func absoluteHealthURL(raw string) bool {
    u, err := url.Parse(strings.TrimSpace(raw))
    if err != nil {
        return false
    }
    return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (h *HealthPoller) pollAll(ctx context.Context) {
    for _, view := range h.registry.List() {
        if view.Health == "" {
            continue
        }
        healthURL := view.Health
        if !absoluteHealthURL(healthURL) {
            // #223: a bare-path health field is same-origin — served by the
            // gateway itself. Resolve it against the gateway's own origin
            // and probe it; skipping left same-origin modules (built-in
            // admin console: health=/healthz) with nobody heartbeating them,
            // so they went unreachable one heartbeat_timeout after every
            // restart and the shell rendered "Module unavailable".
            resolved, ok := h.resolveRelativeHealth(healthURL)
            if !ok {
                if relativeHealthWarn.Allow(view.ID) {
                    slog.Warn("shell module health URL is not absolute, probe skipped (manifest health must be an http(s) URL)",
                        "module", view.ID,
                        "health", view.Health,
                    )
                }
                continue
            }
            healthURL = resolved
        }
        reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        err := h.probe(reqCtx, healthURL)
        cancel()
        h.registry.RecordHealth(view.ID, err == nil)
        if err == nil {
            // A successful probe proves liveness for dynamic registrations
            // too: nobody else heartbeats them after the initial register,
            // otherwise a healthy module flips unreachable after
            // heartbeat_timeout despite a green probe.
            _ = h.registry.Heartbeat(view.ID)
        }
        if err != nil {
            slog.Debug("shell module health probe failed", "module", view.ID, "health", view.Health, "error", err)
        }
    }
    // #276: drop stale dynamic registrations after the probe pass — a green
    // probe above refreshed the heartbeat, so anything still unreachable here
    // is heartbeat-dead; entries whose upstream still accepts connections
    // stay (greyed), only connection-refusing ones expire.
    h.expireStale(ctx)
}

// expireStale drops dynamic registrations whose heartbeat has expired AND
// whose upstream actively refuses connections (#276): a stale registration
// pointing at a dead port keeps serving its menu entry to every fresh shell
// session, and each proxy hit degrades to 503 forever. Removing it makes the
// next manifest list reflect reality within one heartbeat interval; the
// module re-registers via its own start.sh when it comes back, so no operator
// action is needed. Builtins are protected (Remove rejects them), and entries
// without an upstream cannot be connection-checked — both stay unreachable.
func (h *HealthPoller) expireStale(ctx context.Context) {
    for _, view := range h.registry.List() {
        if !view.Registered || view.Status != StatusUnreachable || view.Upstream == "" {
            continue
        }
        reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        err := h.probe(reqCtx, view.Upstream)
        cancel()
        if err == nil {
            // Upstream accepts connections — heartbeat-dead but alive on the
            // wire; keep it greyed, dropping would flap a half-up module.
            continue
        }
        if rmErr := h.registry.Remove(view.ID); rmErr != nil {
            // Builtins / static entries land here — protected by design.
            slog.Debug("shell stale manifest kept", "module", view.ID, "error", rmErr)
            continue
        }
        slog.Info("shell stale manifest expired (heartbeat gone, upstream refuses connections)",
            "module", view.ID, "upstream", view.Upstream, "probe_error", err)
    }
}

// resolveRelativeHealth turns a bare-path health field into an absolute URL
// against the gateway's own origin. ok=false when there is no base origin
// configured or the health field is not a rooted path — those stay
// probe-skipped exactly as before.
func (h *HealthPoller) resolveRelativeHealth(raw string) (string, bool) {
    if h.baseOrigin == "" {
        return "", false
    }
    trimmed := strings.TrimSpace(raw)
    u, err := url.Parse(trimmed)
    if err != nil || !strings.HasPrefix(u.Path, "/") || u.Scheme != "" || u.Host != "" {
        return "", false
    }
    return h.baseOrigin + trimmed, true
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
