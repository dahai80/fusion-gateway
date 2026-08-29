package browser

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewRegistryRejectsDuplicateSeedID(t *testing.T) {
    seed := []config.BrowserNodeConfig{
        {ID: "dup", SocketPath: "/tmp/a.sock"},
        {ID: "dup", SocketPath: "/tmp/b.sock"},
    }
    _, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, seed)
    if err == nil {
        t.Fatal("expected error for duplicate seed id, got nil")
    }
    if !errIs(err, "duplicate node id") {
        t.Fatalf("expected duplicate-id error, got %v", err)
    }
}

func TestNewRegistrySeedsNodes(t *testing.T) {
    seed := []config.BrowserNodeConfig{
        {ID: "a", SocketPath: "/tmp/a.sock"},
        {ID: "b", SocketPath: "/tmp/b.sock"},
    }
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, seed)
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    views := reg.Snapshot()
    if len(views) != 2 {
        t.Fatalf("expected 2 seeded nodes, got %d", len(views))
    }
    // Snapshot sorted by id asc.
    if views[0].NodeID != "a" || views[1].NodeID != "b" {
        t.Fatalf("snapshot not sorted by id: %+v", views)
    }
    for _, v := range views {
        if v.Capacity != nil {
            t.Fatalf("seeded node %s should have nil capacity pre-poll", v.NodeID)
        }
        if v.State != NodeStateLive {
            t.Fatalf("seeded node %s should be live pre-poll, got %s", v.NodeID, v.State)
        }
        if string(v.Source) != "config" {
            t.Fatalf("seeded node %s source should be config, got %s", v.NodeID, v.Source)
        }
    }
}

func TestSocketOf(t *testing.T) {
    seed := []config.BrowserNodeConfig{{ID: "a", SocketPath: "/tmp/a.sock"}}
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, seed)
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    got, ok := reg.SocketOf("a")
    if !ok || got != "/tmp/a.sock" {
        t.Fatalf("SocketOf(a) = %q ok=%v, want /tmp/a.sock true", got, ok)
    }
    if _, ok := reg.SocketOf("missing"); ok {
        t.Fatal("SocketOf(missing) should be ok=false")
    }
}

func TestIsDeadUnknownIsDead(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    // Unknown node is treated as dead (no socket to dial).
    if !reg.IsDead("ghost") {
        t.Fatal("IsDead(unknown) should be true")
    }
}

func TestRegisterDialinAddsNode(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    id := reg.RegisterDialin("/tmp/dial.sock", &FBNodeCapacity{NodeID: "p1", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    if id == "" {
        t.Fatal("RegisterDialin returned empty id")
    }
    if !hasPrefix(id, "dialin-") {
        t.Fatalf("dialin id should start dialin-, got %s", id)
    }
    socket, ok := reg.SocketOf(id)
    if !ok || socket != "/tmp/dial.sock" {
        t.Fatalf("dial-in node socket wrong: %q ok=%v", socket, ok)
    }
    views := reg.Snapshot()
    found := false
    for _, v := range views {
        if v.NodeID == id && string(v.Source) == "dialin" {
            found = true
            if v.Capacity == nil || v.Capacity.FreeMemoryMB != 8000 {
                t.Fatalf("dial-in capacity not stored: %+v", v.Capacity)
            }
        }
    }
    if !found {
        t.Fatalf("dial-in node %s not in snapshot", id)
    }
}

func TestRegisterDialinDedupsBySocket(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "cfg", SocketPath: "/tmp/shared.sock"}})
    // Dial-in on the same socket as a config-seed node: should NOT mint a new
    // node; config id wins, capacity is refreshed.
    id := reg.RegisterDialin("/tmp/shared.sock", &FBNodeCapacity{NodeID: "p2", MaxSessions: 4, LiveSessions: 2, FreeMemoryMB: 4000})
    if id != "cfg" {
        t.Fatalf("dial-in on existing socket should return config id cfg, got %s", id)
    }
    views := reg.Snapshot()
    if len(views) != 1 {
        t.Fatalf("expected 1 node (dedup), got %d", len(views))
    }
    if views[0].Capacity == nil || views[0].Capacity.FreeMemoryMB != 4000 {
        t.Fatalf("capacity not refreshed by dial-in: %+v", views[0].Capacity)
    }
}

