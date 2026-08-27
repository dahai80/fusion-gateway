package cluster

// #119 guard tests: gateway honors the routing strategy the fusion-multi-node
// master owns (master.RoutingSummary.Strategy), so a strategy set in
// fusion-studio is authoritative for /v1/chat/completions inference node
// selection — not divergent from the gateway's local cluster.load_balancer.
//
// Discriminator: two healthy nodes distinguish the strategies.
//   hw-node: MemoryGB 64, InFlight 5 → hardware-aware picks it (highest memory).
//   lc-node: MemoryGB 8,  InFlight 0 → least-connections picks it (lowest in-flight).
// Master strategy "hardware-aware" must select hw-node even when the caller
// passes "least-connections" (the local config strategy). Reverting the
// resolveStrategy override makes the caller's "least-connections" win → lc-node.

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// masterStrategyServer builds an httptest master serving /api/nodes (two
// healthy workers) and /api/routing/summary (the given strategy). The workers'
// Addresses point back at this server so /v1/models is served here too (both
// nodes serve the test model — the discriminator is the selection strategy,
// not model availability).
func masterStrategyServer(t *testing.T, strategy string) *httptest.Server {
    t.Helper()
    var srv *httptest.Server
    srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/api/nodes":
            resp := MasterNodesResponse{
                Total:  2,
                Online: 2,
                Nodes: []MasterNodeInfo{
                    {NodeID: "hw-node", Address: srv.URL, GPU: "M2Ultra", MemoryGB: 64, Status: "online"},
                    {NodeID: "lc-node", Address: srv.URL, GPU: "M2", MemoryGB: 8, Status: "online"},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        case "/api/routing/summary":
            _ = json.NewEncoder(w).Encode(MasterRoutingSummary{
                Strategy:   strategy,
                TotalNodes: 2,
                AvgLoad:    0.1,
            })
        case "/v1/models":
            _ = json.NewEncoder(w).Encode([]map[string]string{{"id": "served-model"}})
        default:
            http.NotFound(w, r)
        }
    }))
    return srv
}

// setInFlight sets a node's in-flight counter for the least-connections
// discriminator (hw-node busy → lc-node wins on least-connections).
func setInFlight(d *Discovery, nodeID string, n int64) {
    node, ok := d.GetNode(nodeID)
    if !ok {
        return
    }
    node.inFlight.Store(n)
}

// waitForMasterStrategy polls until the cached master strategy matches want or
// times out (the sync runs on a goroutine). Returns false on timeout.
func waitForMasterStrategy(d *Discovery, want string, timeout time.Duration) bool {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if entry, ok := d.masterStrategy.Load().(masterStrategyEntry); ok && entry.strategy == want {
            return true
        }
        time.Sleep(10 * time.Millisecond)
    }
    return false
}

// TestMasterStrategy_HonoredOverLocal: master reports strategy=hardware-aware;
// local config load_balancer=least-connections. SelectNodeByModel must pick
// hw-node (the hardware-aware pick), proving the master strategy overrides the
// caller's local strategy.
// Guard: if resolveStrategy did not override, the caller's "least-connections"
// would win and pick lc-node (in-flight 0).
func TestMasterStrategy_HonoredOverLocal(t *testing.T) {
    srv := masterStrategyServer(t, "hardware-aware")
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()

    if !waitForMasterStrategy(d, "hardware-aware", 2*time.Second) {
        t.Fatal("master strategy hardware-aware not cached in time (syncMasterStrategy did not run?)")
    }
    // Make hw-node busy so least-connections would skip it (in-flight 5); the
    // hardware-aware strategy should still pick it (highest MemoryGB 64).
    setInFlight(d, "hw-node", 5)

    selected, err := d.SelectNodeByModel("least-connections", "served-model", 0)
    if err != nil {
        t.Fatalf("expected a node, got error: %v", err)
    }
    if selected.ID != "hw-node" {
        t.Fatalf("master strategy hardware-aware must pick hw-node (MemoryGB 64), got %s — resolveStrategy did not override the local least-connections strategy", selected.ID)
    }
}

