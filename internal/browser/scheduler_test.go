package browser

import (
    "errors"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// newTestRegistry builds a registry with no config seed so scheduler tests can
// seed nodes by stable config id and inject capacity directly (white-box: the
// test is in package browser so it can touch the unexported nodes map). The
// poll worker is NOT started — capacity is set by setCap, not by polling.
func newTestRegistry(t *testing.T) *Registry {
    t.Helper()
    r, err := NewRegistry(NewNodeClient(0, 0, 0), 5*time.Second, 3, 30*time.Second, nil)
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    return r
}

// seedNode adds a config-seed node with the given stable id + socket. Capacity
// is nil until setCap is called (simulates the pre-poll state).
func seedNode(t *testing.T, reg *Registry, id, socket string) {
    t.Helper()
    reg.DrainAndApply(append(reg.liveConfigSeed(), config.BrowserNodeConfig{ID: id, SocketPath: socket}))
}

// liveConfigSeed returns the config-seed nodes currently in the registry as a
// BrowserNodeConfig slice, so seedNode can append without dropping prior seeds.
func (r *Registry) liveConfigSeed() []config.BrowserNodeConfig {
    r.mu.RLock()
    defer r.mu.RUnlock()
    var out []config.BrowserNodeConfig
    for _, n := range r.nodes {
        if n.source == sourceConfig {
            out = append(out, config.BrowserNodeConfig{ID: n.NodeID, SocketPath: n.SocketPath})
        }
    }
    return out
}

// setCap injects a capacity snapshot into a seeded node (white-box). Replaces
// the nil capacity set by seedNode so the scheduler has a placement signal
// without running the poll worker.
func setCap(t *testing.T, reg *Registry, id string, cap *FBNodeCapacity) {
    t.Helper()
    reg.mu.Lock()
    defer reg.mu.Unlock()
    n, ok := reg.nodes[id]
    if !ok {
        t.Fatalf("setCap: unknown node %q", id)
    }
    n.Capacity = cap
    n.State = NodeStateLive
    n.failures = 0
    n.lastPoll = time.Now()
}

// killNode flips a node to dead (white-box) so the scheduler excludes it.
func killNode(t *testing.T, reg *Registry, id string) {
    t.Helper()
    reg.mu.Lock()
    defer reg.mu.Unlock()
    n, ok := reg.nodes[id]
    if !ok {
        t.Fatalf("killNode: unknown node %q", id)
    }
    n.State = NodeStateDead
}

func TestPickMostFreeMemory(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    seedNode(t, reg, "b", "/tmp/b.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 4000})
    setCap(t, reg, "b", &FBNodeCapacity{NodeID: "b", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "b" {
        t.Fatalf("expected most-free node b, got %s", got)
    }
}

func TestPickTieBreakFewestLive(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    seedNode(t, reg, "b", "/tmp/b.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 3, FreeMemoryMB: 8000})
    setCap(t, reg, "b", &FBNodeCapacity{NodeID: "b", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "b" {
        t.Fatalf("expected fewest-live node b, got %s", got)
    }
}

func TestPickTieBreakIDAsc(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "zeta", "/tmp/zeta.sock")
    seedNode(t, reg, "alpha", "/tmp/alpha.sock")
    setCap(t, reg, "zeta", &FBNodeCapacity{NodeID: "zeta", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    setCap(t, reg, "alpha", &FBNodeCapacity{NodeID: "alpha", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "alpha" {
        t.Fatalf("expected id-asc node alpha, got %s", got)
    }
}

func TestPickSkipsFullNode(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    seedNode(t, reg, "b", "/tmp/b.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 2, LiveSessions: 2, FreeMemoryMB: 8000})
    setCap(t, reg, "b", &FBNodeCapacity{NodeID: "b", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 1000})
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "b" {
        t.Fatalf("expected b (a is full), got %s", got)
    }
}

func TestPickSkipsBelowMemoryFloor(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    seedNode(t, reg, "b", "/tmp/b.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 100})
    setCap(t, reg, "b", &FBNodeCapacity{NodeID: "b", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 500})
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "b" {
        t.Fatalf("expected b (a below floor), got %s", got)
    }
}

func TestPickFreeMemoryZeroAdmitted(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 0})
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "a" {
        t.Fatalf("expected a admitted on free==0, got %s", got)
    }
}

func TestPickGlobalQuotaExceeded(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    seedNode(t, reg, "b", "/tmp/b.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 3, FreeMemoryMB: 8000})
    setCap(t, reg, "b", &FBNodeCapacity{NodeID: "b", MaxSessions: 4, LiveSessions: 2, FreeMemoryMB: 8000})
    // ceiling 5; total live = 5 >= 5 → quota.
    sched := NewScheduler(reg, 5, 200)
    _, err := sched.Pick()
    if !errors.Is(err, ErrGlobalQuotaExceeded) {
        t.Fatalf("expected ErrGlobalQuotaExceeded, got %v", err)
    }
}

func TestPickNoHeadroom(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 2, LiveSessions: 2, FreeMemoryMB: 8000})
    sched := NewScheduler(reg, 0, 200)
    _, err := sched.Pick()
    if !errors.Is(err, ErrNoNodeHeadroom) {
        t.Fatalf("expected ErrNoNodeHeadroom, got %v", err)
    }
}

func TestPickDeadNodeExcluded(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    seedNode(t, reg, "b", "/tmp/b.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    setCap(t, reg, "b", &FBNodeCapacity{NodeID: "b", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 4000})
    killNode(t, reg, "a") // a dead → only b eligible
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "b" {
        t.Fatalf("expected b (a is dead), got %s", got)
    }
}

func TestPickAllDeadNoHeadroom(t *testing.T) {
    reg := newTestRegistry(t)
    seedNode(t, reg, "a", "/tmp/a.sock")
    setCap(t, reg, "a", &FBNodeCapacity{NodeID: "a", MaxSessions: 4, LiveSessions: 1, FreeMemoryMB: 8000})
    killNode(t, reg, "a")
    sched := NewScheduler(reg, 0, 200)
    _, err := sched.Pick()
    if !errors.Is(err, ErrNoNodeHeadroom) {
        t.Fatalf("expected ErrNoNodeHeadroom when all dead, got %v", err)
    }
}

func TestPickUnpolledNodeAdmitted(t *testing.T) {
    // A config-seed node that has never been polled has nil Capacity.
    // Pick must admit it (0 live, huge max, 0 free) rather than reject.
    reg := newTestRegistry(t)
    seedNode(t, reg, "fresh", "/tmp/fresh.sock") // nil Capacity
    sched := NewScheduler(reg, 0, 200)
    got, err := sched.Pick()
    if err != nil {
        t.Fatalf("Pick: %v", err)
    }
    if got != "fresh" {
        t.Fatalf("expected unpolled node fresh admitted, got %s", got)
    }
}

func TestPickEmptyRegistryNoHeadroom(t *testing.T) {
    reg := newTestRegistry(t)
    sched := NewScheduler(reg, 0, 200)
    _, err := sched.Pick()
    if !errors.Is(err, ErrNoNodeHeadroom) {
        t.Fatalf("expected ErrNoNodeHeadroom on empty registry, got %v", err)
    }
}
