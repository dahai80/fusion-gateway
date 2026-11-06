package config

import (
    "bytes"
    "crypto/sha256"
    "encoding/hex"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// #536: bindSecretEnv silently replaces auth.master_key from config.yaml with
// FG_MASTER_KEY / FUSION_ADMIN_KEY. Derivations that assume the file value
// (shell session secret minting, smoke tooling) then fail with 401 and no
// visible cause. Load/Reload must log the winning source plus a short
// non-secret fingerprint of the effective value.
func captureMasterKeyLog(t *testing.T, fn func()) string {
    t.Helper()
    var buf bytes.Buffer
    old := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
    defer slog.SetDefault(old)
    fn()
    return buf.String()
}

func writeMasterKeyConfig(t *testing.T, key string) string {
    t.Helper()
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    yaml := "auth:\n    master_key: " + key + "\nserver:\n    host: 127.0.0.1\n    port: 11432\n"
    if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
        t.Fatal(err)
    }
    return path
}

func TestLoad_LogsMasterKeyEnvOverride(t *testing.T) {
    path := writeMasterKeyConfig(t, "file-value-key")
    t.Setenv("FG_MASTER_KEY", "env-value-key")

    log := captureMasterKeyLog(t, func() {
        if _, err := Load(path); err != nil {
            t.Fatalf("load: %v", err)
        }
    })
    if !strings.Contains(log, "env FG_MASTER_KEY") {
        t.Fatalf("expected env override source in log, got: %s", log)
    }
    sum := sha256.Sum256([]byte("env-value-key"))
    if !strings.Contains(log, hex.EncodeToString(sum[:8])) {
        t.Fatalf("expected fingerprint of effective env value in log, got: %s", log)
    }
    if strings.Contains(log, "config file") {
        t.Fatalf("env override must not be reported as config file: %s", log)
    }
}

func TestLoad_LogsMasterKeyFallbackEnvName(t *testing.T) {
    path := writeMasterKeyConfig(t, "file-value-key")
    t.Setenv("FG_MASTER_KEY", "")
    t.Setenv("FUSION_ADMIN_KEY", "admin-env-key")

    log := captureMasterKeyLog(t, func() {
        if _, err := Load(path); err != nil {
            t.Fatalf("load: %v", err)
        }
    })
    if !strings.Contains(log, "env FUSION_ADMIN_KEY") {
        t.Fatalf("expected FUSION_ADMIN_KEY override source in log, got: %s", log)
    }
}

func TestLoad_LogsMasterKeyConfigFileSource(t *testing.T) {
    path := writeMasterKeyConfig(t, "file-value-key")
    t.Setenv("FG_MASTER_KEY", "")
    t.Setenv("FUSION_ADMIN_KEY", "")

    log := captureMasterKeyLog(t, func() {
        if _, err := Load(path); err != nil {
            t.Fatalf("load: %v", err)
        }
    })
    if !strings.Contains(log, "config file") {
        t.Fatalf("expected config file source in log, got: %s", log)
    }
    sum := sha256.Sum256([]byte("file-value-key"))
    if !strings.Contains(log, hex.EncodeToString(sum[:8])) {
        t.Fatalf("expected fingerprint of file value in log, got: %s", log)
    }
}

func TestReload_LogsMasterKeySource(t *testing.T) {
    path := writeMasterKeyConfig(t, "file-value-key")
    t.Setenv("FG_MASTER_KEY", "reload-env-key")

    if _, err := Load(path); err != nil {
        t.Fatalf("load: %v", err)
    }
    log := captureMasterKeyLog(t, func() {
        if _, err := Reload(path); err != nil {
            t.Fatalf("reload: %v", err)
        }
    })
    if !strings.Contains(log, "env FG_MASTER_KEY") {
        t.Fatalf("expected env override source on reload, got: %s", log)
    }
}
