package config

// Regression for the provider-delete 404 ("backend not found in config
// file"): viper re-nests dotted backend names, materializing a ghost
// "kimi-k2" backend from `backends.kimi-k2.7-code:` and dropping the real
// dotted entry. The admin UI then offers a delete on a backend the YAML file
// does not contain. fixDottedBackendNames must re-derive the map from raw
// YAML: dotted name present, ghost absent, env overrides re-applied.

import (
    "os"
    "testing"
    "time"
)

func TestDottedBackendNames(t *testing.T) {
    raw := []byte(`
backends:
  fusion-mlx:
    type: openai-compatible
    base_url: http://127.0.0.1:11434
    api_key: file-key
    timeout: 5m
    enabled: true
  kimi-k2.7-code:
    type: openai-compatible
    base_url: http://127.0.0.1:9999
    api_key: kimi-key
    enabled: false
`)
    cfg := DefaultConfig()
    cfg.Backends = map[string]BackendConfig{
        "kimi-k2": {}, // the viper dot-split ghost
    }
    t.Setenv("FUSION_MLX_API_KEY", "env-mlx-key")
    fixDottedBackendNames(&cfg, raw)

    real, ok := cfg.Backends["kimi-k2.7-code"]
    if !ok {
        t.Fatal("dotted backend kimi-k2.7-code missing (#654 ghost bug)")
    }
    if real.BaseURL != "http://127.0.0.1:9999" || real.APIKey != "kimi-key" || real.Enabled {
        t.Fatalf("dotted backend decoded wrong: %+v", real)
    }
    if ghost, ok := cfg.Backends["kimi-k2"]; ok {
        t.Fatalf("ghost dot-split backend survived: %+v", ghost)
    }
    mlx := cfg.Backends["fusion-mlx"]
    if mlx.Timeout != 5*time.Minute {
        t.Fatalf("duration decode failed: %v", mlx.Timeout)
    }
    if mlx.APIKey != "env-mlx-key" {
        t.Fatalf("FUSION_MLX_API_KEY override lost: %q", mlx.APIKey)
    }
}

func TestDottedBackendNamesNoEnv(t *testing.T) {
    os.Unsetenv("FUSION_MLX_API_KEY")
    raw := []byte(`
backends:
  fusion-mlx:
    type: openai-compatible
    base_url: http://127.0.0.1:11434
    api_key: file-key
`)
    cfg := DefaultConfig()
    fixDottedBackendNames(&cfg, raw)
    if cfg.Backends["fusion-mlx"].APIKey != "file-key" {
        t.Fatalf("file key clobbered without env: %q", cfg.Backends["fusion-mlx"].APIKey)
    }
}

func TestDottedBackendNamesNoBackendsSection(t *testing.T) {
    cfg := DefaultConfig()
    seed := map[string]BackendConfig{"fusion-mlx": {Enabled: true}}
    cfg.Backends = seed
    fixDottedBackendNames(&cfg, []byte("server:\n  port: 11432\n"))
    if _, ok := cfg.Backends["fusion-mlx"]; !ok {
        t.Fatal("absent backends section must leave the existing map untouched")
    }
}
