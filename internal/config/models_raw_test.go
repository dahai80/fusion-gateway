package config

import (
    "os"
    "path/filepath"
    "reflect"
    "strings"
    "testing"
)

// dottedSupplyYAML reproduces the production incident shape: a
// models.supply key with dots in the model name (plus a dotted alias), which
// viper's AllSettings() re-nests into sub-maps and fails to decode.
const dottedSupplyYAML = `
server:
  port: 11432
models:
  supply:
    "mlx-community/Qwen3.5-4B-MLX-4bit":
      - fusion-mlx
      - glm-cloud
  aliases:
    "qwen3.5-4b": "mlx-community/Qwen3.5-4B-MLX-4bit"
`

func TestLoadModelsNamespace_DottedKeysVerbatim(t *testing.T) {
    cfg := DefaultConfig()
    loadModelsNamespace(&cfg, []byte(dottedSupplyYAML))

    wantSupply := map[string][]string{
        "mlx-community/Qwen3.5-4B-MLX-4bit": {"fusion-mlx", "glm-cloud"},
    }
    if !reflect.DeepEqual(cfg.Models.Supply, wantSupply) {
        t.Fatalf("supply = %v, want %v", cfg.Models.Supply, wantSupply)
    }
    wantAliases := map[string]string{
        "qwen3.5-4b": "mlx-community/Qwen3.5-4B-MLX-4bit",
    }
    if !reflect.DeepEqual(cfg.Models.Aliases, wantAliases) {
        t.Fatalf("aliases = %v, want %v", cfg.Models.Aliases, wantAliases)
    }
}

func TestLoadModelsNamespace_AbsentSectionClears(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Models.Supply = map[string][]string{"stale": {"fusion-mlx"}}
    cfg.Models.Aliases = map[string]string{"stale": "x"}
    loadModelsNamespace(&cfg, []byte("server:\n  port: 11432\n"))
    if cfg.Models.Supply != nil || cfg.Models.Aliases != nil {
        t.Fatalf("absent models section must clear both maps, got supply=%v aliases=%v",
            cfg.Models.Supply, cfg.Models.Aliases)
    }
}

// TestProbeValidate_DottedSupplyPasses: regression for the finding-apply
// incident — the mutated config (dotted supply key) previously failed at
// Reload time AFTER already landing on disk.
func TestProbeValidate_DottedSupplyPasses(t *testing.T) {
    if err := ProbeValidate([]byte(dottedSupplyYAML)); err != nil {
        t.Fatalf("dotted models.supply must probe-validate, got: %v", err)
    }
}

func TestProbeValidate_RejectsInvalid(t *testing.T) {
    err := ProbeValidate([]byte("server:\n  port: -1\n"))
    if err == nil {
        t.Fatal("invalid config must fail probe validation")
    }
    if !strings.Contains(err.Error(), "invalid server port") {
        t.Fatalf("unexpected error: %v", err)
    }
}

// TestLoad_DottedModelSupply: end-to-end through the real Load path.
func TestLoad_DottedModelSupply(t *testing.T) {
    path := filepath.Join(t.TempDir(), "config.yaml")
    if err := os.WriteFile(path, []byte(dottedSupplyYAML), 0600); err != nil {
        t.Fatal(err)
    }
    snap, err := Load(path)
    if err != nil {
        t.Fatalf("Load with dotted models.supply failed: %v", err)
    }
    got, ok := snap.Config.Models.Supply["mlx-community/Qwen3.5-4B-MLX-4bit"]
    if !ok {
        t.Fatalf("supply key missing after Load: %v", snap.Config.Models.Supply)
    }
    if !reflect.DeepEqual(got, []string{"fusion-mlx", "glm-cloud"}) {
        t.Fatalf("supply value = %v", got)
    }
    if snap.Config.Models.Aliases["qwen3.5-4b"] != "mlx-community/Qwen3.5-4B-MLX-4bit" {
        t.Fatalf("alias missing after Load: %v", snap.Config.Models.Aliases)
    }
}
