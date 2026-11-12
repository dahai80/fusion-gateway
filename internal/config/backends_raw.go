package config

// #655 provider-delete bug: viper re-nests dotted backend names.
// A YAML key like `backends.kimi-k2.7-code:` is treated by viper's key
// flattening as the nested path backends → kimi-k2 → 7-code, so v.Unmarshal
// materializes a GHOST backend named "kimi-k2" (empty fields) and DROPS the
// real `kimi-k2.7-code` entry. The admin UI then lists the ghost, and
// deleting it fails with "backend not found in config file" — the snapshot
// and the YAML on disk disagree.
//
// Fix mirrors loadModelsNamespace: re-derive cfg.Backends from the raw YAML
// bytes (yaml.v3 preserves dotted keys as flat map keys) and decode them with
// mapstructure using the same mapstructure tags viper would use. Only the
// two explicitly bound env overrides for backends (FUSION_MLX_URL,
// FUSION_MLX_API_KEY, bindSecretEnv) are re-applied afterwards so container
// env injection (#143/#488) keeps working.

import (
    "log/slog"
    "os"
    "strings"

    "github.com/go-viper/mapstructure/v2"
    "gopkg.in/yaml.v3"
)

func fixDottedBackendNames(cfg *Config, raw []byte) {
    var doc map[string]interface{}
    if err := yaml.Unmarshal(raw, &doc); err != nil {
        // viper already unmarshaled the same bytes; a yaml.v3 parse failure
        // here would be a yaml.v3-vs-viper tolerance gap. Keep the
        // viper-decoded (dot-split) backends and let validate decide.
        slog.Warn("backends raw parse failed; backends stay viper-decoded", "error", err)
        return
    }
    rawBackends, _ := doc["backends"].(map[string]interface{})
    if rawBackends == nil {
        return
    }
    decoded := map[string]BackendConfig{}
    dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
        Result:      &decoded,
        TagName:     "mapstructure",
        ErrorUnused: false,
        DecodeHook: mapstructure.ComposeDecodeHookFunc(
            mapstructure.StringToTimeDurationHookFunc(),
            mapstructure.StringToSliceHookFunc(","),
        ),
    })
    if err != nil {
        slog.Warn("backends decoder build failed; backends stay viper-decoded", "error", err)
        return
    }
    if err := dec.Decode(rawBackends); err != nil {
        slog.Warn("backends raw decode failed; backends stay viper-decoded", "error", err)
        return
    }
    applyBackendEnvOverrides(decoded)
    cfg.Backends = decoded
    slog.Info("backends loaded (raw yaml path, viper bypass)", "entries", len(decoded))
}

// applyBackendEnvOverrides re-applies the two explicitly bound backend env
// vars after the raw-YAML replacement. bindSecretEnv binds only
// backends.fusion-mlx.base_url (FUSION_MLX_URL) and backends.fusion-mlx.api_key
// (FUSION_MLX_API_KEY); any future backend env binding added to bindSecretEnv
// must be mirrored here or the raw-YAML path will silently drop it.
func applyBackendEnvOverrides(decoded map[string]BackendConfig) {
    bc, ok := decoded["fusion-mlx"]
    if !ok {
        return
    }
    if u := strings.TrimSpace(os.Getenv("FUSION_MLX_URL")); u != "" {
        bc.BaseURL = u
    }
    if k := strings.TrimSpace(os.Getenv("FUSION_MLX_API_KEY")); k != "" {
        bc.APIKey = k
    }
    decoded["fusion-mlx"] = bc
}
