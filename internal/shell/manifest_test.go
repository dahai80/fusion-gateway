package shell

import (
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func validManifest(id string) config.ShellManifest {
    return config.ShellManifest{
        ID:       id,
        Title:    "Test " + id,
        Path:     "/" + id,
        Entry:    "http://127.0.0.1:11432/shell/" + id + "/",
        Devices:  []string{"desktop"},
        Category: "platform",
    }
}

func TestRegistry_UpsertAndList(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    if err := r.Upsert(validManifest("fusion-mlx")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    views := r.List()
    if len(views) != 1 || views[0].Status != StatusOnline {
        t.Fatalf("want 1 online view, got %+v", views)
    }
    // Idempotent upsert: same id does not duplicate.
    if err := r.Upsert(validManifest("fusion-mlx")); err != nil {
        t.Fatalf("re-upsert: %v", err)
    }
    if views := r.List(); len(views) != 1 {
        t.Fatalf("idempotent upsert duplicated: %d views", len(views))
    }
}

func TestRegistry_HeartbeatExpiryMarksUnreachable(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Millisecond)
    if err := r.Upsert(validManifest("fusion-mlx")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    time.Sleep(40 * time.Millisecond)
    views := r.List()
    if views[0].Status != StatusUnreachable {
        t.Fatalf("want unreachable after heartbeat expiry, got %s", views[0].Status)
    }
    // Entry NOT deleted: still listed.
    if len(views) != 1 {
        t.Fatalf("stale entry was deleted, got %d", len(views))
    }
    if err := r.Heartbeat("fusion-mlx"); err != nil {
        t.Fatalf("heartbeat: %v", err)
    }
    if views := r.List(); views[0].Status != StatusOnline {
        t.Fatalf("want online after heartbeat, got %s", views[0].Status)
    }
}

func TestRegistry_StaticFallback(t *testing.T) {
    staticM := validManifest("fusion-bench")
    r := NewRegistry([]config.ShellManifest{staticM}, nil, 30*time.Second)
    views := r.List()
    if len(views) != 1 || views[0].Status != StatusOffline || views[0].Registered {
        t.Fatalf("want offline unregistered static fallback, got %+v", views)
    }
    // Once dynamically registered the dynamic entry wins; static suppressed.
    if err := r.Upsert(validManifest("fusion-bench")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    views = r.List()
    if len(views) != 1 || views[0].Status != StatusOnline || !views[0].Registered {
        t.Fatalf("want single online dynamic view, got %+v", views)
    }
    // Failed health probe clears observation; successful probe flips a
    // never-registered static entry online.
    r.RecordHealth("fusion-nobody", false)
    r.RecordHealth("fusion-nobody", true)
}

func TestRegistry_StaticFallbackHealthProbeFlipsOnline(t *testing.T) {
    staticM := validManifest("fusion-bench")
    staticM.Health = "http://127.0.0.1:1/health"
    r := NewRegistry([]config.ShellManifest{staticM}, nil, 500*time.Millisecond)
    r.RecordHealth("fusion-bench", true)
    views := r.List()
    if views[0].Status != StatusOnline {
        t.Fatalf("want online after fresh health probe, got %s", views[0].Status)
    }
    time.Sleep(600 * time.Millisecond)
    if views := r.List(); views[0].Status != StatusOffline {
        t.Fatalf("want offline after stale health probe, got %s", views[0].Status)
    }
    r.RecordHealth("fusion-bench", false)
    if views := r.List(); views[0].Status != StatusOffline {
        t.Fatalf("want offline after failed probe, got %s", views[0].Status)
    }
}

func TestRegistry_UpsertValidation(t *testing.T) {
    r := NewRegistry(nil, nil, time.Second)
    cases := []struct {
        name string
        m    config.ShellManifest
    }{
        {"missing id", config.ShellManifest{Title: "x", Path: "/x", Entry: "http://a/b", Devices: []string{"desktop"}, Category: "platform"}},
        {"bad id prefix", validManifest("mlx")},
        {"missing title", func() config.ShellManifest { m := validManifest("fusion-x"); m.Title = ""; return m }()},
        {"missing path", func() config.ShellManifest { m := validManifest("fusion-x"); m.Path = ""; return m }()},
        {"missing entry", func() config.ShellManifest { m := validManifest("fusion-x"); m.Entry = ""; return m }()},
        {"missing devices", func() config.ShellManifest { m := validManifest("fusion-x"); m.Devices = nil; return m }()},
        {"bad category", func() config.ShellManifest { m := validManifest("fusion-x"); m.Category = "junk"; return m }()},
        {"bad permissions_policy feature", func() config.ShellManifest { m := validManifest("fusion-x"); m.PermissionsPolicy = []string{"geolocation"}; return m }()},
        {"bad group id uppercase", func() config.ShellManifest { m := validManifest("fusion-x"); m.Group = "FusionOffice"; return m }()},
        {"bad group id too long", func() config.ShellManifest { m := validManifest("fusion-x"); m.Group = strings.Repeat("a", 65); return m }()},
        {"group title too long", func() config.ShellManifest { m := validManifest("fusion-x"); m.Group = "fusion-office"; m.GroupTitle = strings.Repeat("标", 129); return m }()},
    }
    for _, tc := range cases {
        if err := r.Upsert(tc.m); err == nil {
            t.Errorf("%s: want error, got nil", tc.name)
        }
    }
}

func TestRegistry_GroupFieldsRoundTrip(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, time.Second)
    m := validManifest("fusion-doc")
    m.Group = "fusion-office"
    m.GroupTitle = "Fusion Office"
    m.GroupTitleEn = "Fusion Office"
    m.Sort = 3
    if err := r.Upsert(m); err != nil {
        t.Fatalf("grouped manifest rejected: %v", err)
    }
    plain := validManifest("fusion-solo")
    if err := r.Upsert(plain); err != nil {
        t.Fatalf("ungrouped manifest rejected: %v", err)
    }
    var grouped, ungrouped *ManifestView
    // List() once: map iteration order is randomized per call, so indexing
    // a fresh List() with a previous call's i can visit one view twice and
    // skip the other (race-mode flake, caught by CI on an unrelated PR).
    views := r.List()
    for i, v := range views {
        switch v.ID {
        case "fusion-doc":
            grouped = &views[i]
        case "fusion-solo":
            ungrouped = &views[i]
        }
    }
    if grouped == nil || ungrouped == nil {
        t.Fatal("expected both manifests listed")
    }
    if grouped.Group != "fusion-office" || grouped.GroupTitle != "Fusion Office" || grouped.GroupTitleEn != "Fusion Office" || grouped.Sort != 3 {
        t.Fatalf("group fields not passed through verbatim: %+v", grouped.ShellManifest)
    }
    if ungrouped.Group != "" || ungrouped.GroupTitle != "" || ungrouped.GroupTitleEn != "" || ungrouped.Sort != 0 {
        t.Fatalf("ungrouped manifest should carry no group fields: %+v", ungrouped.ShellManifest)
    }
}

func TestRegistry_PermissionsPolicyAllowed(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, time.Second)
    m := validManifest("fusion-voice")
    m.PermissionsPolicy = []string{"microphone", "camera"}
    if err := r.Upsert(m); err != nil {
        t.Fatalf("allowed features rejected: %v", err)
    }
    views := r.List()
    if len(views) != 1 {
        t.Fatalf("expected 1 view, got %d", len(views))
    }
}

func TestRegistry_CredentialFor(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, time.Second)
    if _, ok := r.CredentialFor("fusion-none"); ok {
        t.Fatal("unknown id should report ok=false")
    }
    m := validManifest("fusion-agent-studio")
    m.Upstream = "http://127.0.0.1:8080"
    m.APIKey = "module-supplied-key"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert failed: %v", err)
    }
    key, ok := r.CredentialFor("fusion-agent-studio")
    if !ok || key != "module-supplied-key" {
        t.Fatalf("want module-supplied-key, got %q ok=%v", key, ok)
    }
    // Issuance keeps CredentialFor in sync with the fm- plaintext (#294): the
    // proxy reads this after a rotation.
    _, plaintext, issued, err := r.EnsureServiceKey("fusion-agent-studio")
    if err != nil || !issued {
        t.Fatalf("issue failed: err=%v issued=%v", err, issued)
    }
    key, ok = r.CredentialFor("fusion-agent-studio")
    if !ok || key != plaintext {
        t.Fatalf("want issued fm- key after EnsureServiceKey, got %q ok=%v", key, ok)
    }
    if _, plaintext2, issued2, _ := r.EnsureServiceKey("fusion-agent-studio"); issued2 || plaintext2 != "" {
        t.Fatalf("re-issuance must retain the key (issued=%v)", issued2)
    }
    if key2, _ := r.CredentialFor("fusion-agent-studio"); key2 != plaintext {
        t.Fatalf("credential drifted after idempotent re-registration: %q vs %q", key2, plaintext)
    }
}

