package config

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// #493 AC: tier enum validation — local|lan only; unknown tier is a startup
// error, not a silent fallback.
func TestNetworkTier_ValidateEnum(t *testing.T) {
    dir := t.TempDir()
    for tier, wantErr := range map[string]bool{
        "local": false, "lan": false, "public": true, "wan": true, "": false,
    } {
        path := filepath.Join(dir, "config.yaml")
        yaml := "network:\n  tier: " + tier + "\n"
        // lan carries the hard auth gate: a tier-only YAML would fail for the
        // missing auth plane, which TestNetworkTier_LanRequiresAuth covers.
        if tier == "lan" {
            yaml += "auth:\n  enabled: true\n  master_key: real-key\n"
        }
        if tier == "" {
            yaml = "server:\n  port: 11432\n"
        }
        if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
            t.Fatal(err)
        }
        cfg, err := Load(path)
        if wantErr {
            if err == nil {
                t.Fatalf("tier %q must fail validation", tier)
            }
            if !strings.Contains(err.Error(), "network.tier") {
                t.Fatalf("error must name the key, got %v", err)
            }
            continue
        }
        if err != nil {
            t.Fatalf("tier %q must load, got %v", tier, err)
        }
        want := tier
        if want == "" {
            want = "local"
        }
        if cfg.Config.Network.Tier != want {
            t.Fatalf("tier = %q, want %q", cfg.Config.Network.Tier, want)
        }
    }
}

// #493 AC: FUSION_NETWORK_TIER overrides the file value (env-first, same
// precedence family as the other injected settings).
func TestNetworkTier_EnvOverride(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(path, []byte("network:\n  tier: local\nauth:\n  enabled: true\n  master_key: real-key\n"), 0600); err != nil {
        t.Fatal(err)
    }
    t.Setenv("FUSION_NETWORK_TIER", "lan")
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if cfg.Config.Network.Tier != "lan" {
        t.Fatalf("env must override file, got %q", cfg.Config.Network.Tier)
    }
}

// #493 AC: FUSION_ADMIN_KEY binds auth.master_key (management plane,
// env-first); FG_MASTER_KEY stays as the k8s injection name.
func TestAdminKey_EnvBind(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(path, []byte("auth:\n  enabled: true\n  master_key: file-key\n"), 0600); err != nil {
        t.Fatal(err)
    }
    t.Setenv("FUSION_ADMIN_KEY", "admin-env-key")
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if cfg.Config.Auth.MasterKey != "admin-env-key" {
        t.Fatalf("FUSION_ADMIN_KEY must bind auth.master_key, got %q", cfg.Config.Auth.MasterKey)
    }
}

// #493 AC: FUSION_SERVICE_KEY_<MODULE> envs overlay web_shell.service_keys;
// the module id lowercases and drops the prefix (FUSION_SERVICE_KEY_FUSION_RAG -> fusion-rag).
func TestServiceKey_EnvOverlay(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(path, []byte("web_shell:\n  service_keys:\n    fusion-doc: file-doc-key\n"), 0600); err != nil {
        t.Fatal(err)
    }
    t.Setenv("FUSION_SERVICE_KEY_FUSION_RAG", "env-rag-key")
    cfg, err := Load(path)
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if cfg.Config.Shell.ServiceKeys["fusion-rag"] != "env-rag-key" {
        t.Fatalf("env service key missing: %v", cfg.Config.Shell.ServiceKeys)
    }
    if cfg.Config.Shell.ServiceKeys["fusion-doc"] != "file-doc-key" {
        t.Fatalf("file service key must survive: %v", cfg.Config.Shell.ServiceKeys)
    }
}

// #493 AC: lan tier with a disabled auth plane fails validation, naming the
// anonymous surface — fail visibly before the listener binds.
func TestNetworkTier_LanRequiresAuth(t *testing.T) {
    dir := t.TempDir()
    for name, yaml := range map[string]string{
        "auth_disabled":  "network:\n  tier: lan\nauth:\n  enabled: false\n",
        "passthrough":    "network:\n  tier: lan\nauth:\n  enabled: true\n  passthrough: true\n",
        "no_credentials": "network:\n  tier: lan\nauth:\n  enabled: true\n  master_key: \"\"\n",
    } {
        path := filepath.Join(dir, name+".yaml")
        if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
            t.Fatal(err)
        }
        _, err := Load(path)
        if err == nil {
            t.Fatalf("%s: lan tier must fail without an auth plane", name)
        }
        if !strings.Contains(err.Error(), "anonymous") {
            t.Fatalf("%s: error must name the anonymous surface, got %v", name, err)
        }
    }
    // A lan tier with credentials loads clean.
    path := filepath.Join(dir, "ok.yaml")
    ok := "network:\n  tier: lan\nauth:\n  enabled: true\n  master_key: real-key\n"
    if err := os.WriteFile(path, []byte(ok), 0600); err != nil {
        t.Fatal(err)
    }
    if _, err := Load(path); err != nil {
        t.Fatalf("lan with master key must load, got %v", err)
    }
}
