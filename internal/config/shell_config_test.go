package config

import (
    "os"
    "path/filepath"
    "testing"
)

func writeShellYAML(t *testing.T, body string) string {
    t.Helper()
    path := filepath.Join(t.TempDir(), "config.yaml")
    if err := os.WriteFile(path, []byte(body), 0600); err != nil {
        t.Fatalf("write config: %v", err)
    }
    return path
}

func baseShellYAML() string {
    return `
auth:
  master_key: "0123456789abcdef0123456789abcdef"
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "http://127.0.0.1:11432"
`
}

func TestShell_LoadDefaultsWhenEnabled(t *testing.T) {
    snap, err := Load(writeShellYAML(t, baseShellYAML()))
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if snap.Config.Shell.HeartbeatTimeout != 30000000000 {
        t.Fatalf("heartbeat_timeout = %s, want 30s", snap.Config.Shell.HeartbeatTimeout)
    }
    if snap.Config.Shell.HealthPollInterval.String() != "30s" {
        t.Fatalf("health_poll_interval = %s", snap.Config.Shell.HealthPollInterval)
    }
    if snap.Config.Shell.Session.TTL.String() != "12h0m0s" {
        t.Fatalf("session.ttl = %s", snap.Config.Shell.Session.TTL)
    }
}

func TestShell_ValidateEmptyAllowlistRejected(t *testing.T) {
    body := baseShellYAML() + "  # remove allowlist"
    body = `
auth:
  master_key: "0123456789abcdef0123456789abcdef"
web_shell:
  enabled: true
`
    _, err := Load(writeShellYAML(t, body))
    if err == nil {
        t.Fatal("shell.enabled with empty entry_origin_allowlist must fail validation (A7)")
    }
}

func TestShell_ValidateNoSigningSourceRejected(t *testing.T) {
    body := `
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "http://127.0.0.1:11432"
`
    _, err := Load(writeShellYAML(t, body))
    if err == nil {
        t.Fatal("shell.enabled without session.secret or master_key must fail validation")
    }
}

func TestShell_ValidateDerivedSecretAccepted(t *testing.T) {
    body := baseShellYAML()
    if _, err := Load(writeShellYAML(t, body)); err != nil {
        t.Fatalf("master_key as signing source should be valid: %v", err)
    }
}

func TestShell_ValidateBadAllowlistOriginRejected(t *testing.T) {
    body := baseShellYAML() + `
  entry_origin_allowlist_override: true
`
    body = `
auth:
  master_key: "0123456789abcdef0123456789abcdef"
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "not-an-origin"
`
    _, err := Load(writeShellYAML(t, body))
    if err == nil {
        t.Fatal("invalid allowlist origin must fail validation")
    }
}

func TestShell_ValidateDuplicateAndEmptyUsers(t *testing.T) {
    body := `
auth:
  master_key: "0123456789abcdef0123456789abcdef"
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "http://127.0.0.1:11432"
  session:
    users:
      - username: admin
        password: "x"
      - username: admin
        password: "y"
`
    if _, err := Load(writeShellYAML(t, body)); err == nil {
        t.Fatal("duplicate shell usernames must fail validation")
    }

    body = `
auth:
  master_key: "0123456789abcdef0123456789abcdef"
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "http://127.0.0.1:11432"
  session:
    users:
      - username: admin
`
    if _, err := Load(writeShellYAML(t, body)); err == nil {
        t.Fatal("user without password must fail validation")
    }
}

func TestShell_ValidateBadModuleUpstreamRejected(t *testing.T) {
    body := `
auth:
  master_key: "0123456789abcdef0123456789abcdef"
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "http://127.0.0.1:11432"
  modules:
    fusion-mlx:
      upstream: "not-a-url"
`
    if _, err := Load(writeShellYAML(t, body)); err == nil {
        t.Fatal("invalid module upstream must fail validation")
    }
}

func TestShell_DisabledByDefaultInert(t *testing.T) {
    cfg := DefaultConfig()
    if cfg.Shell.Enabled {
        t.Fatal("shell must default to disabled (fail-closed opt-in gate)")
    }
}
