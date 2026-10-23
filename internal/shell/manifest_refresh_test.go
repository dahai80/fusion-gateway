package shell

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #284: a heartbeat carrying the module's current manifest must converge the
// stored ext fields (the 7→11 pages drift), persist the change, and keep the
// gateway-issued service key valid — no plaintext re-reveal, key_id stable.
func newRefreshTestRegistry(t *testing.T) *Registry {
    t.Helper()
    reg := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    dir := t.TempDir()
    reg.SetPersistence(filepath.Join(dir, "shell_manifests.json"))
    return reg
}

func refreshBaseManifest(pages int) config.ShellManifest {
    m := config.ShellManifest{
        ID: "fusion-cowork", Title: "Cowork", Path: "/cowork",
        Entry: "http://127.0.0.1:11432/shell/fusion-cowork/",
        Upstream: "http://127.0.0.1:19000",
        Devices:  []string{"desktop"}, Category: "platform",
    }
    for i := 0; i < pages; i++ {
        m.Pages = append(m.Pages, config.ShellPage{
            Title: "Page", Route: "/cowork/page-" + string(rune('a'+i)),
        })
    }
    return m
}

func TestHeartbeatWithManifest_ConvergesExtFields(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    grown := refreshBaseManifest(11)
    if err := reg.HeartbeatWithManifest("fusion-cowork", &grown); err != nil {
        t.Fatalf("heartbeat refresh: %v", err)
    }
    views := reg.List()
    if len(views) != 1 || len(views[0].Pages) != 11 {
        t.Fatalf("expected 11 pages after heartbeat refresh, got %+v", views)
    }
    if views[0].Status != StatusOnline {
        t.Fatalf("refresh must also count as liveness, status = %s", views[0].Status)
    }
}

func TestHeartbeatWithManifest_PersistsRefresh(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    grown := refreshBaseManifest(11)
    if err := reg.HeartbeatWithManifest("fusion-cowork", &grown); err != nil {
        t.Fatalf("heartbeat refresh: %v", err)
    }
    // A fresh registry loading the same store must see the 11-page manifest.
    reg2 := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    reg2.SetPersistence(reg.persistencePath(t))
    if err := reg2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    views := reg2.List()
    if len(views) != 1 || len(views[0].Pages) != 11 {
        t.Fatalf("refreshed manifest must persist across restart, got %+v", views)
    }
}

func TestHeartbeatWithManifest_NilIsPlainHeartbeat(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if err := reg.HeartbeatWithManifest("fusion-cowork", nil); err != nil {
        t.Fatalf("nil manifest must be a plain heartbeat: %v", err)
    }
    views := reg.List()
    if len(views[0].Pages) != 7 {
        t.Fatalf("nil manifest must not touch stored fields, pages = %d", len(views[0].Pages))
    }
}

func TestHeartbeatWithManifest_IdMismatchRejected(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    other := refreshBaseManifest(11)
    other.ID = "fusion-other"
    if err := reg.HeartbeatWithManifest("fusion-cowork", &other); err == nil {
        t.Fatal("id mismatch must be rejected")
    }
}

func TestHeartbeatWithManifest_InvalidManifestRejected(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    bad := refreshBaseManifest(11)
    bad.Category = "not-a-category"
    if err := reg.HeartbeatWithManifest("fusion-cowork", &bad); err == nil {
        t.Fatal("invalid manifest must be rejected")
    }
    views := reg.List()
    if len(views[0].Pages) != 7 {
        t.Fatalf("rejected refresh must not converge, pages = %d", len(views[0].Pages))
    }
}

func TestHeartbeatWithManifest_ServiceKeySurvivesRefresh(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    keyID, plaintext, issued, err := reg.EnsureServiceKey("fusion-cowork")
    if err != nil || !issued || plaintext == "" {
        t.Fatalf("issue service key: issued=%v err=%v", issued, err)
    }

    grown := refreshBaseManifest(11)
    if err := reg.HeartbeatWithManifest("fusion-cowork", &grown); err != nil {
        t.Fatalf("heartbeat refresh: %v", err)
    }
    // Heartbeat auth with the SAME fm- key must still succeed.
    if !reg.AuthServiceKey("fusion-cowork", serviceKeyHashOf(plaintext), 24*time.Hour) {
        t.Fatal("fm- key must survive a manifest refresh via heartbeat")
    }
    if id, _, _, err := reg.EnsureServiceKey("fusion-cowork"); err != nil || id != keyID {
        t.Fatalf("key_id must stay stable, got id=%q want %q err=%v", id, keyID, err)
    }
    // The retained upstream credential must not be wiped (#277).
    upstream, cred, ok := reg.UpstreamFor("fusion-cowork")
    if !ok || upstream == "" || cred != plaintext {
        t.Fatalf("refresh must retain the upstream credential, upstream=%q cred-match=%v", upstream, cred == plaintext)
    }
}

func TestHeartbeatWithManifest_PlainHeartbeatNotPersisted(t *testing.T) {
    reg := newRefreshTestRegistry(t)
    if err := reg.Upsert(refreshBaseManifest(7)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    path := reg.persistencePath(t)
    before, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read store: %v", err)
    }
    for i := 0; i < 3; i++ {
        if err := reg.Heartbeat("fusion-cowork"); err != nil {
            t.Fatalf("plain heartbeat: %v", err)
        }
    }
    after, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("re-read store: %v", err)
    }
    if string(before) != string(after) {
        t.Fatal("plain heartbeat must NOT rewrite the persistence store")
    }
}

// persistencePath is a test-only accessor for the store file path.
func (r *Registry) persistencePath(t *testing.T) string {
    t.Helper()
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.path
}
