package config

import (
    "strings"
    "testing"
)

// #455 MT-1: the category vocabulary is shared between static config
// validation and dynamic manifest registration.
func TestValidManifestCategory(t *testing.T) {
    for _, c := range []string{"core", "governance", "platform", "domain", "infra", "dev"} {
        if !ValidManifestCategory(c) {
            t.Fatalf("category %q must be valid", c)
        }
    }
    for _, c := range []string{"", "product", "business", "Platform"} {
        if ValidManifestCategory(c) {
            t.Fatalf("category %q must be invalid", c)
        }
    }
}

// #455 MT-2: static web_shell.manifests entries require an explicit sort —
// the sidebar order is (sort, id) and file order was never a spec.
func TestValidateStaticManifestRequiresCategoryAndSort(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Shell.Enabled = true
    cfg.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    cfg.Shell.Session.Secret = "test-secret"
    cfg.Shell.Manifests = []ShellManifest{{
        ID: "fusion-x", Title: "X", Path: "/x", Entry: "/shell/fusion-x/",
        Devices: []string{"desktop"}, Category: "core", Sort: 11,
    }}
    if err := validate(&cfg); err != nil {
        t.Fatalf("valid entry rejected: %v", err)
    }

    cfg.Shell.Manifests[0].Category = "product"
    err := validate(&cfg)
    if err == nil || !strings.Contains(err.Error(), "category") {
        t.Fatalf("bad category must fail validation, got %v", err)
    }

    cfg.Shell.Manifests[0].Category = "core"
    cfg.Shell.Manifests[0].Sort = 0
    err = validate(&cfg)
    if err == nil || !strings.Contains(err.Error(), "sort") {
        t.Fatalf("missing sort must fail validation, got %v", err)
    }
}
