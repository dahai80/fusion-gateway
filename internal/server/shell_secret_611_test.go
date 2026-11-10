package server

import (
    "bytes"
    "log/slog"
    "strings"
    "testing"
)

// #611: the shell session signing secret has two sources — the explicit
// shell.session.secret and a domain-separated digest of auth.master_key.
// Only the derived branch logged its source, so an operator could not tell
// from the log WHICH value to mint session tokens with. Both branches must
// log the source + non-secret fingerprint at the same visibility.

func shellSecretLog611(t *testing.T, sessionSecret, masterKey string) *bytes.Buffer {
    t.Helper()
    var logBuf bytes.Buffer
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
    defer slog.SetDefault(prev)
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Session.Secret = sessionSecret
    s.cfg.Config.Auth.MasterKey = masterKey
    s.setupShell()
    return &logBuf
}

func Test611_ShellSecretSource_ConfigFile(t *testing.T) {
    logBuf := shellSecretLog611(t, "explicit-session-secret", "some-master-key")
    out := logBuf.String()
    if !strings.Contains(out, "shell session secret resolved") {
        t.Fatalf("expected shell session secret source log, log:\n%s", out)
    }
    if !strings.Contains(out, `source="config file"`) {
        t.Fatalf("config-file branch must log its source, log:\n%s", out)
    }
}

func Test611_ShellSecretSource_Derived(t *testing.T) {
    logBuf := shellSecretLog611(t, "", "derived-source-master-key")
    out := logBuf.String()
    if !strings.Contains(out, "shell session secret resolved") {
        t.Fatalf("expected shell session secret source log, log:\n%s", out)
    }
    if !strings.Contains(out, "derived from auth.master_key") {
        t.Fatalf("derived branch must log its source, log:\n%s", out)
    }
    // The old derived-only line must not double-log.
    if strings.Contains(out, "shell session secret derived from master_key (domain-separated digest)") {
        t.Fatalf("legacy derived line must be replaced, log:\n%s", out)
    }
}