func TestRegistry_EntryOriginAllowlist(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432", "http://localhost:5173"}, time.Second)
    if err := r.Upsert(validManifest("fusion-mlx")); err != nil {
        t.Fatalf("allowed origin rejected: %v", err)
    }
    m := validManifest("fusion-evil")
    m.Entry = "http://evil.example:99/shell/fusion-evil/"
    if err := r.Upsert(m); err == nil {
        t.Fatal("non-whitelisted origin accepted (A7 violation)")
    }
    m.Entry = "not a url"
    if err := r.Upsert(m); err == nil {
        t.Fatal("malformed entry URL accepted")
    }
}

func TestWriteEnvelope(t *testing.T) {
    w := httptest.NewRecorder()
    WriteEnvelope(w, http.StatusServiceUnavailable, "backend_unavailable", "down", "fusion-mlx", true)
    resp := w.Result()
    if resp.StatusCode != http.StatusServiceUnavailable {
        t.Fatalf("status = %d", resp.StatusCode)
    }
    body := w.Body.String()
    for _, want := range []string{`"code":"backend_unavailable"`, `"module_id":"fusion-mlx"`, `"retryable":true`} {
        if !contains(body, want) {
            t.Errorf("body missing %s: %s", want, body)
        }
    }
}

func contains(s, sub string) bool {
    return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return i
        }
    }
    return -1
}