// TestMasterStrategy_FetchFail_FallsBackToLocal: master /api/routing/summary
// returns 500, so no strategy is cached. SelectNodeByModel must fall back to
// the caller's local strategy (least-connections → lc-node), NOT error.
// Guard: if the fetch-fail path errored instead of falling back, routing would
// stall on a transient master blip.
func TestMasterStrategy_FetchFail_FallsBackToLocal(t *testing.T) {
    var srv *httptest.Server
    srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/api/nodes":
            resp := MasterNodesResponse{
                Total: 2, Online: 2,
                Nodes: []MasterNodeInfo{
                    {NodeID: "hw-node", Address: srv.URL, MemoryGB: 64, Status: "online"},
                    {NodeID: "lc-node", Address: srv.URL, MemoryGB: 8, Status: "online"},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        case "/api/routing/summary":
            http.Error(w, `{"error":"master internal error"}`, http.StatusInternalServerError)
        case "/v1/models":
            _ = json.NewEncoder(w).Encode([]map[string]string{{"id": "served-model"}})
        default:
            http.NotFound(w, r)
        }
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()
    time.Sleep(300 * time.Millisecond) // let the failed sync run

    setInFlight(d, "hw-node", 5)
    // No cached strategy → falls back to local least-connections → lc-node.
    selected, err := d.SelectNodeByModel("least-connections", "served-model", 0)
    if err != nil {
        t.Fatalf("fetch-fail must fall back to local strategy, not error: %v", err)
    }
    if selected.ID != "lc-node" {
        t.Fatalf("local least-connections fallback must pick lc-node (in-flight 0), got %s", selected.ID)
    }
}

// TestMasterStrategy_IgnoreStrategyOptOut: master reports hardware-aware, but
// IgnoreMasterStrategy=true. The local least-connections strategy governs →
// lc-node. Proves the opt-out escape hatch keeps local strategy authoritative.
// Guard: if the opt-out check were absent, the master strategy would override
// and pick hw-node.
func TestMasterStrategy_IgnoreStrategyOptOut(t *testing.T) {
    srv := masterStrategyServer(t, "hardware-aware")
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL, IgnoreMasterStrategy: true},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()
    time.Sleep(300 * time.Millisecond)

    // Opt-out → masterStrategy stays empty (syncMasterStrategy skips).
    if entry, ok := d.masterStrategy.Load().(masterStrategyEntry); ok && entry.strategy != "" {
        t.Fatalf("ignore_strategy=true must NOT cache master strategy, got %q", entry.strategy)
    }
    setInFlight(d, "hw-node", 5)
    selected, err := d.SelectNodeByModel("least-connections", "served-model", 0)
    if err != nil {
        t.Fatalf("expected a node, got error: %v", err)
    }
    if selected.ID != "lc-node" {
        t.Fatalf("ignore_strategy=true must keep local least-connections (lc-node), got %s — opt-out not honored", selected.ID)
    }
}

// TestMasterStrategy_StandaloneUnchanged: standalone mode (no masterClient)
// ignores the master strategy machinery entirely. The caller's strategy
// governs directly. Proves #119 does not change the standalone/static-config
// path — the single-node/standalone fallback is preserved.
// Guard: if resolveStrategy consulted a nil masterClient path that panicked or
// returned empty, standalone would break.
func TestMasterStrategy_StandaloneUnchanged(t *testing.T) {
    // Standalone probes each node's /health + /v1/models, so both nodes must
    // point at a real server (not 127.0.0.1:1, which never answers and gets
    // marked unhealthy → "no healthy nodes"). One server serves both node IDs.
    var srv *httptest.Server
    srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/health":
            _ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
        case "/v1/models":
            _ = json.NewEncoder(w).Encode([]map[string]string{{"id": "served-model"}})
        default:
            http.NotFound(w, r)
        }
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeStandalone,
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
        Nodes: []config.ClusterNodeConfig{
            {ID: "hw-node", Address: srv.URL, MemoryGB: 64},
            {ID: "lc-node", Address: srv.URL, MemoryGB: 8},
        },
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()
    time.Sleep(400 * time.Millisecond) // standalone runs healthCheckLoop; wait for first probe

    setInFlight(d, "hw-node", 5)
    selected, err := d.SelectNodeByModel("least-connections", "", 0)
    if err != nil {
        t.Fatalf("standalone SelectNode error: %v", err)
    }
    if selected.ID != "lc-node" {
        t.Fatalf("standalone least-connections must pick lc-node, got %s", selected.ID)
    }
}

// TestEnvClusterBackend_ForcesMasterMode: FUSION_CLUSTER_BACKEND=multi-node with
// a standalone config + master.address set → NewDiscovery forces master mode
// (masterClient created, masterStrategy machinery active). 12-factor promotion
// without a config edit.
// Guard: if the env read were absent, masterClient would be nil and the master
// server's strategy would never be cached.
func TestEnvClusterBackend_ForcesMasterMode(t *testing.T) {
    srv := masterStrategyServer(t, "round-robin")
    defer srv.Close()

    t.Setenv("FUSION_CLUSTER_BACKEND", "multi-node")
    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeStandalone, // env must override this
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    if d.masterClient == nil {
        t.Fatal("FUSION_CLUSTER_BACKEND=multi-node must force master mode (masterClient created), got nil")
    }
    d.Start(context.Background())
    defer d.Stop()

    if !waitForMasterStrategy(d, "round-robin", 2*time.Second) {
        t.Fatal("env-forced master mode did not cache the master strategy (round-robin)")
    }
}

