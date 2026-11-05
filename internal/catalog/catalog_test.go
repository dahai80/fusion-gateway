package catalog

import (
    "strings"
    "testing"
)

// #488 AC: the embedded catalog parses and every entry carries the schema
// fields the shell three-state view needs; ids are unique (they are the
// supervisor/manifest join keys).
func TestCatalog_ValidAndUnique(t *testing.T) {
    entries, err := All()
    if err != nil {
        t.Fatalf("all: %v", err)
    }
    if len(entries) < 10 {
        t.Fatalf("catalog too small: %d entries", len(entries))
    }
    seen := map[string]bool{}
    for _, e := range entries {
        if seen[e.ID] {
            t.Fatalf("duplicate catalog id %q", e.ID)
        }
        seen[e.ID] = true
        for field, val := range map[string]string{
            "id": e.ID, "title_en": e.TitleEn, "title_cn": e.TitleCn,
            "category": e.Category, "repo": e.Repo,
            "description_en": e.DescriptionEn, "description_cn": e.DescriptionCn,
            "footprint": e.Footprint, "model_class": e.ModelClass,
        } {
            if strings.TrimSpace(val) == "" {
                t.Fatalf("entry %q missing %s", e.ID, field)
            }
        }
    }
    // Core-profile ids must exist — the join view depends on them (#490).
    for _, core := range []string{"fusion-mlx", "fusion-gateway", "fusion-supervisor", "fusion-web-shell", "fusion-cli"} {
        if !seen[core] {
            t.Fatalf("core product %q missing from catalog", core)
        }
    }
}

// #518 AC: entries whose repo URL has no real remote must be marked
// installable=false so install clients can filter them; every other entry
// defaults to installable (missing flag = clonable repo).
func TestCatalog_LocalOnlyEntriesNotInstallable(t *testing.T) {
    entries, err := All()
    if err != nil {
        t.Fatalf("all: %v", err)
    }
    marked := 0
    for _, e := range entries {
        if e.IsInstallable() {
            continue
        }
        marked++
        t.Logf("local-only: %s -> %s", e.ID, e.Repo)
    }
    if marked == 0 {
        t.Fatalf("no installable=false entries; ai-fit/ai-charactervoice must be marked (#518)")
    }
    // Default-installer semantics: entries without the flag are installable.
    if entries[0].Installable != nil {
        t.Fatalf("flagged entry %q in first position — default (omitted) entries must omit installable", entries[0].ID)
    }
}
