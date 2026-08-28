package adapter

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestN1_FactoryRegistryComplete asserts every backend type shipped in the
// prior switch now resolves a non-nil factory from the registry. A new backend
// added to config without a registration would otherwise fail at runtime with
// "unknown backend type" — this test catches that at build time.
func TestN1_FactoryRegistryComplete(t *testing.T) {
    wantTypes := []string{
        "fusion-mlx", "fusion-kb", "fusion-model-hub",
        "openai-compatible", "anthropic",
        "bedrock", "vertex", "foundry",
        "volcengine", "qianfan", "deepseek", "openrouter",
        "dashscope", "moonshot", "zhipu", "minimax",
        "baichuan", "hunyuan", "stepfun", "yi",
    }
    for _, bt := range wantTypes {
        f, err := LookupProviderFactory(bt)
        if err != nil {
            t.Errorf("N1: type %q: LookupProviderFactory error: %v", bt, err)
            continue
        }
        if f == nil {
            t.Errorf("N1: type %q: factory is nil", bt)
        }
    }
}

// TestN1_UnknownTypeFailFast preserves the M3 fail-fast contract: an unknown
// backend type returns an error (the prior switch's default case), not a
// silent skip.
func TestN1_UnknownTypeFailFast(t *testing.T) {
    _, err := LookupProviderFactory("nonexistent-backend-type")
    if err == nil {
        t.Fatal("N1: unknown backend type must return error (M3 fail-fast), got nil")
    }
}

// TestN1_BuildProvidersViaRegistry verifies the registry-driven BuildProviders
// constructs a known provider for a registered type, identical to the prior
// switch behavior. Uses fusion-mlx (the one ctor that reads cfg.Config.Routing)
// to confirm the snapshot path works through the factory closure.
func TestN1_BuildProvidersViaRegistry(t *testing.T) {
    pool := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "local": {
                    Type:    "fusion-mlx",
                    Enabled: true,
                    BaseURL: "http://127.0.0.1:11434",
                },
            },
            Routing: config.RoutingConfig{},
        },
    }
    if err := pool.BuildProviders(cfg); err != nil {
        t.Fatalf("N1: BuildProviders fusion-mlx: %v", err)
    }
    mlx := pool.GetFusionMLX()
    if mlx == nil {
        t.Fatal("N1: GetFusionMLX returned nil — registry did not construct fusion-mlx")
    }
}
