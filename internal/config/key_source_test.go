package config

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// #363 F1: FUSION_MLX_API_KEY env previously overrode the config.yaml
// backends.fusion-mlx.api_key silently (BindEnv precedence). These guards pin
// the new fail-loud contract of checkFusionMLXKeySource:
//   - both set + different → Load/Reload hard error
//   - both set + identical → loads (production baseline: both carry the
//     ecosystem default key)
//   - env-only / file-only → loads
//
// #409: FUSION_CLIENT_KEY is the canonical key env; the legacy name stays a
// transition alias. The legacy-name tests blank the canonical env so an
// ambient FUSION_CLIENT_KEY on the dev machine cannot skew the result.

const keySourceYAMLTemplate = `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
backends:
  fusion-mlx:
    type: "fusion-mlx"
    base_url: "http://127.0.0.1:11434"
    api_key: "%s"
    enabled: true
`

func writeKeySourceConfig(t *testing.T, mlxKey string) string {
    t.Helper()
    f := filepath.Join(t.TempDir(), "config.yaml")
    content := keySourceYAMLTemplate
    if mlxKey == "" {
        content = `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
`
    } else {
        content = strings.ReplaceAll(content, "%s", mlxKey)
    }
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    return f
}

func TestFusionMLXKeySource_MismatchFailsLoad(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "")
    t.Setenv("FUSION_MLX_API_KEY", "env-rotated-key")
    f := writeKeySourceConfig(t, "file-stale-key")
    _, err := Load(f)
    if err == nil {
        t.Fatal("expected Load to fail when FUSION_MLX_API_KEY differs from the config.yaml value (#363 F1)")
    }
    if !strings.Contains(err.Error(), "DIFFERENT values") {
        t.Fatalf("expected mismatch error, got: %v", err)
    }
}

func TestFusionMLXKeySource_IdenticalLoads(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "")
    t.Setenv("FUSION_MLX_API_KEY", "same-key")
    f := writeKeySourceConfig(t, "same-key")
    if _, err := Load(f); err != nil {
        t.Fatalf("expected identical file+env keys to load, got: %v", err)
    }
}

func TestFusionMLXKeySource_EnvOnlyLoads(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "")
    t.Setenv("FUSION_MLX_API_KEY", "env-only-key")
    f := writeKeySourceConfig(t, "")
    if _, err := Load(f); err != nil {
        t.Fatalf("expected env-only key to load, got: %v", err)
    }
}

// #409: the canonical FUSION_CLIENT_KEY wins over the legacy alias, and the
// fail-loud contract holds identically through the canonical name.
func TestFusionMLXKeySource_CanonicalWinsOverLegacy(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "canonical-key")
    t.Setenv("FUSION_MLX_API_KEY", "legacy-key")
    f := writeKeySourceConfig(t, "legacy-key") // file matches the LEGACY env
    _, err := Load(f)
    if err == nil {
        t.Fatal("expected Load to fail: canonical env differs from the file value even though the legacy alias matches")
    }
    if !strings.Contains(err.Error(), "DIFFERENT values") {
        t.Fatalf("expected mismatch error, got: %v", err)
    }
}

func TestFusionMLXKeySource_CanonicalMismatchNamesEnv(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "canonical-rotated-key")
    f := writeKeySourceConfig(t, "file-stale-key")
    _, err := Load(f)
    if err == nil {
        t.Fatal("expected Load to fail when FUSION_CLIENT_KEY differs from the config.yaml value")
    }
    if !strings.Contains(err.Error(), "DIFFERENT values") || !strings.Contains(err.Error(), "FUSION_CLIENT_KEY") {
        t.Fatalf("expected mismatch error naming FUSION_CLIENT_KEY, got: %v", err)
    }
}

func TestFusionMLXKeySource_CanonicalIdenticalLoads(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "same-canonical-key")
    f := writeKeySourceConfig(t, "same-canonical-key")
    if _, err := Load(f); err != nil {
        t.Fatalf("expected identical file+canonical-env keys to load, got: %v", err)
    }
}

func TestFusionMLXKeySource_FileOnlyLoads(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "")
    if orig, ok := os.LookupEnv("FUSION_MLX_API_KEY"); ok {
        t.Setenv("FUSION_MLX_API_KEY", orig)
        os.Unsetenv("FUSION_MLX_API_KEY")
        t.Cleanup(func() { os.Setenv("FUSION_MLX_API_KEY", orig) })
    }
    f := writeKeySourceConfig(t, "file-only-key")
    if _, err := Load(f); err != nil {
        t.Fatalf("expected file-only key to load, got: %v", err)
    }
}

func TestFusionMLXKeySource_MismatchFailsReload(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "")
    t.Setenv("FUSION_MLX_API_KEY", "env-rotated-key")
    f := writeKeySourceConfig(t, "file-stale-key")
    _, err := Reload(f)
    if err == nil {
        t.Fatal("expected Reload to reject the same mismatch (#363 F1)")
    }
    if !strings.Contains(err.Error(), "DIFFERENT values") {
        t.Fatalf("expected mismatch error, got: %v", err)
    }
}