func TestDrainAndApplyPreservesUnchangedCapacity(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "a", SocketPath: "/tmp/a.sock"}})
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    // Reload with the same node unchanged: capacity must survive.
    reg.DrainAndApply([]config.BrowserNodeConfig{{ID: "a", SocketPath: "/tmp/a.sock"}})
    views := reg.Snapshot()
    if len(views) != 1 || views[0].Capacity == nil || views[0].Capacity.FreeMemoryMB != 8000 {
        t.Fatalf("unchanged node capacity not preserved: %+v", views)
    }
}

func TestDrainAndApplyDropsRemovedNode(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{
            {ID: "a", SocketPath: "/tmp/a.sock"},
            {ID: "b", SocketPath: "/tmp/b.sock"},
        })
    // Reload with only a: b should be dropped.
    reg.DrainAndApply([]config.BrowserNodeConfig{{ID: "a", SocketPath: "/tmp/a.sock"}})
    views := reg.Snapshot()
    if len(views) != 1 || views[0].NodeID != "a" {
        t.Fatalf("removed node b not dropped: %+v", views)
    }
}

func TestDrainAndApplyPreservesDialin(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    reg.RegisterDialin("/tmp/dial.sock", &FBNodeCapacity{NodeID: "p1", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    // Reload with a config seed: the dial-in node must survive (runtime-learned).
    reg.DrainAndApply([]config.BrowserNodeConfig{{ID: "cfg", SocketPath: "/tmp/cfg.sock"}})
    views := reg.Snapshot()
    if len(views) != 2 {
        t.Fatalf("expected cfg + dialin (2), got %d: %+v", len(views), views)
    }
}

func TestPollFlipsLiveToDead(t *testing.T) {
    // A fake node that errors on capacity → pollOnce records a failure. After
    // failureThreshold consecutive failures the node flips to dead.
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity: func(req RequestFrame) ResponseFrame { return errResp("boom", "poll failure", false) },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 2, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n", SocketPath: fn.socket}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    // Drive two polls manually (threshold=2). pollOnce is unexported; call it
    // directly (white-box, same package).
    ctx, cancel := ctxShort()
    defer cancel()
    reg.pollOnce(ctx)
    reg.pollOnce(ctx)
    if !reg.IsDead("n") {
        t.Fatal("expected node n dead after 2 failures, still live")
    }
}

func TestPollRecoversDeadToLive(t *testing.T) {
    // Start with an erroring node, drive it dead, then flip the handler to a
    // healthy capacity response and poll again → should recover to live.
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity: func(req RequestFrame) ResponseFrame { return errResp("boom", "down", false) },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 2, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n", SocketPath: fn.socket}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    ctx, cancel := ctxShort()
    defer cancel()
    reg.pollOnce(ctx)
    reg.pollOnce(ctx)
    if !reg.IsDead("n") {
        t.Fatal("precondition: node should be dead before recovery")
    }
    // Swap handler to healthy.
    fn.handlers[reqTypeCapacity] = func(req RequestFrame) ResponseFrame {
        return capResp(FBNodeCapacity{NodeID: "n", MaxSessions: 4, LiveSessions: 0, FreeMemoryMB: 8000})
    }
    // Dead node re-probes only after recoveryInterval. Force the lastPoll back
    // so the node is "due" now (white-box).
    reg.mu.Lock()
    if n := reg.nodes["n"]; n != nil {
        n.lastPoll = time.Now().Add(-31 * time.Second)
    }
    reg.mu.Unlock()
    reg.pollOnce(ctx)
    if reg.IsDead("n") {
        t.Fatal("node should recover to live after a healthy poll")
    }
}

func hasPrefix(s, p string) bool {
    return len(s) >= len(p) && s[:len(p)] == p
}
