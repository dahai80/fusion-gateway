// unknown-keys validator regression tests (audit 1019 config-simplification)
// Verifies warnUnknownKeys flags the audit's confirmed invalid keys and stays
// silent on verified-valid keys (dynamic maps, bnup_*, guard_*, api_key).
package config

import (
    "bytes"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func writeTempYAML(t *testing.T, content string) string {
    t.Helper()
    p := filepath.Join(t.TempDir(), "cfg.yaml")
    if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
        t.Fatal(err)
    }
    return p
}

const unknownKeysFixture = `
admin:
    enabled: true
    jwt_secret: 6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b
    users:
        admin: pw-0123456789
auth:
    enabled: true
    master_key: a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8
    api_keys:
        - key: ak-test-0123456789abcdef
          name: n1
server:
    enable_pprof: false
route:
    mode: hybrid
hardware:
    enabled: true
    interval: 5
    enable_iokit: false
    mlx_metrics:
        enabled: true
observability:
    metrics_enabled: true
    namespace: fusion_gateway
routing:
    mode: local
    token_threshold: 8000
    bnup_grammar: bnup-socratic
    bnup_mode: false
    bnup_model: m
    intent_classifier:
        enabled: false
        api_key: ""
        guard_jailbreak_threshold: 0.5
    circuit_breaker:
        failure_threshold: 5
        memory_overload: false
    fallback:
        enabled: true
        model_mapping:
            claude-fable-5: glm5.2
web_shell:
    valid: true
    modules:
        fusion-mlx:
            upstream: http://127.0.0.1:1
            api_key: x
`

func loadCapturingWarns(t *testing.T) string {
    t.Helper()
    var buf bytes.Buffer
    h := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
    old := slog.Default()
    slog.SetDefault(h)
    t.Cleanup(func() { slog.SetDefault(old) })
    p := writeTempYAML(t, unknownKeysFixture)
    if _, err := Load(p); err != nil {
        t.Fatalf("load: %v", err)
    }
    return buf.String()
}

// audit 1019: each of these keys shipped in config.yaml but has no matching
// mapstructure field — warnUnknownKeys must surface every one at load time.
func TestWarnUnknownKeys_FlagsAuditKeys(t *testing.T) {
    out := loadCapturingWarns(t)
    for _, want := range []string{
        "key=route",
        "key=hardware.interval",
        "key=hardware.enable_iokit",
        "key=observability.namespace",
        "key=routing.circuit_breaker.memory_overload",
        "key=web_shell.valid",
    } {
        if !strings.Contains(out, want) {
            t.Errorf("warn output missing %q\ngot:\n%s", want, out)
        }
    }
    for _, banned := range []string{
        "bnup_grammar",
        "bnup_mode",
        "bnup_model",
        "intent_classifier.api_key",
        "guard_jailbreak_threshold",
        "model_mapping",
        "web_shell.modules",
        "api_keys",
        "enable_pprof",
    } {
        if strings.Contains(out, banned) {
            t.Errorf("warn output must not flag verified-valid key %q\ngot:\n%s", banned, out)
        }
    }
}

// audit 1019: a schema-conformant minimal config must load with zero
// unknown-key warnings — no false positives on the clean path.
func TestWarnUnknownKeys_CleanConfigNoWarn(t *testing.T) {
    var buf bytes.Buffer
    h := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
    old := slog.Default()
    slog.SetDefault(h)
    t.Cleanup(func() { slog.SetDefault(old) })
    p := writeTempYAML(t, `
admin:
    enabled: true
    jwt_secret: 6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b
auth:
    enabled: true
    master_key: a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8
routing:
    mode: local
`)
    if _, err := Load(p); err != nil {
        t.Fatalf("load: %v", err)
    }
    if strings.Contains(buf.String(), "unknown config key") {
        t.Errorf("clean config must not warn; got:\n%s", buf.String())
    }
}
