package shell

import (
    "net/http"
    "net/http/httptest"
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
    }
    for _, tc := range cases {
        if err := r.Upsert(tc.m); err == nil {
            t.Errorf("%s: want error, got nil", tc.name)
        }
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
