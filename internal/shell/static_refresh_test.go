package shell

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func staticManifest(id string) config.ShellManifest {
    return config.ShellManifest{
        ID: id, Title: id, Path: "/" + id,
        Entry: "/shell/" + id + "/",
        Devices: []string{"desktop"}, Category: "platform",
    }
}

func staticIDs(t *testing.T, reg *Registry) map[string]bool {
    t.Helper()
    out := map[string]bool{}
    for _, v := range reg.List() {
        out[v.ID] = true
    }
    return out
}

// #453: web_shell.manifests static entries were read once at server
// construction; a config reload updated the snapshot but not the registry, so
// a module added at runtime stayed invisible in the sidebar until a restart.
// ReplaceStatic converges the static fallback list on reload.
func TestReplaceStatic_AddsAndRemoves(t *testing.T) {
    reg := NewRegistry([]config.ShellManifest{staticManifest("fusion-mod-a")}, nil, 30*time.Second)

    reg.ReplaceStatic([]config.ShellManifest{staticManifest("fusion-mod-a"), staticManifest("fusion-mod-b")})
    got := staticIDs(t, reg)
    if !got["fusion-mod-a"] || !got["fusion-mod-b"] {
        t.Fatalf("after adding fusion-mod-b, list = %v, want both fusion-mod-a and fusion-mod-b", got)
    }

    reg.ReplaceStatic([]config.ShellManifest{staticManifest("fusion-mod-b")})
    got = staticIDs(t, reg)
    if got["fusion-mod-a"] || !got["fusion-mod-b"] {
        t.Fatalf("after removing fusion-mod-a, list = %v, want only fusion-mod-b", got)
    }
}

// A surviving id must keep its health observation across a reload; a removed
// id's observation must be pruned so a later re-add starts offline.
func TestReplaceStatic_HealthLifecycle(t *testing.T) {
    reg := NewRegistry([]config.ShellManifest{staticManifest("fusion-mod-a")}, nil, 30*time.Second)
    reg.RecordHealth("fusion-mod-a", true)

    reg.ReplaceStatic([]config.ShellManifest{staticManifest("fusion-mod-a"), staticManifest("fusion-mod-b")})
    statuses := map[string]Status{}
    for _, v := range reg.List() {
        statuses[v.ID] = v.Status
    }
    if statuses["fusion-mod-a"] != StatusOnline {
        t.Fatalf("surviving fusion-mod-a status = %q, want online (health must survive reload)", statuses["fusion-mod-a"])
    }
    if statuses["fusion-mod-b"] != StatusOffline {
        t.Fatalf("new fusion-mod-b status = %q, want offline (never probed)", statuses["fusion-mod-b"])
    }

    reg.ReplaceStatic(nil)
    reg.ReplaceStatic([]config.ShellManifest{staticManifest("fusion-mod-a")})
    for _, v := range reg.List() {
        if v.ID == "fusion-mod-a" && v.Status != StatusOffline {
            t.Fatalf("re-added fusion-mod-a status = %q, want offline (pruned health must not resurrect)", v.Status)
        }
    }
}

// A live dynamic registration keeps precedence over its static fallback
// entry after a static refresh.
func TestReplaceStatic_DynamicWins(t *testing.T) {
    reg := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    m := staticManifest("fusion-mod-a")
    m.Upstream = "http://127.0.0.1:19000"
    if err := reg.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }

    reg.ReplaceStatic([]config.ShellManifest{staticManifest("fusion-mod-a")})
    views := reg.List()
    if len(views) != 1 {
        t.Fatalf("expected 1 view (dynamic overrides static), got %d", len(views))
    }
    if !views[0].Registered {
        t.Fatalf("expected the dynamic registration to win, got static entry")
    }
}
