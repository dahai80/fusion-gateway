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
        default:
            slog.Warn("unknown backend type", "name", name, "type", backendCfg.Type)
        }

        p.backends[name] = backendCfg
    }

    return nil
}
