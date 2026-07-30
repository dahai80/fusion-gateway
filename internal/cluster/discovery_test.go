package cluster

import (
    "context"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Tests for internal/cluster/discovery.go — Task #23 cluster node config & registration discovery
// Covers: NewDiscovery, loadNodesFromConfig, healthCheck, failureThreshold, SelectNode, UpdateConfig, ClusterSelectorAdapter, Status, Node.InFlight

func makeClusterCfg(enabled bool, nodes ...config.ClusterNodeConfig) config.ClusterConfig {
    return config.ClusterConfig{
        Enabled:             enabled,
        Nodes:               nodes,
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 1 * time.Second,
        FailureThreshold:    3,
        RecoveryInterval:    30 * time.Second,
    }
}

func TestNewDiscovery_Disabled(t *testing.T) {
    cfg := makeClusterCfg(false)
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    if d.running.Load() {
        t.Fatal("discovery should not be running when disabled")
    }
}

func TestDiscovery_LoadNodesFromConfig(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    all := d.AllNodes()
    if len(all) != 2 {
        t.Fatalf("expected 2 nodes, got %d", len(all))
    }

    n1, ok := d.GetNode("node-1")
    if !ok {
        t.Fatal("node-1 not found")
    }
    if n1.Address != "http://localhost:9001" {
        t.Errorf("expected address http://localhost:9001, got %s", n1.Address)
    }
    if n1.State() != NodeStateUnhealthy {
        t.Errorf("initial state should be unhealthy, got %s", n1.State())
    }
}

func TestDiscovery_HealthCheck(t *testing.T) {
    healthyCount := atomic.Int32{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if healthyCount.Load() > 0 {
            w.WriteHeader(http.StatusOK)
        } else {
            w.WriteHeader(http.StatusServiceUnavailable)
        }
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    d.checkAll()
    n1, _ := d.GetNode("node-1")
    if n1.State() != NodeStateUnhealthy {
        t.Errorf("expected unhealthy after 503, got %s", n1.State())
    }

    healthyCount.Add(1)
    d.checkAll()
    if n1.State() != NodeStateHealthy {
        t.Errorf("expected healthy after 200, got %s", n1.State())
    }

    if d.HealthyNodes() != 1 {
        t.Errorf("expected 1 healthy node, got %d", d.HealthyNodes())
    }
}

func TestDiscovery_FailureThreshold(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusServiceUnavailable)
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    cfg.FailureThreshold = 3

    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")

    for i := 0; i < 3; i++ {
        d.checkAll()
    }
    if n1.State() != NodeStateDead {
        t.Errorf("expected dead after 3 failures, got %s", n1.State())
    }

    if d.HealthyNodes() != 0 {
        t.Errorf("expected 0 healthy nodes, got %d", d.HealthyNodes())
    }
}

func TestDiscovery_SelectNode_LeastConnections(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n2, _ := d.GetNode("node-2")
    n1.markHealthy()
    n2.markHealthy()

    n1.IncrInFlight()
    n1.IncrInFlight()
    n2.IncrInFlight()

    selected, err := d.SelectNode("least-connections")
    if err != nil {
        t.Fatal(err)
    }
    if selected.ID != "node-2" {
        t.Errorf("expected node-2 (fewer connections), got %s", selected.ID)
    }
}

func TestDiscovery_SelectNode_HardwareAware(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 64},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n2, _ := d.GetNode("node-2")
    n1.markHealthy()
    n2.markHealthy()

    // With remote metrics: node-1 has 50% mem used, node-2 has 10% mem used
    n1.mu.Lock()
    n1.remoteMetrics = NodeRemoteMetrics{MemoryUsedRatio: 0.5, QueueDepth: 0, CollectedAt: time.Now()}
    n1.mu.Unlock()

    n2.mu.Lock()
    n2.remoteMetrics = NodeRemoteMetrics{MemoryUsedRatio: 0.1, QueueDepth: 0, CollectedAt: time.Now()}
    n2.mu.Unlock()

    selected, err := d.SelectNode("hardware-aware")
    if err != nil {
        t.Fatal(err)
    }
    // node-2: avail=64*0.9=57.6 → score=57.6*0.6 + 64*1*0.3 + 64*1*0.1 = 34.56+19.2+6.4 = 60.16
    // node-1: avail=16*0.5=8 → score=8*0.6 + 16*1*0.3 + 16*1*0.1 = 4.8+4.8+1.6 = 11.2
    if selected.ID != "node-2" {
        t.Errorf("expected node-2 (higher hw score), got %s", selected.ID)
    }
}

