package config

import (
    "strings"
    "testing"
    "time"
)

func testBackends() map[string]BackendConfig {
    return map[string]BackendConfig{
        "fusion-mlx": {Type: "openai", BaseURL: "http://127.0.0.1:11434"},
        "glm-cloud":  {Type: "anthropic", BaseURL: "http://glm.example"},
    }
}

func TestResolveModel_PlainBare(t *testing.T) {
    res := ResolveModel("glm-5", testBackends(), ModelsConfig{})
    if res.Bare != "glm-5" || res.Backend != "" || res.Requested != "glm-5" {
        t.Fatalf("bare passthrough failed: %+v", res)
    }
}

func TestResolveModel_Qualified(t *testing.T) {
    res := ResolveModel("glm-cloud/glm-5", testBackends(), ModelsConfig{})
    if res.Backend != "glm-cloud" || res.Bare != "glm-5" {
        t.Fatalf("qualified split failed: %+v", res)
    }
    if res.Qualified() != "glm-cloud/glm-5" {
        t.Errorf("Qualified round-trip failed: %s", res.Qualified())
    }
}

func TestResolveModel_SlashNotBackend(t *testing.T) {
    // A slash-bearing model id whose prefix is NOT a configured backend
    // name passes through untouched (e.g. HF-style ids).
    res := ResolveModel("deepseek-ai/DeepSeek-V3", testBackends(), ModelsConfig{})
    if res.Backend != "" || res.Bare != "deepseek-ai/DeepSeek-V3" {
        t.Fatalf("non-backend slash prefix must pass through: %+v", res)
    }
}

func TestResolveModel_AliasChain(t *testing.T) {
    models := ModelsConfig{Aliases: map[string]string{
        "cc-main": "glm-cloud/glm-5",
    }}
    res := ResolveModel("cc-main", testBackends(), models)
    if res.Bare != "glm-5" || res.Backend != "glm-cloud" {
        t.Fatalf("alias to qualified failed: %+v", res)
    }
    if len(res.AliasHops) != 1 {
        t.Errorf("expected 1 hop, got %v", res.AliasHops)
    }
}

func TestResolveModel_AliasCycleStops(t *testing.T) {
    models := ModelsConfig{Aliases: map[string]string{
        "a": "b",
        "b": "a",
    }}
    res := ResolveModel("a", testBackends(), models)
    if len(res.AliasHops) != 3 {
        t.Errorf("expected hop cap at 3, got %v", res.AliasHops)
    }
}

func TestResolveModel_WhitespaceTrim(t *testing.T) {
    res := ResolveModel("  glm-cloud/glm-5  ", testBackends(), ModelsConfig{})
    if res.Backend != "glm-cloud" || res.Bare != "glm-5" {
        t.Fatalf("trim failed: %+v", res)
    }
}

func TestNormalizeBackends_APIKeyEnv(t *testing.T) {
    t.Setenv("TEST_GW_CLOUD_KEY", "sk-from-env")
    cfg := &Config{Backends: map[string]BackendConfig{
        "cloud-a": {Type: "deepseek", APIKeyEnv: "TEST_GW_CLOUD_KEY"},
    }}
    normalizeBackends(cfg)
    if cfg.Backends["cloud-a"].APIKey != "sk-from-env" {
        t.Fatalf("api_key_env not resolved: %q", cfg.Backends["cloud-a"].APIKey)
    }
    // vendor fill: deepseek base_url + timeout
    if cfg.Backends["cloud-a"].BaseURL != "https://api.deepseek.com/v1" {
        t.Errorf("vendor base_url not filled: %q", cfg.Backends["cloud-a"].BaseURL)
    }
    if cfg.Backends["cloud-a"].Timeout != 60*time.Second {
        t.Errorf("vendor timeout not filled: %v", cfg.Backends["cloud-a"].Timeout)
    }
}

func TestNormalizeBackends_ExplicitBaseURLWins(t *testing.T) {
    cfg := &Config{Backends: map[string]BackendConfig{
        "proxy-a": {Type: "deepseek", BaseURL: "http://my-litellm:4000/v1"},
    }}
    normalizeBackends(cfg)
    if cfg.Backends["proxy-a"].BaseURL != "http://my-litellm:4000/v1" {
        t.Fatalf("explicit base_url overridden: %q", cfg.Backends["proxy-a"].BaseURL)
    }
}

func TestNormalizeBackends_LegacyPlaintextKeyWarns(t *testing.T) {
    cfg := &Config{Backends: map[string]BackendConfig{
        "legacy": {Type: "openai", BaseURL: "http://x", APIKey: "sk-plaintext"},
    }}
    normalizeBackends(cfg) // must not panic/mutate key
    if cfg.Backends["legacy"].APIKey != "sk-plaintext" {
        t.Fatalf("legacy key must be preserved: %q", cfg.Backends["legacy"].APIKey)
    }
}

func TestValidateModels_EmptyAliasError(t *testing.T) {
    cfg := &Config{Models: ModelsConfig{Aliases: map[string]string{"": "x"}}}
    if err := validateModels(cfg); err == nil || !strings.Contains(err.Error(), "empty alias") {
        t.Fatalf("expected empty alias error, got %v", err)
    }
}

func TestValidateModels_UnknownSupplyBackendWarnsNotErrors(t *testing.T) {
    cfg := &Config{
        Backends: map[string]BackendConfig{"a": {}},
        Models:   ModelsConfig{Supply: map[string][]string{"m": {"nonexistent"}}},
    }
    if err := validateModels(cfg); err != nil {
        t.Fatalf("unknown supply backend must WARN not error: %v", err)
    }
}
