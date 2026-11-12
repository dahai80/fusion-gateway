package server

// #643: shell session signature failures mid-session. The derived secret was
// a per-boot digest of the EFFECTIVE master_key — and the effective source
// flips between the env override (FUSION_ADMIN_KEY, set by fusion-sv) and
// config.yaml (a direct start.sh start) across restart paths. Flip = new
// digest = every live session fails signature verification. Fix: the derived
// digest persists to <data_dir>/shell_session_secret (0600) on first boot and
// reloads afterwards, so sessions survive source flips.

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func config643() config.Config {
    c := config.DefaultConfig()
    c.Shell.Enabled = true
    return c
}

func Test643_DerivedSecretPersistsAndSurvivesKeySourceFlip(t *testing.T) {
    dir := t.TempDir()

    // First boot: env-override master key (fusion-sv path).
    cfgEnv := config643()
    cfgEnv.Auth.MasterKey = "env-override-key-value"
    s1, src1 := buildShellSessionSecret(dir, &cfgEnv)
    if src1 != "derived from auth.master_key (persisted to file, stable across restarts)" {
        t.Fatalf("first boot source = %q", src1)
    }

    // Later boot: env gone, config.yaml key visible instead (start.sh path).
    // The VALUES differ — the pre-fix code re-derived a different digest and
    // killed every live session.
    cfgFile := config643()
    cfgFile.Auth.MasterKey = "different-config-file-key"
    s2, src2 := buildShellSessionSecret(dir, &cfgFile)
    if src2 != "persisted file" {
        t.Fatalf("second boot source = %q", src2)
    }
    if string(s1) != string(s2) {
        t.Fatal("secret changed across a master_key source flip — live sessions would fail signature verification (#643)")
    }

    // The persisted file is 0600.
    fi, err := os.Stat(filepath.Join(dir, "shell_session_secret"))
    if err != nil {
        t.Fatal(err)
    }
    if fi.Mode().Perm() != 0600 {
        t.Fatalf("secret file mode = %v, want 0600", fi.Mode().Perm())
    }
}

func Test643_ExplicitConfigSecretWinsOverFile(t *testing.T) {
    dir := t.TempDir()
    cfg := config643()
    cfg.Shell.Session.Secret = "explicit-operator-secret"
    s, src := buildShellSessionSecret(dir, &cfg)
    if src != "config file" || string(s) != "explicit-operator-secret" {
        t.Fatalf("config branch: %q %q", src, s)
    }
    // Setting the explicit secret is the documented rotation path: it must
    // NOT be poisoned by a previously persisted digest.
    if _, err := os.Stat(filepath.Join(dir, "shell_session_secret")); !os.IsNotExist(err) {
        t.Fatal("config branch must not create a persistence file")
    }
}

func Test643_EmptyDataDirSkipsPersistence(t *testing.T) {
    cfg := config643()
    cfg.Auth.MasterKey = "k1"
    s1, _ := buildShellSessionSecret("", &cfg)
    s2, _ := buildShellSessionSecret("", &cfg)
    if string(s1) != string(s2) {
        t.Fatal("same key must derive the same digest")
    }
    cfg.Auth.MasterKey = "k2"
    s3, _ := buildShellSessionSecret("", &cfg)
    if string(s2) == string(s3) {
        t.Fatal("different keys must derive different digests (legacy semantics preserved)")
    }
}