func TestDiscovery_SelectNode_HardwareAware_QueuePenalty(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 64},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 64},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n2, _ := d.GetNode("node-2")
    n1.markHealthy()
    n2.markHealthy()

    // Same memory, but node-2 has deep queue
    n1.mu.Lock()
    n1.remoteMetrics = NodeRemoteMetrics{MemoryUsedRatio: 0.2, QueueDepth: 0, CollectedAt: time.Now()}
    n1.mu.Unlock()

    n2.mu.Lock()
    n2.remoteMetrics = NodeRemoteMetrics{MemoryUsedRatio: 0.2, QueueDepth: 5, CollectedAt: time.Now()}
    n2.mu.Unlock()

    selected, err := d.SelectNode("hardware-aware")
    if err != nil {
        t.Fatal(err)
    }
    if selected.ID != "node-1" {
        t.Errorf("expected node-1 (no queue), got %s", selected.ID)
    }
}

func TestDiscovery_SelectNode_HardwareAware_NoRemoteMetrics(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 64},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n2, _ := d.GetNode("node-2")
    n1.markHealthy()
    n2.markHealthy()

    // No remote metrics — falls back to static MemoryGB / (inFlight+1)
    n2.IncrInFlight()

    selected, err := d.SelectNode("hardware-aware")
    if err != nil {
        t.Fatal(err)
    }
    // node-1: 16/(0+1)=16, node-2: 64/(1+1)=32 → node-2 still wins
    if selected.ID != "node-2" {
        t.Errorf("expected node-2 (higher static score), got %s", selected.ID)
    }
}

func TestDiscovery_SelectNode_RoundRobin(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    for _, n := range d.AllNodes() {
        n.markHealthy()
    }

    // Round-robin should distribute across both nodes
    counts := map[string]int{}
    for i := 0; i < 10; i++ {
        node, _ := d.SelectNode("round-robin")
        counts[node.ID]++
    }

    if len(counts) != 2 {
        t.Errorf("round-robin should hit both nodes, got %d nodes: %v", len(counts), counts)
    }
    for id, c := range counts {
        if c != 5 {
            t.Errorf("round-robin should distribute evenly, got %d hits for %s", c, id)
        }
    }
}

func TestDiscovery_SelectNode_NoHealthy(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    _, err := d.SelectNode("least-connections")
    if err == nil {
        t.Fatal("expected error when no healthy nodes")
    }
}

func TestDiscovery_UpdateConfig_AddAndModify(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    newCfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1Pro", MemoryGB: 32},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 64},
    )
    d.UpdateConfig(newCfg)

    all := d.AllNodes()
    if len(all) != 2 {
        t.Fatalf("expected 2 nodes after update, got %d", len(all))
    }

    n1, _ := d.GetNode("node-1")
    if n1.MemoryGB != 32 {
        t.Errorf("expected node-1 memory 32, got %d", n1.MemoryGB)
    }

    _, ok := d.GetNode("node-2")
    if !ok {
        t.Fatal("node-2 should exist after update")
    }
}

func TestDiscovery_UpdateConfig_RemoveNode(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    newCfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d.UpdateConfig(newCfg)

    _, ok := d.GetNode("node-2")
    if ok {
        t.Fatal("node-2 should be removed after config update")
    }
}

func TestClusterSelectorAdapter(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    sel := NewClusterSelectorAdapter(d)

    if sel.HealthyNodes() != 0 {
        t.Errorf("expected 0 healthy, got %d", sel.HealthyNodes())
    }

    n1, _ := d.GetNode("node-1")
    n1.markHealthy()

    if sel.HealthyNodes() != 1 {
        t.Errorf("expected 1 healthy, got %d", sel.HealthyNodes())
    }

    nodeID, err := sel.SelectNode("least-connections")
    if err != nil {
        t.Fatal(err)
    }
    if nodeID != "node-1" {
        t.Errorf("expected node-1, got %s", nodeID)
    }
}

func TestDiscovery_Status(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n1.markHealthy()
    n1.IncrInFlight()

    statuses := d.Status()
    if len(statuses) != 1 {
        t.Fatalf("expected 1 status, got %d", len(statuses))
    }
    if statuses[0].ID != "node-1" {
        t.Errorf("expected node-1, got %s", statuses[0].ID)
    }
    if statuses[0].State != string(NodeStateHealthy) {
        t.Errorf("expected healthy state, got %s", statuses[0].State)
    }
    if statuses[0].InFlight != 1 {
        t.Errorf("expected 1 in-flight, got %d", statuses[0].InFlight)
    }
}

func TestNode_InFlight(t *testing.T) {
    n := &Node{ID: "test", state: NodeStateHealthy}
    if n.InFlight() != 0 {
        t.Errorf("expected 0 in-flight, got %d", n.InFlight())
    }
    n.IncrInFlight()
    n.IncrInFlight()
    if n.InFlight() != 2 {
        t.Errorf("expected 2 in-flight, got %d", n.InFlight())
    }
    n.DecrInFlight()
    if n.InFlight() != 1 {
        t.Errorf("expected 1 in-flight, got %d", n.InFlight())
    }
}
