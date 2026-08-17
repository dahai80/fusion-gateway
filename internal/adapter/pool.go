package adapter

import (
    "context"
    "fmt"
    "log/slog"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type Pool struct {
    mu        sync.RWMutex
    providers map[string]Provider
    backends  map[string]config.BackendConfig
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
    switch bc.Type {
    case "fusion-mlx", "fusion-kb", "fusion-model-hub":
        return true
    }
    return false
}

func (p *Pool) GetFusionMLX() *FusionMLXProvider {
    p.mu.RLock()
    defer p.mu.RUnlock()
    for _, prov := range p.providers {
        if mlx, ok := prov.(*FusionMLXProvider); ok {
            return mlx
        }
    }
    return nil
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
    p.mu.RLock()
    defer p.mu.RUnlock()

    results := make(map[string]error)
    for name, provider := range p.providers {
        results[name] = provider.HealthCheck(ctx)
    }
    return results
}

func (p *Pool) BuildProviders(cfg *config.ConfigSnapshot) error {
    p.mu.Lock()
    defer p.mu.Unlock()

    for name, backendCfg := range cfg.Config.Backends {
        if !backendCfg.Enabled {
            slog.Info("skipping disabled backend", "name", name)
            continue
        }

        switch backendCfg.Type {
        case "fusion-mlx":
            provider := NewFusionMLXProvider(backendCfg, cfg.Config.Routing)
            p.providers[name] = provider
        case "openai-compatible":
            provider := NewOpenAICompatibleProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered openai-compatible provider", "name", name, "base_url", backendCfg.BaseURL)
        case "anthropic":
            provider := NewAnthropicProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered anthropic provider", "name", name, "base_url", backendCfg.BaseURL)
        case "bedrock":
            provider := NewBedrockProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered bedrock provider (AWS SigV4)", "name", name, "base_url", backendCfg.BaseURL)
        case "vertex":
            provider := NewVertexProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered vertex provider (GCP OAuth2)", "name", name, "base_url", backendCfg.BaseURL)
        case "foundry":
            provider := NewFoundryProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered foundry provider (Azure)", "name", name, "base_url", backendCfg.BaseURL)
        case "volcengine":
            provider := NewVolcengineProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered volcengine provider", "name", name, "base_url", backendCfg.BaseURL)
        case "qianfan":
            provider := NewQianfanProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered qianfan provider", "name", name, "base_url", backendCfg.BaseURL)
        case "deepseek":
            provider := NewDeepSeekProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered deepseek provider", "name", name, "base_url", backendCfg.BaseURL)
        case "openrouter":
            provider := NewOpenRouterProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered openrouter provider", "name", name, "base_url", backendCfg.BaseURL)
        // Chinese LLM providers - user instruction: "这部分适配工作，要马上启动落地"
        case "dashscope":
            provider := NewDashScopeProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered dashscope provider", "name", name, "base_url", backendCfg.BaseURL)
        case "moonshot":
            provider := NewMoonshotProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered moonshot provider", "name", name, "base_url", backendCfg.BaseURL)
        case "zhipu":
            provider := NewZhipuProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered zhipu provider", "name", name, "base_url", backendCfg.BaseURL)
        case "minimax":
            provider := NewMinimaxProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered minimax provider", "name", name, "base_url", backendCfg.BaseURL)
        case "baichuan":
            provider := NewBaichuanProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered baichuan provider", "name", name, "base_url", backendCfg.BaseURL)
        case "hunyuan":
            provider := NewHunyuanProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered hunyuan provider", "name", name, "base_url", backendCfg.BaseURL)
        case "stepfun":
            provider := NewStepFunProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered stepfun provider", "name", name, "base_url", backendCfg.BaseURL)
        case "yi":
            provider := NewYiProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered yi provider", "name", name, "base_url", backendCfg.BaseURL)
        case "fusion-kb":
            provider := NewFusionKBProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered fusion-kb provider", "name", name, "base_url", backendCfg.BaseURL)
        case "fusion-model-hub":
            provider := NewFusionModelHubProvider(name, backendCfg)
            p.providers[name] = provider; slog.Info("registered fusion-model-hub provider", "name", name, "base_url", backendCfg.BaseURL)
        default:
            // M3 fix: fail-fast on unknown backend type instead of silent skip
            return fmt.Errorf("unknown backend type: %s for backend %q", backendCfg.Type, name)
        }

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

    return nil
}