func TestRegistry_RelativeEntryAcceptedAndTraversalRejected(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    // F-5: PRD §5.1 prod convention — gateway-hosted relative path is
    // same-origin by construction, accepted without an origin check.
    if err := r.Upsert(config.ShellManifest{ID: "fusion-rel", Title: "R", Path: "/r",
        Entry: "/shell/fusion-rel/", Devices: []string{"desktop"}, Category: "domain"}); err != nil {
        t.Fatalf("relative entry rejected: %v", err)
    }
    if err := r.Upsert(config.ShellManifest{ID: "fusion-evil", Title: "E", Path: "/e",
        Entry: "/shell/../../admin", Devices: []string{"desktop"}, Category: "domain"}); err == nil {
        t.Fatal("path traversal entry accepted")
    }
    // Absolute out-of-allowlist origin still rejected (A7 unchanged).
    if err := r.Upsert(config.ShellManifest{ID: "fusion-abs", Title: "A", Path: "/a",
        Entry: "http://evil.example:99/", Devices: []string{"desktop"}, Category: "domain"}); err == nil {
        t.Fatal("out-of-allowlist absolute entry accepted")
    }
}

// TestRegistry_Persistence_RoundTrip (#230): an upserted registration lands
// in the store file and a fresh registry restores it with the persisted
// heartbeat (still online right after save).
func TestRegistry_Persistence_RoundTrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "shell_manifests.json")
    r1 := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    r1.SetPersistence(path)
    if err := r1.Upsert(validManifest("fusion-agent-studio")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    raw, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("store file missing after upsert: %v", err)
    }
    if strings.Contains(string(raw), "fusion-admin") {
        t.Fatal("static/admin entries must not leak into the dynamic store")
    }

    r2 := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    r2.SetPersistence(path)
    if err := r2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    views := r2.List()
    if len(views) != 1 || views[0].ID != "fusion-agent-studio" || !views[0].Registered {
        t.Fatalf("restored views = %+v", views)
    }
    if views[0].Status != StatusOnline {
        t.Fatalf("fresh heartbeat should restore online, got %s", views[0].Status)
    }

    // Stale heartbeat survives the round-trip as unreachable — a restart
    // never resurrects a dead module to online (heartbeat offline marking
    // still applies).
    stale := []persistedManifest{{
        ShellManifest: validManifest("fusion-stale"),
        LastHeartbeat: time.Now().Add(-time.Hour),
    }}
    staleRaw, _ := json.MarshalIndent(stale, "", "    ")
    if err := os.WriteFile(path, staleRaw, 0o600); err != nil {
        t.Fatalf("write stale store: %v", err)
    }
    r3 := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    r3.SetPersistence(path)
    if err := r3.Load(); err != nil {
        t.Fatalf("load stale: %v", err)
    }
    if views := r3.List(); len(views) != 1 || views[0].Status != StatusUnreachable {
        t.Fatalf("stale views = %+v, want unreachable", views)
    }
}

