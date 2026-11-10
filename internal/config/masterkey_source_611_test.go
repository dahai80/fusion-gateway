package config

import (
    "bytes"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// #611: FUSION_ADMIN_KEY / FG_MASTER_KEY silently override auth.master_key
// in config.yaml (bindSecretEnv). Any tooling that mints shell-session
// tokens from the FILE value (the documented derivation) then 401s with no
// visible cause. The conflict must be loud at boot: a WARN naming both
// fingerprints when env overrides a non-empty file value — INFO as today
// when they agree or only one source exists. Env override stays canonical
// (Helm secretKeyRef flow), so this warns, it does not refuse.

const masterKeyFile611 = `
auth:
  master_key: %q
server:
  port: 8113
`

func load611(t *testing.T, yamlMasterKey, envValue string) *bytes.Buffer {
    t.Helper()
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := "server:\n  port: 8113\n"
    if yamlMasterKey != "" {
        content = "auth:\n  master_key: " + yamlMasterKey + "\n" + content
    }
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    var logBuf bytes.Buffer
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
    defer slog.SetDefault(prev)
    if envValue != "" {
        t.Setenv("FUSION_ADMIN_KEY", envValue)
    } else {
        t.Setenv("FUSION_ADMIN_KEY", "")
    }
    if _, err := Load(f); err != nil {
        t.Fatalf("load: %v", err)
    }
    return &logBuf
}

func Test611_EnvOverridingFileMasterKey_Warns(t *testing.T) {
    logBuf := load611(t, "file-master-key-value", "env-master-key-value")
    out := logBuf.String()
    if !strings.Contains(out, "auth.master_key override conflict") {
        t.Fatalf("expected override-conflict WARN, log:\n%s", out)
    }
    if !strings.Contains(out, "FUSION_ADMIN_KEY") {
        t.Fatalf("WARN must name the winning env var, log:\n%s", out)
    }
}

func Test611_EnvMatchesFile_NoWarn(t *testing.T) {
    logBuf := load611(t, "same-key-both-places", "same-key-both-places")
    if strings.Contains(logBuf.String(), "override conflict") {
        t.Fatalf("identical values must not warn, log:\n%s", logBuf.String())
    }
}

func Test611_EnvOnly_NoWarn(t *testing.T) {
    logBuf := load611(t, "", "env-only-key")
    if strings.Contains(logBuf.String(), "override conflict") {
        t.Fatalf("env-only source must not warn, log:\n%s", logBuf.String())
    }
}

func Test611_FileOnly_NoWarn(t *testing.T) {
    logBuf := load611(t, "file-only-key", "")
    if strings.Contains(logBuf.String(), "override conflict") {
        t.Fatalf("file-only source must not warn, log:\n%s", logBuf.String())
    }
}
