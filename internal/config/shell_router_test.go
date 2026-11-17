package config

import (
    "strings"
    "testing"
)

// #675: static web_shell.manifests entries validate the router vocabulary
// the same way dynamic registration does — empty|hash|path only.
func TestValidateStaticManifestRouterVocabulary(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Shell.Enabled = true
    cfg.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    cfg.Shell.Session.Secret = "test-secret"
    cfg.Shell.Manifests = []ShellManifest{{
        ID: "fusion-site", Title: "Fusion Site", Path: "/site", Entry: "/shell/fusion-site/",
        Devices: []string{"desktop"}, Category: "platform", Sort: 35, Router: "path",
    }}
    if err := validate(&cfg); err != nil {
        t.Fatalf("router=path entry rejected: %v", err)
    }

    cfg.Shell.Manifests[0].Router = "hash"
    if err := validate(&cfg); err != nil {
        t.Fatalf("router=hash entry rejected: %v", err)
    }

    cfg.Shell.Manifests[0].Router = "diagonal"
    err := validate(&cfg)
    if err == nil || !strings.Contains(err.Error(), "router") {
        t.Fatalf("router=diagonal must fail validation, got %v", err)
    }
}