// TestEnvClusterBackend_NoAddress_StaysConfigured: FUSION_CLUSTER_BACKEND=
// multi-node but cluster.master.address empty → env gate is a no-op (logged),
// stays in configured standalone mode. Prevents the env var from silently
// emptying the node set.
// Guard: if the env gate forced master mode with no address, masterClient
// would be nil and the node set would be empty → every request cloud-degrades.
func TestEnvClusterBackend_NoAddress_StaysConfigured(t *testing.T) {
    t.Setenv("FUSION_CLUSTER_BACKEND", "multi-node")
    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeStandalone,
        Master:              config.ClusterMasterConfig{Address: ""}, // no master address
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
        Nodes: []config.ClusterNodeConfig{
            {ID: "lc-node", Address: "http://127.0.0.1:1", MemoryGB: 8},
        },
    }
    d := NewDiscovery(cfg)
    if d.masterClient != nil {
        t.Fatal("FUSION_CLUSTER_BACKEND=multi-node with empty master.address must NOT create masterClient (no-op, stays standalone)")
    }
}

// TestR3_MasterStrategy_StaleFallsBackToLocal (R3 audit P2): a cached master
// strategy older than max_stale_age must NOT be trusted — resolveStrategy
// falls back to the caller's local strategy. Before R3 the strategy was
// cached as a bare string with no timestamp, so a fetch failure left it
// permanently trusted across an indefinite master outage (route to dead
// nodes). Here a fresh strategy is cached, then its cachedAt is rewound past
// max_stale_age to simulate a long outage, and resolveStrategy must return
// local.
func TestR3_MasterStrategy_StaleFallsBackToLocal(t *testing.T) {
    srv := masterStrategyServer(t, "hardware-aware")
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL, MaxStaleAge: 50 * time.Millisecond},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()
    if !waitForMasterStrategy(d, "hardware-aware", 2*time.Second) {
        t.Fatal("master strategy hardware-aware not cached in time")
    }

    // Rewind cachedAt far into the past so the entry is stale beyond
    // max_stale_age (50ms). Simulates a master outage long enough that the
    // cached strategy is no longer trustworthy.
    d.masterStrategy.Store(masterStrategyEntry{strategy: "hardware-aware", cachedAt: time.Now().Add(-10 * time.Minute)})

    // resolveStrategy must reject the stale strategy and return local.
    if got := d.resolveStrategy("least-connections"); got != "least-connections" {
        t.Errorf("R3: stale strategy (age 10m > max_stale_age 50ms) must fall back to local 'least-connections', got %q (stale strategy trusted beyond bound — pre-R3永久-sticky bug)", got)
    }
}

// TestR3_MasterStrategy_FreshTrusted: a cached strategy younger than
// max_stale_age IS trusted (resolveStrategy returns the master strategy).
// Companion to the stale guard — proves the bound does not reject fresh
// strategies, only过期 ones.
func TestR3_MasterStrategy_FreshTrusted(t *testing.T) {
    srv := masterStrategyServer(t, "hardware-aware")
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL, MaxStaleAge: 10 * time.Minute},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()
    if !waitForMasterStrategy(d, "hardware-aware", 2*time.Second) {
        t.Fatal("master strategy hardware-aware not cached in time")
    }

    // Fresh (just synced) + max_stale_age 10m → trusted.
    if got := d.resolveStrategy("least-connections"); got != "hardware-aware" {
        t.Errorf("R3: fresh strategy must be trusted (return master 'hardware-aware'), got %q", got)
    }
}

// TestR3_MasterStrategy_MaxStaleZeroDisablesBound: max_stale_age=0 keeps the
// legacy永久-sticky behavior (the explicit opt-out) — even a strategy cached
// 10m ago is trusted. Proves the 0-disables semantics so operators who set 0
// intentionally are not surprised.
func TestR3_MasterStrategy_MaxStaleZeroDisablesBound(t *testing.T) {
    srv := masterStrategyServer(t, "hardware-aware")
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL, MaxStaleAge: 0},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()
    if !waitForMasterStrategy(d, "hardware-aware", 2*time.Second) {
        t.Fatal("master strategy hardware-aware not cached in time")
    }

    // Rewind 10m into the past, but max_stale_age=0 disables the bound → trusted.
    d.masterStrategy.Store(masterStrategyEntry{strategy: "hardware-aware", cachedAt: time.Now().Add(-10 * time.Minute)})
    if got := d.resolveStrategy("least-connections"); got != "hardware-aware" {
        t.Errorf("R3: max_stale_age=0 must disable the bound (trust even a 10m-old strategy), got %q", got)
    }
}
