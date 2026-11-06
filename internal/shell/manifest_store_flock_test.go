package shell

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sync"
    "testing"
    "time"
)

// storeIDs reads the persisted ids from a store file.
func storeIDs(t *testing.T, path string) map[string]bool {
    t.Helper()
    raw, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read store: %v", err)
    }
    var persisted []persistedManifest
    if err := json.Unmarshal(raw, &persisted); err != nil {
        t.Fatalf("parse store: %v", err)
    }
    ids := make(map[string]bool, len(persisted))
    for _, p := range persisted {
        ids[p.ShellManifest.ID] = true
    }
    return ids
}

// Regression for #542: two gateway processes sharing one store file must not
// clobber each other's registrations. The second registry adopts foreign ids
// on save instead of rewriting the file from its own stale snapshot.
func TestManifestStore_TwoProcessesNoClobber(t *testing.T) {
    path := filepath.Join(t.TempDir(), "shell_manifests.json")

    procA := NewRegistry(nil, nil, 30*time.Second)
    procA.SetPersistence(path)
    if err := procA.Upsert(validManifest("fusion-alpha")); err != nil {
        t.Fatalf("A upsert: %v", err)
    }

    procB := NewRegistry(nil, nil, 30*time.Second)
    procB.SetPersistence(path)
    if err := procB.Load(); err != nil {
        t.Fatalf("B load: %v", err)
    }
    // B's snapshot already contains fusion-alpha; save must keep it.
    if err := procB.Upsert(validManifest("fusion-beta")); err != nil {
        t.Fatalf("B upsert: %v", err)
    }
    ids := storeIDs(t, path)
    if !ids["fusion-alpha"] || !ids["fusion-beta"] {
        t.Fatalf("B save dropped a manifest: %v", ids)
    }

    // A's snapshot predates fusion-beta; its next save must adopt it.
    if err := procA.Upsert(validManifest("fusion-gamma")); err != nil {
        t.Fatalf("A upsert: %v", err)
    }
    ids = storeIDs(t, path)
    for _, want := range []string{"fusion-alpha", "fusion-beta", "fusion-gamma"} {
        if !ids[want] {
            t.Fatalf("save lost foreign manifest %q: %v", want, ids)
        }
    }
}

// A deletion by one process must not be resurrected by that same process's
// later saves, even though older store files carried the id.
func TestManifestStore_DeleteNotResurrected(t *testing.T) {
    path := filepath.Join(t.TempDir(), "shell_manifests.json")
    r := NewRegistry(nil, nil, 30*time.Second)
    r.SetPersistence(path)
    if err := r.Upsert(validManifest("fusion-alpha")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if err := r.Remove("fusion-alpha"); err != nil {
        t.Fatalf("remove: %v", err)
    }
    if err := r.Upsert(validManifest("fusion-beta")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    ids := storeIDs(t, path)
    if ids["fusion-alpha"] {
        t.Fatalf("removed manifest resurrected: %v", ids)
    }
    if !ids["fusion-beta"] {
        t.Fatalf("later upsert missing: %v", ids)
    }
}

// Concurrent registrations from two processes sharing the store must all
// survive: the flock serialises saves and the foreign-adoption merge keeps
// the union on disk.
func TestManifestStore_ConcurrentProcessesUnion(t *testing.T) {
    path := filepath.Join(t.TempDir(), "shell_manifests.json")
    procs := []*Registry{
        NewRegistry(nil, nil, 30*time.Second),
        NewRegistry(nil, nil, 30*time.Second),
    }
    for _, r := range procs {
        r.SetPersistence(path)
        if err := r.Load(); err != nil {
            t.Fatalf("load: %v", err)
        }
    }
    var wg sync.WaitGroup
    for pi, r := range procs {
        for i := 0; i < 10; i++ {
            wg.Add(1)
            go func(r *Registry, pi, i int) {
                defer wg.Done()
                if err := r.Upsert(validManifest("fusion-p" + string(rune('a'+pi)) + "-" + string(rune('0'+i)))); err != nil {
                    t.Errorf("upsert: %v", err)
                }
            }(r, pi, i)
        }
    }
    wg.Wait()
    ids := storeIDs(t, path)
    if len(ids) != 20 {
        t.Fatalf("want 20 manifests on disk, got %d: %v", len(ids), ids)
    }
}
