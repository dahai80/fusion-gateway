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
