package shell

import (
    "testing"
    "time"
)

// #675 AC: the optional router field passes through the manifest registry,
// "path"/"hash" persist, anything else is rejected at registration, and
// older manifests without the field keep working.
func Test675_RouterFieldPassthroughAndValidation(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-site")
    m.Router = "path"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert router=path: %v", err)
    }
    found := false
    for _, v := range r.List() {
        if v.ID == "fusion-site" {
            found = true
            if v.Router != "path" {
                t.Fatalf("router must pass through List, got %q", v.Router)
            }
        }
    }
    if !found {
        t.Fatal("manifest missing from List")
    }

    bad := validManifest("fusion-diagonal")
    bad.Router = "diagonal"
    if err := r.Upsert(bad); err == nil {
        t.Fatal("router=diagonal must be rejected at registration")
    }
    for _, v := range r.List() {
        if v.ID == "fusion-diagonal" {
            t.Fatal("rejected manifest must not be listed")
        }
    }

    hash := validManifest("fusion-hash")
    hash.Router = "hash"
    if err := r.Upsert(hash); err != nil {
        t.Fatalf("upsert router=hash: %v", err)
    }

    legacy := validManifest("fusion-legacy")
    if err := r.Upsert(legacy); err != nil {
        t.Fatalf("legacy manifest (no router) must register: %v", err)
    }
    for _, v := range r.List() {
        if v.ID == "fusion-legacy" && v.Router != "" {
            t.Fatalf("legacy manifest must carry no router field, got %q", v.Router)
        }
    }
}
