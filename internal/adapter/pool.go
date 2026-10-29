package adapter

import (
    "context"
    "fmt"
    "log/slog"
    "sort"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

type Pool struct {
    mu        sync.RWMutex
    providers map[string]Provider
    backends  map[string]config.BackendConfig
    // localEngine is the canonical fusion-mlx engine role (#210): the
    // *FusionMLXProvider that main.go wires into the router (InFlight /
    // ModelSet / 60s model-set refresh) and that owns the RR4 slot counter.
    // It is the typed provider when a backend declares type fusion-mlx, or a
    // SHADOW instance built from a local-classified openai-compatible backend
    // (same engine, registered under that type for the Images passthrough).
    localEngine     *FusionMLXProvider
    localEngineName string
}

func NewPool() *Pool {
    return &Pool{
        providers: make(map[string]Provider),
        backends:  make(map[string]config.BackendConfig),
    }
}

func (p *Pool) Register(name string, provider Provider, backendCfg config.BackendConfig) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.providers[name] = provider
    p.backends[name] = backendCfg
    slog.Info("provider registered", "name", name, "type", backendCfg.Type, "base_url", backendCfg.BaseURL)
}

func (p *Pool) Get(name string) (Provider, bool) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    prov, ok := p.providers[name]
    return prov, ok
}

func (p *Pool) GetByBackend(backend string) (Provider, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()

    prov, ok := p.providers[backend]
    if !ok {
        return nil, fmt.Errorf("backend not found: %s", backend)
    }
    return prov, nil
}

func (p *Pool) ListProviders() []string {
    p.mu.RLock()
    defer p.mu.RUnlock()

    names := make([]string, 0, len(p.providers))
    for name := range p.providers {
        names = append(names, name)
    }
    return names
}

// IsLocalProvider reports whether the named provider is a local backend
// (fusion-mlx / fusion-kb / fusion-model-hub) rather than a cloud provider.
// Used to filter /v1/models results when routing mode is "local".
func (p *Pool) IsLocalProvider(name string) bool {
    p.mu.RLock()
    defer p.mu.RUnlock()
    bc, ok := p.backends[name]
    if !ok {
        return false
    }
    return isLocalBackend(bc)
}

// isLocalBackend reports whether a backend config describes a local engine:
// an intrinsically local type (fusion-mlx/kb/model-hub) OR any backend whose
// endpoint is local — loopback base_url or UDS socket_path. Type-only
// detection misclassified a fusion-mlx instance registered as
// openai-compatible (switched 2026-10-12 for the Images passthrough) as
// cloud: /v1/models came back empty under routing.mode=local and local
// traffic was wrapped in the cloud in-flight tracking decorator. Mirrors the
// httpx.IsLocalBackendConfig predicate (#208 local header-timeout semantics).
func isLocalBackend(bc config.BackendConfig) bool {
    switch bc.Type {
    case "fusion-mlx", "fusion-kb", "fusion-model-hub":
        return true
    }
    return httpx.IsLocalBackendConfig(bc)
}

func (p *Pool) GetFusionMLX() *FusionMLXProvider {
    p.mu.RLock()
    defer p.mu.RUnlock()
    for _, prov := range p.providers {
        if mlx, ok := prov.(*FusionMLXProvider); ok {
            return mlx
        }
    }
    // #210: no typed fusion-mlx backend — fall back to the shadow engine role
    // built from a local-classified openai-compatible backend (same engine).
    return p.localEngine
}

// FusionMLXName returns the config key under which the fusion-mlx provider is
// registered (e.g. "fusion-mlx"), plus the typed provider. Returns "" and nil
// when no fusion-mlx backend is configured. Callers need the key to render it
// in the health payload's backends map (#59). #210: falls back to the shadow
// engine role name when the engine is registered as openai-compatible.
func (p *Pool) FusionMLXName() (string, *FusionMLXProvider) {
    p.mu.RLock()
    defer p.mu.RUnlock()
    for name, prov := range p.providers {
        if mlx, ok := prov.(*FusionMLXProvider); ok {
            return name, mlx
        }
    }
    if p.localEngine != nil {
        return p.localEngineName, p.localEngine
    }
    return "", nil
}

func (p *Pool) GetModelHub() *FusionModelHubProvider {
    p.mu.RLock()
    defer p.mu.RUnlock()
    for _, prov := range p.providers {
        if hub, ok := prov.(*FusionModelHubProvider); ok {
            return hub
        }
    }
    return nil
}

func (p *Pool) HealthCheckAll(ctx context.Context) map[string]error {
    return p.HealthCheckAllSkipping(ctx, nil)
}

// HealthCheckAllSkipping probes every backend except the named ones. #381:
// the model-hub health endpoint subcalls the gateway's mlx_url (which
// defaults to the gateway itself), so probing the hub from /health re-enters
// the gateway health surface — an unbounded probe cycle truncated only by
// timeouts. Callers detect the hub's own probe (X-Fusion-Source: model-hub)
// and skip the hub backends to break the cycle.
func (p *Pool) HealthCheckAllSkipping(ctx context.Context, skip map[string]bool) map[string]error {
    p.mu.RLock()
    defer p.mu.RUnlock()

    results := make(map[string]error)
    for name, provider := range p.providers {
        if skip[name] {
            continue
        }
        results[name] = provider.HealthCheck(ctx)
    }
    return results
}

// ModelHubNames returns the backend names backed by FusionModelHubProvider —
// the probes that must be skipped when the /health caller is the hub itself.
func (p *Pool) ModelHubNames() []string {
    p.mu.RLock()
    defer p.mu.RUnlock()
    var names []string
    for name, provider := range p.providers {
        if _, ok := provider.(*FusionModelHubProvider); ok {
            names = append(names, name)
        }
    }
    return names
}