// TestRegistry_Persistence_SkipsInvalidEntries (#230): store entries that no
// longer validate against the CURRENT rules (allowlist tightened, bad shape)
// are skipped with a warning, never fatal.
func TestRegistry_Persistence_SkipsInvalidEntries(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "shell_manifests.json")
    store := []persistedManifest{
        {ShellManifest: validManifest("fusion-good"), LastHeartbeat: time.Now()},
        {ShellManifest: validManifest("fusion-evil2"), LastHeartbeat: time.Now()},
        {ShellManifest: validManifest("badshape"), LastHeartbeat: time.Now()},
    }
    store[1].ShellManifest.Entry = "http://evil.example:99/x/"
    raw, _ := json.MarshalIndent(store, "", "    ")
    if err := os.WriteFile(path, raw, 0o600); err != nil {
        t.Fatalf("write store: %v", err)
    }
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    r.SetPersistence(path)
    if err := r.Load(); err != nil {
        t.Fatalf("load must not fail on invalid entries: %v", err)
    }
    views := r.List()
    if len(views) != 1 || views[0].ID != "fusion-good" {
        t.Fatalf("views = %+v, want only fusion-good", views)
    }
}

// TestRegistry_Persistence_CorruptStore: a corrupt file returns an error
// (caller logs and continues memory-only) and leaves the registry usable.
func TestRegistry_Persistence_CorruptStore(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "shell_manifests.json")
    if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
        t.Fatalf("write: %v", err)
    }
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    r.SetPersistence(path)
    if err := r.Load(); err == nil {
        t.Fatal("corrupt store must return an error")
    }
    if err := r.Upsert(validManifest("fusion-mlx")); err != nil {
        t.Fatalf("registry unusable after corrupt store: %v", err)
    }
}

