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

// #412: an ambient FUSION_CLIENT_KEY (the ecosystem gateway CLIENT credential,
// exported by every client shell — config-conventions §1) must neither fail
// startup nor override the mlx backend key. v0.9.89 bound this name as the
// backend key and hard-failed start.sh on every convention-compliant machine.
// This is the user-visible repro: .env carries FUSION_MLX_API_KEY=fg-admin-key,
// the shell carries FUSION_CLIENT_KEY=<gateway-issued key>, config.yaml carries
// the mlx key.
func TestFusionMLXKeySource_AmbientClientKeyDoesNotFailOrOverride(t *testing.T) {
    t.Setenv("FUSION_MLX_API_KEY", "fg-admin-key")
    t.Setenv("FUSION_CLIENT_KEY", "d2540a9e72b1e5c0a42370de434122dbc931f511aa0f7eae")
    f := writeKeySourceConfig(t, "fg-admin-key")
    snap, err := Load(f)
    if err != nil {
        t.Fatalf("expected ambient FUSION_CLIENT_KEY to load without error (#412), got: %v", err)
    }
    if got := snap.Config.Backends["fusion-mlx"].APIKey; got != "fg-admin-key" {
        t.Fatalf("expected the deliberate mlx key to win, got %q", got)
    }
}

// #412: the deliberate injection name stays fail-loud. An ambient client key
// present at the same time changes nothing.
func TestFusionMLXKeySource_DeliberateEnvStillFailLoud(t *testing.T) {
    t.Setenv("FUSION_CLIENT_KEY", "client-key")
    t.Setenv("FUSION_MLX_API_KEY", "file-key")
    f := writeKeySourceConfig(t, "file-key")
    if _, err := Load(f); err != nil {
        t.Fatalf("expected matching deliberate env to load, got: %v", err)
    }
    t.Setenv("FUSION_MLX_API_KEY", "rotated-key")
    _, err := Load(f)
    if err == nil {
        t.Fatal("expected FUSION_MLX_API_KEY mismatch to fail Load (#363 F1)")
    }
    if !strings.Contains(err.Error(), "DIFFERENT values") {
        t.Fatalf("expected mismatch error naming FUSION_MLX_API_KEY, got: %v", err)
    }
}

// #412: an api_key_env: FUSION_CLIENT_KEY reference in config.yaml still
// resolves the mlx backend key from the client env (#409 acceptance).
func TestFusionMLXKeySource_ClientKeyViaAPIKeyEnvLoads(t *testing.T) {
    t.Setenv("FUSION_MLX_API_KEY", "")
    t.Setenv("FUSION_CLIENT_KEY", "client-env-key")
    f := filepath.Join(t.TempDir(), "config.yaml")
    content := `
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
    api_key_env: "FUSION_CLIENT_KEY"
    enabled: true
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    snap, err := Load(f)
    if err != nil {
        t.Fatalf("expected api_key_env: FUSION_CLIENT_KEY to load, got: %v", err)
    }
    if got := snap.Config.Backends["fusion-mlx"].APIKey; got != "client-env-key" {
        t.Fatalf("expected client env key to resolve via api_key_env, got %q", got)
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
