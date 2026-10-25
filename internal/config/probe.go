package config

import (
    "bytes"
    "fmt"

    "github.com/spf13/viper"
)

// ProbeValidate dry-runs the full Load parse pipeline (seed defaults →
// unmarshal → models.* raw load → normalize → validate) against config YAML
// bytes WITHOUT touching the global snapshot or the version counter. Used by
// the admin config-write path to validate a mutation BEFORE it lands on
// disk — the old order (write → Reload) left broken YAML on config.yaml when
// the mutation failed validation, with a stale live snapshot and a manual
// file repair as the only way out (production: finding-apply wrote a
// models.supply entry the load path could not re-read).
func ProbeValidate(data []byte) error {
    v := viper.New()
    v.SetConfigType("yaml")
    v.AutomaticEnv()
    bindSecretEnv(v)
    if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
        return fmt.Errorf("read config: %w", err)
    }
    cfg := DefaultConfig()
    if cfg.Admin != nil {
        cfg.Admin.JWTSecret = ""
    }
    if err := v.Unmarshal(&cfg); err != nil {
        return fmt.Errorf("unmarshal config: %w", err)
    }
    loadModelsNamespace(&cfg, data)
    normalizeBackends(&cfg)
    if err := validate(&cfg); err != nil {
        return fmt.Errorf("validate config: %w", err)
    }
    return nil
}