// TestRegistry_NoPersistence_MemoryOnly: no path set — Load is a no-op and
// nothing is written anywhere.
func TestRegistry_NoPersistence_MemoryOnly(t *testing.T) {
    r := NewRegistry(nil, nil, time.Second)
    if err := r.Load(); err != nil {
        t.Fatalf("memory-only Load must be a no-op: %v", err)
    }
    if err := r.Upsert(validManifest("fusion-mlx")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if r.path != "" {
        t.Fatal("path must stay empty without SetPersistence")
    }
}

func TestRegistry_ExtFieldValidation(t *testing.T) {
    r := NewRegistry(nil, nil, time.Second)
    cases := []struct {
        name string
        m    config.ShellManifest
    }{
        {"search endpoint not gateway-absolute", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.SearchProviders = []config.ShellSearchProvider{{ID: "s", Label: "S", Endpoint: "http://evil/x"}}
            return m
        }()},
        {"search provider missing label", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.SearchProviders = []config.ShellSearchProvider{{ID: "s", Endpoint: "/api/s"}}
            return m
        }()},
        {"search endpoint traversal", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.SearchProviders = []config.ShellSearchProvider{{ID: "s", Label: "S", Endpoint: "/api/../secret"}}
            return m
        }()},
        {"page route missing title", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Pages = []config.ShellPage{{Route: "/runs"}}
            return m
        }()},
        {"page route contains fragment", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Pages = []config.ShellPage{{Route: "/runs#top", Title: "Runs"}}
            return m
        }()},
        {"page route not absolute", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Pages = []config.ShellPage{{Route: "runs", Title: "Runs"}}
            return m
        }()},
        {"notification bad kind", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Notifications = []config.ShellNotificationSource{{ID: "n", Label: "N", Endpoint: "/api/n", Kind: "junk"}}
            return m
        }()},
        {"notification missing id", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Notifications = []config.ShellNotificationSource{{Label: "N", Endpoint: "/api/n", Kind: "runs"}}
            return m
        }()},
        {"home card missing title", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Home = &config.ShellHome{Cards: []config.ShellHomeCard{{ID: "c", Route: "/runs"}}}
            return m
        }()},
        {"home card route contains fragment", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Home = &config.ShellHome{Cards: []config.ShellHomeCard{{ID: "c", Title: "C", Route: "/runs#top"}}}
            return m
        }()},
        {"home card route not absolute", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Home = &config.ShellHome{Cards: []config.ShellHomeCard{{ID: "c", Title: "C", Route: "runs"}}}
            return m
        }()},
        {"too many home cards", func() config.ShellManifest {
            m := validManifest("fusion-x")
            cards := make([]config.ShellHomeCard, 33)
            for i := range cards {
                cards[i] = config.ShellHomeCard{ID: fmt.Sprintf("c%d", i), Title: "C", Route: "/runs"}
            }
            m.Home = &config.ShellHome{Cards: cards}
            return m
        }()},
        {"home project missing label", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Home = &config.ShellHome{Projects: []config.ShellHomeProject{{ID: "p", Endpoint: "/api/spaces"}}}
            return m
        }()},
        {"home project endpoint not absolute", func() config.ShellManifest {
            m := validManifest("fusion-x")
            m.Home = &config.ShellHome{Projects: []config.ShellHomeProject{{ID: "p", Label: "P", Endpoint: "api/spaces"}}}
            return m
        }()},
        {"too many home projects", func() config.ShellManifest {
            m := validManifest("fusion-x")
            projects := make([]config.ShellHomeProject, 33)
            for i := range projects {
                projects[i] = config.ShellHomeProject{ID: fmt.Sprintf("p%d", i), Label: "P", Endpoint: "/api/spaces"}
            }
            m.Home = &config.ShellHome{Projects: projects}
            return m
        }()},
        {"too many search providers", func() config.ShellManifest {
            m := validManifest("fusion-x")
            for i := 0; i < 33; i++ {
                m.SearchProviders = append(m.SearchProviders, config.ShellSearchProvider{ID: fmt.Sprintf("s%d", i), Label: "S", Endpoint: "/api/s"})
            }
            return m
        }()},
    }
    for _, tc := range cases {
        if err := r.Upsert(tc.m); err == nil {
            t.Errorf("%s: want error, got nil", tc.name)
        }
    }

    // A valid full set passes and surfaces from List.
    ok := validManifest("fusion-x")
    ok.SearchProviders = []config.ShellSearchProvider{{ID: "workflows", Label: "工作流", LabelEn: "Workflows", Endpoint: "/api/cowork/search"}}
    ok.Pages = []config.ShellPage{{Route: "/runs", Title: "运行", TitleEn: "Runs"}}
    ok.Notifications = []config.ShellNotificationSource{{ID: "approvals", Label: "审批", Endpoint: "/api/cowork/notifications", Kind: "approvals"}, {ID: "tasks", Label: "任务", Endpoint: "/api/cowork/tasks", Kind: "tasks"}}
    ok.Home = &config.ShellHome{Cards: []config.ShellHomeCard{{ID: "monthly-close", Title: "月度对账", TitleEn: "Monthly close", Description: "对账并生成报告", DescriptionEn: "Reconcile and report", Route: "/workflows/monthly-close", Chip: true}}, Projects: []config.ShellHomeProject{{ID: "spaces", Label: "空间", LabelEn: "Spaces", Endpoint: "/api/cowork/spaces"}}}
    if err := r.Upsert(ok); err != nil {
        t.Fatalf("valid ext upsert: %v", err)
    }
    views := r.List()
    if len(views) != 1 {
        t.Fatalf("want 1 view, got %d", len(views))
    }
    v := views[0]
    if len(v.SearchProviders) != 1 || v.SearchProviders[0].Endpoint != "/api/cowork/search" {
        t.Fatalf("search providers lost: %+v", v.SearchProviders)
    }
    if len(v.Pages) != 1 || v.Pages[0].Route != "/runs" {
        t.Fatalf("pages lost: %+v", v.Pages)
    }
    if len(v.Notifications) != 2 || v.Notifications[1].Kind != "tasks" {
        t.Fatalf("notifications lost: %+v", v.Notifications)
    }
    if v.Home == nil || len(v.Home.Cards) != 1 || v.Home.Cards[0].Route != "/workflows/monthly-close" || !v.Home.Cards[0].Chip {
        t.Fatalf("home cards lost: %+v", v.Home)
    }
    if len(v.Home.Projects) != 1 || v.Home.Projects[0].Endpoint != "/api/cowork/spaces" || v.Home.Projects[0].LabelEn != "Spaces" {
        t.Fatalf("home projects lost: %+v", v.Home.Projects)
    }
}

func TestRegistry_ExtFieldsPersistRoundTrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "shell_manifests.json")
    r := NewRegistry(nil, nil, time.Second)
    r.SetPersistence(path)
    m := validManifest("fusion-cowork")
    m.SearchProviders = []config.ShellSearchProvider{{ID: "workflows", Label: "工作流", Endpoint: "/api/cowork/search"}}
    m.Pages = []config.ShellPage{{Route: "/runs", Title: "运行"}}
    m.Notifications = []config.ShellNotificationSource{{ID: "runs", Label: "运行", Endpoint: "/api/cowork/notifications", Kind: "runs"}, {ID: "tasks", Label: "任务", Endpoint: "/api/cowork/tasks", Kind: "tasks"}}
    m.Home = &config.ShellHome{Cards: []config.ShellHomeCard{{ID: "weekly-readout", Title: "周报 readout", Route: "/workflows/weekly"}}, Projects: []config.ShellHomeProject{{ID: "spaces", Label: "空间", Endpoint: "/api/cowork/spaces"}}}
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }

    r2 := NewRegistry(nil, nil, time.Second)
    r2.SetPersistence(path)
    if err := r2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    views := r2.List()
    if len(views) != 1 {
        t.Fatalf("want 1 view, got %d", len(views))
    }
    v := views[0]
    if len(v.SearchProviders) != 1 || len(v.Pages) != 1 || len(v.Notifications) != 2 {
        t.Fatalf("ext fields not persisted: %+v", v)
    }
    if v.Notifications[0].Kind != "runs" || v.Notifications[1].Kind != "tasks" {
        t.Fatalf("notification kind lost: %+v", v.Notifications)
    }
    if v.Home == nil || len(v.Home.Cards) != 1 || v.Home.Cards[0].ID != "weekly-readout" {
        t.Fatalf("home cards not persisted: %+v", v.Home)
    }
    if len(v.Home.Projects) != 1 || v.Home.Projects[0].ID != "spaces" {
        t.Fatalf("home projects not persisted: %+v", v.Home.Projects)
    }
}

