package config

import (
    "errors"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// corruptYAML reproduces the 2026-10-27 production incident class: a block
// textually inserted without removing the original, leaving a duplicate
// mapping key (the incident had a second web_shell.modules; this minimal
// fixture duplicates `server:`) — yaml.v3 rejects the duplicate key.
const corruptYAML = `
server:
  port: 11432
cache:
  enabled: true
server:
  port: 11432
`

const goodYAML = `
server:
  port: 11432
cache:
  enabled: true
`

func writeCfg(t *testing.T, path, content string) {
    t.Helper()
    if err := os.WriteFile(path, []byte(content), 0600); err != nil {
        t.Fatal(err)
    }
}

func TestBackupFile_CreatesLastGood(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, goodYAML)

    if err := BackupFile(path); err != nil {
        t.Fatalf("BackupFile: %v", err)
    }
    raw, err := os.ReadFile(LastGoodPath(path))
    if err != nil {
        t.Fatalf("lastgood missing: %v", err)
    }
    if string(raw) != goodYAML {
        t.Fatalf("lastgood content mismatch")
    }
}

func TestBackupFile_MissingSourceNoop(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := BackupFile(path); err != nil {
        t.Fatalf("missing source must be a no-op, got: %v", err)
    }
}

func TestBackupFile_IdenticalContentSkipsRewrite(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, goodYAML)
    if err := BackupFile(path); err != nil {
        t.Fatal(err)
    }
    bak := LastGoodPath(path)
    before, err := os.Stat(bak)
    if err != nil {
        t.Fatal(err)
    }
    if err := BackupFile(path); err != nil {
        t.Fatal(err)
    }
    after, err := os.Stat(bak)
    if err != nil {
        t.Fatal(err)
    }
    if !before.ModTime().Equal(after.ModTime()) {
        t.Fatalf("identical backup must not rewrite the file")
    }
}

func TestLoadWithRecovery_RestoresFromLastGood(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, goodYAML)
    if _, err := LoadWithRecovery(path); err != nil {
        t.Fatalf("seed load: %v", err)
    }
    // The successful load path refreshes .lastgood, so it now exists.
    if _, err := os.Stat(LastGoodPath(path)); err != nil {
        t.Fatalf("lastgood not created: %v", err)
    }

    // Simulate the external textual corruption.
    writeCfg(t, path, corruptYAML)

    snap, err := LoadWithRecovery(path)
    if err != nil {
        t.Fatalf("recovery must succeed, got: %v", err)
    }
    if snap == nil || snap.Config.Server.Port != 11432 {
        t.Fatalf("recovered snapshot invalid")
    }

    // Live file restored to the good content.
    raw, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    if string(raw) != goodYAML {
        t.Fatalf("config file not restored from lastgood")
    }

    // Broken bytes preserved in a quarantine copy.
    matches, _ := filepath.Glob(path + ".corrupt-*")
    if len(matches) != 1 {
        t.Fatalf("expected 1 quarantine copy, got %v", matches)
    }
    qRaw, _ := os.ReadFile(matches[0])
    if string(qRaw) != corruptYAML {
        t.Fatalf("quarantine copy must hold the corrupt bytes")
    }
}

func TestLoadWithRecovery_NoLastGoodIsConfigFatal(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, corruptYAML)

    _, err := LoadWithRecovery(path)
    if err == nil {
        t.Fatal("corrupt config without backup must fail")
    }
    var cfgErr *ConfigFatalError
    if !errors.As(err, &cfgErr) {
        t.Fatalf("error must be ConfigFatalError, got %T: %v", err, err)
    }
}

func TestLoadWithRecovery_BrokenBackupIsConfigFatal(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, corruptYAML)
    writeCfg(t, LastGoodPath(path), "server:\n  port: -1\n")

    _, err := LoadWithRecovery(path)
    if err == nil {
        t.Fatal("broken backup must fail")
    }
    var cfgErr *ConfigFatalError
    if !errors.As(err, &cfgErr) {
        t.Fatalf("error must be ConfigFatalError, got %T: %v", err, err)
    }
}

func TestReloadWithRecovery_RestoresFile(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, goodYAML)
    if _, err := LoadWithRecovery(path); err != nil {
        t.Fatalf("seed load: %v", err)
    }

    writeCfg(t, path, corruptYAML)
    snap, err := ReloadWithRecovery(path)
    if err != nil {
        t.Fatalf("reload recovery must succeed, got: %v", err)
    }
    if snap == nil {
        t.Fatal("recovered snapshot is nil")
    }

    raw, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    if string(raw) != goodYAML {
        t.Fatalf("config file not restored after reload recovery")
    }
}

func TestReloadWithRecovery_NoBackupKeepsError(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, goodYAML)
    if _, err := LoadWithRecovery(path); err != nil {
        t.Fatal(err)
    }
    // Remove the backup created by the load path, then corrupt.
    if err := os.Remove(LastGoodPath(path)); err != nil {
        t.Fatal(err)
    }
    writeCfg(t, path, corruptYAML)

    if _, err := ReloadWithRecovery(path); err == nil {
        t.Fatal("reload recovery without backup must surface the error")
    }
}

func TestLoadWithRecovery_GoodConfigRefreshesBackup(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    writeCfg(t, path, "server:\n  port: 11433\n")
    if _, err := LoadWithRecovery(path); err != nil {
        t.Fatal(err)
    }
    if _, err := os.Stat(LastGoodPath(path)); err != nil {
        t.Fatalf("successful load must create/refresh lastgood: %v", err)
    }
    // Sanity: the duplicate-key corruption is what Load rejects.
    writeCfg(t, path, corruptYAML)
    if _, err := Load(path); err == nil {
        t.Fatal("duplicate mapping key must fail Load")
    } else if !strings.Contains(err.Error(), "server") {
        t.Fatalf("expected duplicate-key error mentioning server, got: %v", err)
    }
}
