package shell

import (
    "bytes"
    "log/slog"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #478 regression guard: a static web_shell.modules upstream that differs
// from the module's self-registered upstream is a silent proxy mis-route
// (plugins-ecosystem: static pointed at the API server :8765 which hangs on
// "/", while the webui serving the iframe document lives on :8795). Both
// healths return 200, so nothing surfaced the drift — the registry must
// WARN when a registration's own upstream disagrees with the static config.

func driftCaptureLogs(t *testing.T) *bytes.Buffer {
    t.Helper()
    var buf bytes.Buffer
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
    t.Cleanup(func() { slog.SetDefault(prev) })
    return &buf
}

func driftManifest(id, upstream string) config.ShellManifest {
    return config.ShellManifest{
        ID:       id,
        Title:    "Drift",
        TitleEn:  "Drift",
        Entry:    "/shell/" + id + "/",
        Path:     "/" + strings.TrimPrefix(id, "fusion-"),
        Devices:  []string{"desktop"},
        Category: "platform",
        Sort:     50,
        Upstream: upstream,
    }
}

func TestUpstreamDrift_StaticVsSelfRegistered_Warns(t *testing.T) {
    buf := driftCaptureLogs(t)
    r := NewRegistry(nil, nil, time.Second)
    r.SetProxyUpstreams(map[string]string{"fusion-drift": "http://127.0.0.1:8765"})
    if err := r.Upsert(driftManifest("fusion-drift", "http://127.0.0.1:8795")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if !strings.Contains(buf.String(), "upstream drift") {
        t.Fatalf("expected upstream drift WARN, got: %s", buf.String())
    }
}

func TestUpstreamDrift_MatchingUpstreams_Silent(t *testing.T) {
    buf := driftCaptureLogs(t)
    r := NewRegistry(nil, nil, time.Second)
    r.SetProxyUpstreams(map[string]string{"fusion-drift": "http://127.0.0.1:8795"})
    if err := r.Upsert(driftManifest("fusion-drift", "http://127.0.0.1:8795")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if strings.Contains(buf.String(), "upstream drift") {
        t.Fatalf("matching upstreams must not warn, got: %s", buf.String())
    }
}

func TestUpstreamDrift_NoStaticEntry_Silent(t *testing.T) {
    buf := driftCaptureLogs(t)
    r := NewRegistry(nil, nil, time.Second)
    // modules map empty (self-registered-only module) — nothing to drift from
    if err := r.Upsert(driftManifest("fusion-drift", "http://127.0.0.1:8795")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if strings.Contains(buf.String(), "upstream drift") {
        t.Fatalf("absent static entry must not warn, got: %s", buf.String())
    }
}

func TestUpstreamDrift_SelfRegisteredWithoutUpstream_Silent(t *testing.T) {
    buf := driftCaptureLogs(t)
    r := NewRegistry(nil, nil, time.Second)
    r.SetProxyUpstreams(map[string]string{"fusion-drift": "http://127.0.0.1:8765"})
    m := driftManifest("fusion-drift", "")
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if strings.Contains(buf.String(), "upstream drift") {
        t.Fatalf("registration without its own upstream must not warn, got: %s", buf.String())
    }
}
