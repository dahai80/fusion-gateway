package config

import (
    "log/slog"

    "gopkg.in/yaml.v3"
)

// rawModelsNamespace mirrors models.supply / models.aliases from the raw YAML
// bytes. Both maps are keyed by model names, which nearly always contain dots
// ("mlx-community/Qwen3.5-4B-MLX-4bit", "gpt-4o"). viper's AllSettings() — the
// tree Unmarshal decodes from — treats every dot inside a key as a path
// separator, re-nests the subtree and lowercases it, so a dotted supply key
// made Unmarshal fail outright (production: 'models.supply[mlx-community--qwen3][0]'
// expected type 'string', got unconvertible type 'map[string]interface {}').
// The struct fields are tagged mapstructure:"-" so viper never decodes them;
// this loader is their single source of truth. yaml.v3 keeps dotted map keys
// verbatim, case included.
type rawModelsNamespace struct {
    Models *struct {
        Supply  map[string][]string `yaml:"supply"`
        Aliases map[string]string   `yaml:"aliases"`
    } `yaml:"models"`
}

// loadModelsNamespace overwrites cfg.Models.Supply/Aliases from raw YAML
// bytes. An absent models section clears both maps — a reload must reflect
// removals, not silently keep the previous snapshot's entries.
func loadModelsNamespace(cfg *Config, data []byte) {
    var raw rawModelsNamespace
    if err := yaml.Unmarshal(data, &raw); err != nil {
        // viper already unmarshaled the same bytes, so a parse error here
        // means yaml.v3 rejected something viper tolerated. Keep the
        // (viper-untouched, i.e. empty) values and let validate decide.
        slog.Warn("models.* raw parse failed; supply/aliases stay empty", "error", err)
        return
    }
    if raw.Models == nil {
        cfg.Models.Supply = nil
        cfg.Models.Aliases = nil
        return
    }
    cfg.Models.Supply = raw.Models.Supply
    cfg.Models.Aliases = raw.Models.Aliases
    if n := len(cfg.Models.Supply); n > 0 {
        slog.Info("models.supply loaded (raw yaml path, viper bypass)", "entries", n)
    }
    if n := len(cfg.Models.Aliases); n > 0 {
        slog.Info("models.aliases loaded (raw yaml path, viper bypass)", "entries", n)
    }
}
