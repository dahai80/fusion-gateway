package shell

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func taxonomyManifest(id, category string, sort int) config.ShellManifest {
    return config.ShellManifest{
        ID: id, Title: id, Path: "/" + id, Entry: "/shell/" + id + "/",
        Devices: []string{"desktop"}, Category: category, Sort: sort,
    }
}

// #455 MT-1: product lines (fusion-agent-studio, fusion-design) are `core`,
// admin consoles (fusion-security) are `governance` — the old enum
// (platform|domain|infra|dev) forced product lines into `platform`.
func TestValidateManifest_AcceptsCoreAndGovernance(t *testing.T) {
    for _, c := range []string{"core", "governance", "platform", "domain", "infra", "dev"} {
        if err := validateManifest(taxonomyManifest("fusion-x", c, 1)); err != nil {
            t.Fatalf("category %q rejected: %v", c, err)
        }
    }
    if err := validateManifest(taxonomyManifest("fusion-x", "product", 1)); err == nil {
        t.Fatalf("unknown category accepted")
    }
}

// #455 MT-2: sidebar order is the (sort, id) key — explicit sort wins,
// unsorted entries (sort=0) sink below sorted ones instead of jumping the
// queue, ties break by id so the order is deterministic.
func TestList_OrdersBySortThenID(t *testing.T) {
    reg := NewRegistry([]config.ShellManifest{
        taxonomyManifest("fusion-b-domain", "domain", 31),
        taxonomyManifest("fusion-a-core", "core", 11),
    }, nil, 30*time.Second)
    if err := reg.Upsert(taxonomyManifest("fusion-z-dyn", "domain", 0)); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    var ids []string
    for _, v := range reg.List() {
        ids = append(ids, v.ID)
    }
    want := []string{"fusion-a-core", "fusion-b-domain", "fusion-z-dyn"}
    for i, w := range want {
        if ids[i] != w {
            t.Fatalf("index %d: want %q, got %q (full order: %v)", i, w, ids[i], ids)
        }
    }
}

// #455 MT-4: a module whose health check fails must still appear in the
// aggregation with its health state attached — the shell renders a gray dot,
// it never hides the entry ("menu disappeared" false reports).
func TestList_KeepsUnreachableEntries(t *testing.T) {
    reg := NewRegistry([]config.ShellManifest{taxonomyManifest("fusion-dead", "domain", 31)}, nil, 30*time.Second)
    reg.RecordHealth("fusion-dead", false)
    views := reg.List()
    if len(views) != 1 {
        t.Fatalf("unreachable entry dropped from aggregation, got %d views", len(views))
    }
    if views[0].Status != StatusOffline || !views[0].Unreachable {
        t.Fatalf("unreachable entry status = %q unreachable=%v, want offline+unreachable (health state must be attached)", views[0].Status, views[0].Unreachable)
    }
}

// #455 MT-1/MT-2: taxonomy (category/sort) is a deployment-side decision —
// when a dynamic registration carries its own values, they still lose to
// the static fallback entry for the same id (a module heartbeat must not
// flip the operator's classification or sink core/11 to the sidebar bottom
// with platform/0). Ids without a static entry keep the dynamic values.
func TestList_StaticTaxonomyWinsOverDynamic(t *testing.T) {
    reg := NewRegistry([]config.ShellManifest{
        taxonomyManifest("fusion-agent-studio", "core", 11),
    }, nil, 30*time.Second)

    if err := reg.Upsert(taxonomyManifest("fusion-agent-studio", "platform", 0)); err != nil {
        t.Fatalf("upsert dynamic: %v", err)
    }
    if err := reg.Upsert(taxonomyManifest("fusion-standalone", "domain", 31)); err != nil {
        t.Fatalf("upsert standalone: %v", err)
    }

    got := map[string]config.ShellManifest{}
    for _, v := range reg.List() {
        got[v.ID] = v.ShellManifest
    }
    if m := got["fusion-agent-studio"]; m.Category != "core" || m.Sort != 11 {
        t.Fatalf("agent-studio taxonomy = (%q, %d), want (core, 11) — static entry is the authority, dynamic registration must not override it", m.Category, m.Sort)
    }
    if m := got["fusion-standalone"]; m.Category != "domain" || m.Sort != 31 {
        t.Fatalf("standalone taxonomy = (%q, %d), want (domain, 31) — no static entry, dynamic values must survive", m.Category, m.Sort)
    }
}