func TestRegistry_Remove(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "shell_manifests.json")
    r := NewRegistry(nil, nil, time.Second)
    r.SetPersistence(path)
    if err := r.Upsert(validManifest("fusion-temp")); err != nil {
        t.Fatalf("upsert: %v", err)
    }

    // Unknown id: ErrManifestNotFound (404 condition, not idempotent-200).
    if err := r.Remove("fusion-ghost"); !errors.Is(err, ErrManifestNotFound) {
        t.Fatalf("remove unknown = %v, want ErrManifestNotFound", err)
    }
    // Static-only id: ErrManifestStatic (config-owned, API delete rejected).
    r2 := NewRegistry([]config.ShellManifest{validManifest("fusion-static")}, nil, time.Second)
    if err := r2.Remove("fusion-static"); !errors.Is(err, ErrManifestStatic) {
        t.Fatalf("remove static = %v, want ErrManifestStatic", err)
    }
    // Dynamic id that ALSO exists as static fallback: the dynamic
    // registration is API-owned and removable; the static entry survives.
    r3 := NewRegistry([]config.ShellManifest{validManifest("fusion-temp")}, nil, time.Second)
    if err := r3.Upsert(validManifest("fusion-temp")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if err := r3.Remove("fusion-temp"); err != nil {
        t.Fatalf("remove dynamic-over-static: %v", err)
    }
    views := r3.List()
    if len(views) != 1 || views[0].Registered {
        t.Fatalf("want static-only offline view, got %+v", views)
    }

    // Removed entry persists: a fresh registry loads without it.
    if err := r.Remove("fusion-temp"); err != nil {
        t.Fatalf("remove: %v", err)
    }
    r4 := NewRegistry(nil, nil, time.Second)
    r4.SetPersistence(path)
    if err := r4.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    if views := r4.List(); len(views) != 0 {
        t.Fatalf("removed manifest resurrected on load: %+v", views)
    }
}

func TestRegistry_RemoveBuiltinRejected(t *testing.T) {
    r := NewRegistry(nil, nil, time.Second)
    m := validManifest("fusion-admin")
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert builtin: %v", err)
    }
    r.MarkBuiltin("fusion-admin")
    // Builtins are gateway-owned: rejected like static entries even though
    // the registration itself is dynamic.
    if err := r.Remove("fusion-admin"); !errors.Is(err, ErrManifestStatic) {
        t.Fatalf("remove builtin = %v, want ErrManifestStatic", err)
    }
    // Unmarking is not possible via the API surface — a different id still
    // removes fine on the same registry.
    if err := r.Upsert(validManifest("fusion-temp")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if err := r.Remove("fusion-temp"); err != nil {
        t.Fatalf("remove non-builtin: %v", err)
    }
}

func TestRegistry_ExtRouteFor(t *testing.T) {
    r := NewRegistry(nil, nil, time.Second)
    m := validManifest("fusion-mlx")
    m.Upstream = "http://127.0.0.1:11471"
    m.APIKey = "fk-bridge"
    m.SearchProviders = []config.ShellSearchProvider{{ID: "models", Label: "模型", Endpoint: "/api/search/models"}}
    m.Notifications = []config.ShellNotificationSource{{ID: "eng", Label: "事件", Endpoint: "/api/notifications", Kind: "engine"}}
    m.Pages = []config.ShellPage{{Route: "/runs", Title: "运行"}}
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }

    cases := []struct {
        path string
        want bool
    }{
        // Module-relative declaration style.
        {"/api/search/models", true},        // exact search endpoint
        {"/api/search/models?q=x", false},   // query is not part of path
        {"/api/search/models/sub", true},    // segment-boundary prefix
        {"/api/notifications", true},        // exact notification endpoint
        {"/api/notificationss", false},      // NOT a prefix at segment boundary
        // Gateway-absolute declaration style (what fusion-mlx registers).
        {"/shell/fusion-mlx/api/search/models", true},
        {"/shell/fusion-mlx/api/notifications", true},
        {"/shell/fusion-mlx/api/search/models/sub", true},
        // Cross-module path must NOT match (never route another module's
        // namespace to this module's upstream).
        {"/shell/fusion-doc/api/search/models", false},
        {"/shell/fusion-mlx/other", false}, // undeclared path
        {"", false},                        // empty
        {"/runs", false},                   // pages are SPA deep links, never routed
    }
    for _, tc := range cases {
        up, key, ok := r.ExtRouteFor("fusion-mlx", tc.path)
        if ok != tc.want {
            t.Errorf("ExtRouteFor(%q) ok = %v, want %v", tc.path, ok, tc.want)
            continue
        }
        if ok && (up != "http://127.0.0.1:11471" || key != "fk-bridge") {
            t.Errorf("ExtRouteFor(%q) = %q/%q", tc.path, up, key)
        }
    }

    // No upstream on the manifest: no ext routing (static serves).
    m2 := validManifest("fusion-doc")
    m2.SearchProviders = []config.ShellSearchProvider{{ID: "s", Label: "S", Endpoint: "/api/search"}}
    if err := r.Upsert(m2); err != nil {
        t.Fatalf("upsert m2: %v", err)
    }
    if _, _, ok := r.ExtRouteFor("fusion-doc", "/api/search"); ok {
        t.Error("manifest without upstream must not route ext paths")
    }
    // Unknown module: no.
    if _, _, ok := r.ExtRouteFor("fusion-ghost", "/api/search/models"); ok {
        t.Error("unknown module must not route ext paths")
    }
}

