package adapter

// N1 (audit): provider factory registry. BuildProviders previously held a
// 21-way switch over backendCfg.Type (one case per provider vendor). The
// switch grew linearly with every new backend and forced all provider
// constructors to live in pool.go's switch. Extract a ProviderFactory map
// so adding a backend is a one-line registration, not a switch-case edit.
//
// Central registry (not per-package init()) avoids an import cycle: provider
// constructors live in the adapter package, so a map literal here is the
// natural home — no factory package needs to import pool. Behavior is
// identical to the prior switch (same ctors, same logs, same unknown-type
// fail-fast).

import (
    "fmt"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// ProviderFactory builds a Provider for a backend config. The signature takes
// the full ConfigSnapshot because one provider (fusion-mlx) needs
// cfg.Config.Routing; every other provider needs only (name, backendCfg).
// Factories that ignore the snapshot just don't read it.
type ProviderFactory func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider

// providerFactories maps backend type -> factory. Populated in init() so the
// registry is ready before BuildProviders runs. Add a new backend by adding
// one line here.
var providerFactories = map[string]ProviderFactory{}

func init() {
    providerFactories["fusion-mlx"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewFusionMLXProvider(bc, snap.Config.Routing)
    }
    providerFactories["fusion-kb"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewFusionKBProvider(name, bc)
    }
    providerFactories["fusion-model-hub"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewFusionModelHubProvider(name, bc)
    }
    providerFactories["openai-compatible"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewOpenAICompatibleProvider(name, bc)
    }
    providerFactories["anthropic"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewAnthropicProvider(name, bc)
    }
    providerFactories["bedrock"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewBedrockProvider(name, bc)
    }
    providerFactories["vertex"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewVertexProvider(name, bc)
    }
    providerFactories["foundry"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewFoundryProvider(name, bc)
    }
    providerFactories["volcengine"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewVolcengineProvider(name, bc)
    }
    providerFactories["qianfan"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewQianfanProvider(name, bc)
    }
    providerFactories["deepseek"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewDeepSeekProvider(name, bc)
    }
    providerFactories["openrouter"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewOpenRouterProvider(name, bc)
    }
    // Chinese LLM providers — user instruction: "这部分适配工作，要马上启动落地"
    providerFactories["dashscope"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewDashScopeProvider(name, bc)
    }
    providerFactories["moonshot"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewMoonshotProvider(name, bc)
    }
    providerFactories["zhipu"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewZhipuProvider(name, bc)
    }
    providerFactories["minimax"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewMinimaxProvider(name, bc)
    }
    providerFactories["baichuan"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewBaichuanProvider(name, bc)
    }
    providerFactories["hunyuan"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewHunyuanProvider(name, bc)
    }
    providerFactories["stepfun"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewStepFunProvider(name, bc)
    }
    providerFactories["yi"] = func(name string, bc config.BackendConfig, snap *config.ConfigSnapshot) Provider {
        return NewYiProvider(name, bc)
    }
}

// LookupProviderFactory returns the factory for a backend type, or an error
// matching the prior switch's unknown-type fail-fast (M3 fix).
func LookupProviderFactory(backendType string) (ProviderFactory, error) {
    f, ok := providerFactories[backendType]
    if !ok {
        return nil, fmt.Errorf("unknown backend type: %s", backendType)
    }
    return f, nil
}

// RegisteredProviderTypes returns every backend type with a registered
// factory, for diagnostics + the factory-coverage test.
func RegisteredProviderTypes() []string {
    types := make([]string, 0, len(providerFactories))
    for t := range providerFactories {
        types = append(types, t)
    }
    return types
}
