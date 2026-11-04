package shell

import (
    "testing"
    "time"
)

// #486 AC: model_capable / suggested_model pass through the manifest
// registry (schema v3 ext-field family, #250/#297 precedent), and older
// manifests without the fields keep working.
func TestManifest_ModelPolicyFieldsPassthrough(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-demo")
    m.ModelCapable = true
    m.SuggestedModel = "mlx/coder"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    found := false
    for _, v := range r.List() {
        if v.ID != "fusion-demo" {
            continue
        }
        found = true
        if !v.ModelCapable || v.SuggestedModel != "mlx/coder" {
            t.Fatalf("model-policy fields must pass through List, got capable=%v suggested=%q", v.ModelCapable, v.SuggestedModel)
        }
    }
    if !found {
        t.Fatal("manifest missing from List")
    }
    // A manifest without the fields (legacy) keeps working: zero values,
    // no error, no fabricated hints.
    legacy := validManifest("fusion-legacy")
    if err := r.Upsert(legacy); err != nil {
        t.Fatalf("legacy upsert: %v", err)
    }
    for _, v := range r.List() {
        if v.ID == "fusion-legacy" && (v.ModelCapable || v.SuggestedModel != "") {
            t.Fatalf("legacy manifest must carry no model-policy fields, got capable=%v suggested=%q", v.ModelCapable, v.SuggestedModel)
        }
    }
    // Dynamic registration persistence round-trips the fields (#230 file).
    path := t.TempDir() + "/shell_manifests.json"
    r2 := NewRegistry(nil, nil, 30*time.Second)
    r2.SetPersistence(path)
    m2 := validManifest("fusion-persist")
    m2.ModelCapable = true
    m2.SuggestedModel = "mlx/persist"
    if err := r2.Upsert(m2); err != nil {
        t.Fatalf("persist upsert: %v", err)
    }
    r3 := NewRegistry(nil, nil, 30*time.Second)
    r3.SetPersistence(path)
    if err := r3.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    seen := false
    for _, v := range r3.List() {
        if v.ID == "fusion-persist" {
            seen = true
            if !v.ModelCapable || v.SuggestedModel != "mlx/persist" {
                t.Fatalf("fields must survive the persistence round-trip, got capable=%v suggested=%q", v.ModelCapable, v.SuggestedModel)
            }
        }
    }
    if !seen {
        t.Fatal("persisted manifest missing after Load")
    }
}