func (p *Pool) BuildProviders(cfg *config.ConfigSnapshot) error {
    p.mu.Lock()
    defer p.mu.Unlock()

    for name, backendCfg := range cfg.Config.Backends {
        if !backendCfg.Enabled {
            slog.Info("skipping disabled backend", "name", name)
            continue
        }

        // N1 (audit): build via the provider factory registry (register.go)
        // instead of a per-type switch. The registry centralizes type->ctor
        // mapping so adding a backend is one registration line, not a switch
        // case. Behavior matches the prior switch: same constructors, fail-fast
        // on unknown type (M3), uniform structured log per registration
        // (mirrors the Register() method's "provider registered" shape).
        factory, err := LookupProviderFactory(backendCfg.Type)
        if err != nil {
            // M3 fix: fail-fast on unknown backend type instead of silent skip.
            return fmt.Errorf("unknown backend type: %s for backend %q", backendCfg.Type, name)
        }
        provider := factory(name, backendCfg, cfg)
        p.providers[name] = provider
        slog.Info("registered provider", "name", name, "type", backendCfg.Type, "base_url", backendCfg.BaseURL)

        p.backends[name] = backendCfg
    }

    // C4 fix: remove stale providers that are no longer in config or disabled
    for name := range p.providers {
        cfgBackend, exists := cfg.Config.Backends[name]
        if !exists || !cfgBackend.Enabled {
            slog.Info("removing stale provider on reload", "name", name)
            delete(p.providers, name)
            delete(p.backends, name)
        }
    }

    // EI6: wrap each cloud provider with an in-flight tracking decorator so the
    // fusion_gateway_in_flight_requests{backend="cloud"} series is a live signal
    // (was permanently 0 — the cloud label was never written). Mirrors the cluster
    // node_adapter IncrInFlight/DecrInFlight pattern: inc on call entry, dec on
    // call return (sync methods) or stream close (StreamChat drain goroutine).
    // Local providers (fusion-mlx/kb/model-hub) are tracked by the router's
    // localInFlight counter via the metrics_sync loop, so they are NOT wrapped.
    // NOTE: read p.backends directly here — we already hold p.mu.Lock (taken at
    // the top of BuildProviders), so calling p.IsLocalProvider() would re-acquire
    // p.mu.RLock on the same goroutine and deadlock (Go RWMutex: RLock inside
    // Lock on the same goroutine blocks forever).
    for name, prov := range p.providers {
        bc := p.backends[name]
        if isLocalBackend(bc) {
            continue
        }
        p.providers[name] = WrapCloudTracking(prov)
        slog.Info("wrapped cloud provider with in-flight tracking", "name", name)
    }

    // #210: resolve the local-engine role AFTER the cloud wrap, so the role
    // and the slot decorator layer in the right order. Typed fusion-mlx wins;
    // otherwise a local-classified backend (loopback/UDS openai-compatible
    // pointing at the SAME engine) gets a shadow FusionMLXProvider as the
    // role, and its serving provider is wrapped with the shared slot counter
    // so RR4 + the router's local in-flight signal actually track local load.
    p.localEngine = nil
    p.localEngineName = ""
    for name, prov := range p.providers {
        if mlx, ok := prov.(*FusionMLXProvider); ok {
            p.localEngine = mlx
            p.localEngineName = name
            break
        }
    }
    if p.localEngine == nil {
        candidates := make([]string, 0, len(p.providers))
        for name, bc := range p.backends {
            if _, ok := p.providers[name]; !ok {
                continue
            }
            // fusion-kb / fusion-model-hub are local SERVICES, not the mlx
            // engine — the shadow role must point at the inference engine.
            switch bc.Type {
            case "fusion-kb", "fusion-model-hub":
                continue
            }
            if isLocalBackend(bc) {
                candidates = append(candidates, name)
            }
        }
        sort.Strings(candidates)
        // Prefer the conventional name so multi-local-backend configs are
        // deterministic and match operator expectation.
        for _, name := range candidates {
            if name == "fusion-mlx" {
                candidates = []string{name}
                break
            }
        }
        if len(candidates) > 0 {
            name := candidates[0]
            bc := p.backends[name]
            shadow := NewFusionMLXProvider(bc, cfg.Config.Routing)
            p.localEngine = shadow
            p.localEngineName = name
            p.providers[name] = WrapLocalSlot(p.providers[name], shadow)
            slog.Info("local engine role: shadow FusionMLXProvider for local-classified backend",
                "name", name, "type", bc.Type, "base_url", bc.BaseURL,
                "note", "serving provider wrapped with RR4 slot accounting (#210)")
        }
    } else {
        slog.Info("local engine role: typed fusion-mlx provider", "name", p.localEngineName)
    }

    // #389 follow-up (live finding): BuildProviders runs on every config reload
    // and rebuilds the engine-role provider fresh, so its modelSet starts nil.
    // The refresh worker re-fetches the role each tick but settles into a 60s
    // cadence after bootstrap — a reload left localVisionModelLoaded (and the
    // router's P0.2/P6 model guards) blind for up to 60s, false-negativing the
    // multimodal entry guard into the multimodal_unconfigured sentinel. Warm
    // the set eagerly with a short timeout; on failure the next 60s tick
    // recovers (same keep-last-good contract as #343).
    warmCtx, warmCancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer warmCancel()
    if p.localEngine != nil && p.localEngine.RefreshModelSet(warmCtx) {
        slog.Info("warmed local engine model set on build", "name", p.localEngineName)
    }

    return nil
}