func TestRegistry_UpsertKeepsIssuedKeyPlaintextOnReregistration(t *testing.T) {
    // #277: a re-registration without api_key must keep the fm- plaintext
    // (the proxy injection path), not just the hashes — otherwise the
    // module looks healthy (heartbeat hash still matches) while the proxy
    // forwards unauthenticated and the plaintext is lost from the store.
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-trainer")
    m.Upstream = "http://127.0.0.1:8790"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    _, plaintext, issued, err := r.EnsureServiceKey("fusion-trainer")
    if err != nil || !issued || plaintext == "" {
        t.Fatalf("issue service key: issued=%v err=%v", issued, err)
    }
    // Module restart → idempotent re-registration, still no api_key in body.
    if err := r.Upsert(m); err != nil {
        t.Fatalf("re-upsert: %v", err)
    }
    _, key, ok := r.UpstreamFor("fusion-trainer")
    if !ok || key != plaintext {
        t.Fatalf("proxy credential lost on re-registration: ok=%v key=%q want %q", ok, key, plaintext)
    }
    // Heartbeat must still authenticate with the same fm- key.
    if !r.AuthServiceKey("fusion-trainer", serviceKeyHashOf(plaintext), time.Hour) {
        t.Fatal("issued key no longer authenticates after re-registration")
    }
}

// TestRegistry_SaveConcurrent: save() writes a fixed temp path (path + ".tmp")
// then renames it into place. Two saves interleaving let whichever rename lost
// the race fail with "no such file or directory", logging "shell manifest
// registry save failed" while losing that snapshot. Concurrent saves must all
// succeed and leave a parseable store.
func TestRegistry_SaveConcurrent(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "shell_manifests.json")
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, 30*time.Second)
    r.SetPersistence(path)
    if err := r.Upsert(validManifest("fusion-agent-studio")); err != nil {
        t.Fatalf("upsert: %v", err)
    }

    const writers = 16
    errs := make(chan error, writers)
    var wg sync.WaitGroup
    for i := 0; i < writers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            errs <- r.save()
        }()
    }
    wg.Wait()
    close(errs)
    for err := range errs {
        if err != nil {
            t.Fatalf("concurrent save failed: %v", err)
        }
    }

    raw, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("store file missing: %v", err)
    }
    var got []persistedManifest
    if err := json.Unmarshal(raw, &got); err != nil {
        t.Fatalf("store file unparseable after concurrent saves: %v", err)
    }
    if len(got) != 1 || got[0].ID != "fusion-agent-studio" {
        t.Fatalf("store contents = %+v, want the single upserted manifest", got)
    }
    if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
        t.Fatalf("temp file left behind after save: err=%v", err)
    }
}
