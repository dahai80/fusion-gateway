package cluster

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
)

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

    n2.IncrInFlight()

    selected, err := d.SelectNode("hardware-aware")
    if err != nil {
        t.Fatal(err)
    }
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

func TestNode_InFlight_PublishesMetrics(t *testing.T) {
    t.Log("testing IncrInFlight/DecrInFlight publish to in_flight_requests gauge (#96)")
    n := &Node{ID: "metric-node", state: NodeStateHealthy}
    n.IncrInFlight()
    n.IncrInFlight()

    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    want := `fusion_gateway_in_flight_requests{backend="cluster-metric-node"} 2`
    if !strings.Contains(body, want) {
        t.Errorf("metrics missing %s\n got: %s", want, body)
    }

    n.DecrInFlight()
    rec2 := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec2, req)
    body2 := rec2.Body.String()
    want2 := `fusion_gateway_in_flight_requests{backend="cluster-metric-node"} 1`
    if !strings.Contains(body2, want2) {
        t.Errorf("metrics missing %s\n got: %s", want2, body2)
    }
}

func TestDiscovery_SelectNodeByModel_PrefersServingNode(t *testing.T) {
    t.Log("testing SelectNodeByModel prefers a node serving the model (#95)")
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
        config.ClusterNodeConfig{ID: "node-3", Address: "http://localhost:9003", GPU: "M3", MemoryGB: 64},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    for _, id := range []string{"node-1", "node-2", "node-3"} {
        n, _ := d.GetNode(id)
        n.markHealthy()
    }

    // only node-2 serves qwen3
    n2, _ := d.GetNode("node-2")
    n2.mu.Lock()
    n2.models = []string{"qwen3"}
    n2.mu.Unlock()

    selected, err := d.SelectNodeByModel("least-connections", "qwen3", 0)
    if err != nil {
        t.Fatalf("expected node-2, got error: %v", err)
    }
    if selected.ID != "node-2" {
        t.Errorf("expected node-2 (serves qwen3), got %s", selected.ID)
    }

    // no node serves absent-model
    if _, err := d.SelectNodeByModel("least-connections", "absent-model", 0); err == nil {
        t.Error("expected error when no node serves absent-model")
    }
}

func TestDiscovery_SelectNodeByModel_EmptyModel_Legacy(t *testing.T) {
    t.Log("testing empty model falls back to model-agnostic SelectNode (#95)")
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

    selected, err := d.SelectNodeByModel("least-connections", "", 0)
    if err != nil {
        t.Fatal(err)
    }
    // empty model = legacy SelectNode (any healthy node, least-connections → node-2)
    if selected.ID != "node-2" {
        t.Errorf("expected node-2 (legacy least-connections), got %s", selected.ID)
    }
}

func TestDiscovery_SelectNodeByModel_SkipsCappedNodes(t *testing.T) {
    t.Log("testing SelectNodeByModel skips nodes at slot cap (#102 ADR-001)")
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-a", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-b", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    na, _ := d.GetNode("node-a")
    nb, _ := d.GetNode("node-b")
    na.markHealthy()
    nb.markHealthy()
    // both serve qwen3
    na.mu.Lock()
    na.models = []string{"qwen3"}
    na.mu.Unlock()
    nb.mu.Lock()
    nb.models = []string{"qwen3"}
    nb.mu.Unlock()

    // node-a filled to cap (max_concurrent=2); node-b free → node-b selected
    na.IncrInFlight()
    na.IncrInFlight()
    selected, err := d.SelectNodeByModel("least-connections", "qwen3", 2)
    if err != nil {
        t.Fatalf("expected node-b (node-a capped), got error: %v", err)
    }
    if selected.ID != "node-b" {
        t.Errorf("expected node-b (node-a at cap), got %s", selected.ID)
    }

    // both nodes at cap → error, no node below cap
    nb.IncrInFlight()
    nb.IncrInFlight()
    if _, err := d.SelectNodeByModel("least-connections", "qwen3", 2); err == nil {
        t.Error("expected error when all serving nodes are at slot cap")
    }

    // maxConcurrent <= 0 disables cap → node-a (least-connections tie, first wins) selectable again
    if _, err := d.SelectNodeByModel("least-connections", "qwen3", 0); err != nil {
        t.Errorf("expected no error with cap disabled (maxConcurrent=0), got: %v", err)
    }
}

func TestDiscovery_FetchModels_PopulatesNodeModels(t *testing.T) {
    t.Log("testing fetchModels GET /v1/models populates node.models (#95)")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/models" {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(`[{"id":"qwen3","object":"model"},{"id":"llama3","object":"model"}]`))
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n1.markHealthy()
    d.fetchModels(n1)

    got := n1.Models()
    if len(got) != 2 {
        t.Fatalf("expected 2 models, got %d: %v", len(got), got)
    }
    want := map[string]bool{"qwen3": true, "llama3": true}
    for _, m := range got {
        if !want[m] {
            t.Errorf("unexpected model %q", m)
        }
    }
    if !n1.servesModel("qwen3") {
        t.Error("expected servesModel(qwen3) true after fetchModels")
    }
    if n1.servesModel("absent") {
        t.Error("expected servesModel(absent) false")
    }
}

func TestNode_LastHealth(t *testing.T) {
    n := &Node{ID: "test", state: NodeStateUnhealthy}
    if !n.LastHealth().IsZero() {
        t.Errorf("expected zero lastHealth initially")
    }
    n.markHealthy()
    if n.LastHealth().IsZero() {
        t.Errorf("expected non-zero lastHealth after markHealthy")
    }
}

func TestDiscovery_Stop(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.Start(context.Background())

    if !d.running.Load() {
        t.Fatal("discovery should be running after start")
    }

    d.Stop()
    if d.running.Load() {
        t.Fatal("discovery should not be running after stop")
    }
}

func TestDiscovery_Stop_NotRunning(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    // Stop on non-running discovery should be a no-op
    d.Stop()
    if d.running.Load() {
        t.Fatal("discovery should not be running")
    }
}

func TestDiscovery_Start_AlreadyRunning(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()

    // Second start should be a no-op
    d.Start(context.Background())
}

func TestDiscovery_Start_StandaloneMode(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    cfg.HealthCheckInterval = 100 * time.Millisecond

    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()

    // Wait for health check loop to run at least once
    time.Sleep(300 * time.Millisecond)

    n1, _ := d.GetNode("node-1")
    if n1.State() != NodeStateHealthy {
        t.Errorf("expected healthy after health check loop, got %s", n1.State())
    }
}

func TestDiscovery_Start_MasterMode(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{
            Total:  1,
            Online: 1,
            Nodes: []MasterNodeInfo{
                {NodeID: "worker-1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "online"},
            },
        }
        _ = json.NewEncoder(w).Encode(resp)
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

    // Wait for master sync loop to run
    time.Sleep(300 * time.Millisecond)

    all := d.AllNodes()
    if len(all) != 1 {
        t.Fatalf("expected 1 node from master sync, got %d", len(all))
    }
}

func TestDiscovery_healthCheckLoop_ContextCancel(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    cfg.HealthCheckInterval = 50 * time.Millisecond

    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        d.healthCheckLoop(ctx)
        close(done)
    }()

    time.Sleep(100 * time.Millisecond)
    cancel()

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("healthCheckLoop should exit on context cancel")
    }
}

func TestDiscovery_healthCheckLoop_StopCh(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    cfg.HealthCheckInterval = 50 * time.Millisecond

    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    done := make(chan struct{})
    go func() {
        d.healthCheckLoop(context.Background())
        close(done)
    }()

    time.Sleep(100 * time.Millisecond)
    close(d.stopCh)

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("healthCheckLoop should exit on stopCh")
    }
}

func TestDiscovery_masterSyncLoop_ContextCancel(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{Total: 0, Online: 0}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 50 * time.Millisecond,
    }

    d := NewDiscovery(cfg)

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        d.masterSyncLoop(ctx)
        close(done)
    }()

    time.Sleep(100 * time.Millisecond)
    cancel()

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("masterSyncLoop should exit on context cancel")
    }
}

func TestDiscovery_masterSyncLoop_StopCh(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{Total: 0, Online: 0}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer:        "least-connections",
        HealthCheckInterval: 50 * time.Millisecond,
    }

    d := NewDiscovery(cfg)

    done := make(chan struct{})
    go func() {
        d.masterSyncLoop(context.Background())
        close(done)
    }()

    time.Sleep(100 * time.Millisecond)
    close(d.stopCh)

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("masterSyncLoop should exit on stopCh")
    }
}

func TestDiscovery_checkNode_HealthyWithMetrics(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/health" {
            w.WriteHeader(http.StatusOK)
            return
        }
        if r.URL.Path == "/v1/status" {
            resp := struct {
                Hardware struct {
                    MemoryUsedRatio float64 `json:"memory_used_ratio"`
                } `json:"hardware"`
                Backends struct {
                    FusionMLX struct {
                        QueueDepth int `json:"queue_depth"`
                    } `json:"fusion_mlx"`
                } `json:"backends"`
            }{}
            resp.Hardware.MemoryUsedRatio = 0.65
            resp.Backends.FusionMLX.QueueDepth = 7
            _ = json.NewEncoder(w).Encode(resp)
            return
        }
        w.WriteHeader(http.StatusNotFound)
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    d.checkNode(n1)

    if n1.State() != NodeStateHealthy {
        t.Errorf("expected healthy, got %s", n1.State())
    }
    metrics := n1.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0.65 {
        t.Errorf("expected 0.65, got %f", metrics.MemoryUsedRatio)
    }
    if metrics.QueueDepth != 7 {
        t.Errorf("expected 7, got %d", metrics.QueueDepth)
    }
    if metrics.CollectedAt.IsZero() {
        t.Errorf("expected non-zero CollectedAt")
    }
}

func TestDiscovery_checkNode_FailedRequest(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://127.0.0.1:1", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()
    d.client.Timeout = 1 * time.Second

    n1, _ := d.GetNode("node-1")
    d.checkNode(n1)

    if n1.State() != NodeStateUnhealthy {
        t.Errorf("expected unhealthy after connection failure, got %s", n1.State())
    }
}

func TestDiscovery_fetchRemoteMetrics_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("error"))
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)

    n1 := &Node{ID: "node-1", Address: srv.URL, state: NodeStateHealthy}
    d.fetchRemoteMetrics(n1)

    metrics := n1.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0 {
        t.Errorf("metrics should not be updated on non-200, got %f", metrics.MemoryUsedRatio)
    }
}

func TestDiscovery_fetchRemoteMetrics_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("not json"))
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)

    n1 := &Node{ID: "node-1", Address: srv.URL, state: NodeStateHealthy}
    d.fetchRemoteMetrics(n1)

    metrics := n1.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0 {
        t.Errorf("metrics should not be updated on decode error, got %f", metrics.MemoryUsedRatio)
    }
}

func TestDiscovery_fetchRemoteMetrics_ConnectionFailed(t *testing.T) {
    cfg := makeClusterCfg(true)
    d := NewDiscovery(cfg)

    n1 := &Node{ID: "node-1", Address: "http://127.0.0.1:1", state: NodeStateHealthy}
    d.client.Timeout = 1 * time.Second
    d.fetchRemoteMetrics(n1)

    metrics := n1.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0 {
        t.Errorf("metrics should not be updated on connection failure, got %f", metrics.MemoryUsedRatio)
    }
}

func TestDiscovery_fetchRemoteMetrics_BadURL(t *testing.T) {
    cfg := makeClusterCfg(true)
    d := NewDiscovery(cfg)

    n1 := &Node{ID: "node-1", Address: "://bad", state: NodeStateHealthy}
    d.fetchRemoteMetrics(n1)

    metrics := n1.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0 {
        t.Errorf("metrics should not be updated on bad URL, got %f", metrics.MemoryUsedRatio)
    }
}

func TestDiscovery_checkFailureThreshold_DefaultThreshold(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    cfg.FailureThreshold = 0 // use default

    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    // Default threshold is 3
    for i := 0; i < 3; i++ {
        n1.markUnhealthy()
    }
    d.checkFailureThreshold(n1)
    if n1.State() != NodeStateDead {
        t.Errorf("expected dead after 3 failures with default threshold, got %s", n1.State())
    }
}

func TestDiscovery_checkFailureThreshold_NotReached(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    cfg.FailureThreshold = 5

    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n1.markUnhealthy()
    d.checkFailureThreshold(n1)
    if n1.State() == NodeStateDead {
        t.Errorf("node should not be dead with only 1 failure and threshold 5")
    }
}

func TestDiscovery_LoadNodesFromConfig_DuplicateID(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    all := d.AllNodes()
    if len(all) != 1 {
        t.Fatalf("duplicate IDs should be deduplicated, got %d", len(all))
    }

    n1, _ := d.GetNode("node-1")
    if n1.Address != "http://localhost:9001" {
        t.Errorf("should keep first occurrence, got %s", n1.Address)
    }
}

func TestDiscovery_LoadNodesFromConfig_Idempotent(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()
    d.loadNodesFromConfig()

    all := d.AllNodes()
    if len(all) != 1 {
        t.Errorf("duplicate loadNodesFromConfig calls should not duplicate nodes, got %d", len(all))
    }
}

func TestDiscovery_SelectNode_DefaultStrategy(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n1.markHealthy()

    // Default strategy should fall back to least-connections
    selected, err := d.SelectNode("unknown-strategy")
    if err != nil {
        t.Fatal(err)
    }
    if selected.ID != "node-1" {
        t.Errorf("expected node-1 with default strategy, got %s", selected.ID)
    }
}

func TestDiscovery_SelectNodeID_Error(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    _, err := d.SelectNodeID("least-connections")
    if err == nil {
        t.Fatal("expected error when no healthy nodes for SelectNodeID")
    }
}

func TestDiscovery_SelectNodeID_Success(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n1.markHealthy()

    id, err := d.SelectNodeID("least-connections")
    if err != nil {
        t.Fatal(err)
    }
    if id != "node-1" {
        t.Errorf("expected node-1, got %s", id)
    }
}

func TestDiscovery_HealthyNodeList(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16},
        config.ClusterNodeConfig{ID: "node-2", Address: "http://localhost:9002", GPU: "M2", MemoryGB: 32},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("node-1")
    n1.markHealthy()

    list := d.HealthyNodeList()
    if len(list) != 1 {
        t.Fatalf("expected 1 healthy node, got %d", len(list))
    }
    if list[0].ID != "node-1" {
        t.Errorf("expected node-1, got %s", list[0].ID)
    }
}

func TestNode_markDead(t *testing.T) {
    n := &Node{ID: "test", state: NodeStateUnhealthy}
    n.markDead()
    if n.State() != NodeStateDead {
        t.Errorf("expected dead, got %s", n.State())
    }
}

func TestDiscovery_Start_EmptyMode(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "node-1", Address: srv.URL, GPU: "M1", MemoryGB: 16},
    )
    cfg.Mode = "" // empty mode should default to standalone
    cfg.HealthCheckInterval = 100 * time.Millisecond

    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()

    time.Sleep(300 * time.Millisecond)

    n1, _ := d.GetNode("node-1")
    if n1.State() != NodeStateHealthy {
        t.Errorf("expected healthy with empty mode defaulting to standalone, got %s", n1.State())
    }
}

func TestDiscovery_checkNode_BadURL(t *testing.T) {
    cfg := makeClusterCfg(true)
    d := NewDiscovery(cfg)

    n1 := &Node{ID: "bad-node", Address: "://invalid", state: NodeStateUnhealthy}
    d.checkNode(n1)

    if n1.State() != NodeStateUnhealthy {
        t.Errorf("expected unhealthy after bad URL, got %s", n1.State())
    }
}

func TestDiscovery_nodeScore_NegativeMemAvail(t *testing.T) {
    n := &Node{ID: "n1", MemoryGB: 16}
    n.mu.Lock()
    n.remoteMetrics = NodeRemoteMetrics{MemoryUsedRatio: 1.5, CollectedAt: time.Now()}
    n.mu.Unlock()

    d := NewDiscovery(makeClusterCfg(true))
    score := d.nodeScore(n)

    // memAvail = (1-1.5)*16 = -8, clamped to 0
    if score < 0 {
        t.Errorf("score should not be negative, got %f", score)
    }
}

func TestDiscovery_nodeScore_WithInFlight(t *testing.T) {
    n := &Node{ID: "n1", MemoryGB: 16}
    n.mu.Lock()
    n.remoteMetrics = NodeRemoteMetrics{MemoryUsedRatio: 0.5, QueueDepth: 2, CollectedAt: time.Now()}
    n.mu.Unlock()
    n.IncrInFlight()

    d := NewDiscovery(makeClusterCfg(true))
    score := d.nodeScore(n)
    t.Logf("nodeScore with inFlight: %f", score)
    if score <= 0 {
        t.Errorf("score should be positive, got %f", score)
    }
}

func TestDiscovery_nodeScore_StaticWithInFlight(t *testing.T) {
    n := &Node{ID: "n1", MemoryGB: 16}
    // No remote metrics, with in-flight
    n.IncrInFlight()

    d := NewDiscovery(makeClusterCfg(true))
    score := d.nodeScore(n)
    // Static: memScore = 16/(1+1) = 8
    if score != 8.0 {
        t.Errorf("expected 8.0, got %f", score)
    }
}

func TestDiscovery_nodeScore_StaticNoInFlight(t *testing.T) {
    n := &Node{ID: "n1", MemoryGB: 16}
    // No remote metrics, no in-flight

    d := NewDiscovery(makeClusterCfg(true))
    score := d.nodeScore(n)
    // Static: memScore = 16/(0+1) = 16
    if score != 16.0 {
        t.Errorf("expected 16.0, got %f", score)
    }
}

func TestDiscovery_fetchRemoteMetrics_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/status" {
            w.WriteHeader(http.StatusNotFound)
            return
        }
        resp := struct {
            Hardware struct {
                MemoryUsedRatio float64 `json:"memory_used_ratio"`
            } `json:"hardware"`
            Backends struct {
                FusionMLX struct {
                    QueueDepth int `json:"queue_depth"`
                } `json:"fusion_mlx"`
            } `json:"backends"`
        }{}
        resp.Hardware.MemoryUsedRatio = 0.42
        resp.Backends.FusionMLX.QueueDepth = 3
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    cfg := makeClusterCfg(true)
    d := NewDiscovery(cfg)

    n1 := &Node{ID: "node-1", Address: srv.URL, state: NodeStateHealthy}
    d.fetchRemoteMetrics(n1)

    metrics := n1.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0.42 {
        t.Errorf("expected 0.42, got %f", metrics.MemoryUsedRatio)
    }
    if metrics.QueueDepth != 3 {
        t.Errorf("expected 3, got %d", metrics.QueueDepth)
    }
}

func TestDiscovery_LoadNodesFromConfig_Platform(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "mac-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16, Platform: "mac"},
        config.ClusterNodeConfig{ID: "win-1", Address: "http://localhost:9002", GPU: "RTX4090", MemoryGB: 48, Platform: "windows-cuda"},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n, ok := d.GetNode("win-1")
    if !ok {
        t.Fatal("win-1 not found")
    }
    if n.Platform != "windows-cuda" {
        t.Errorf("expected platform windows-cuda, got %q", n.Platform)
    }
    n2, ok := d.GetNode("mac-1")
    if !ok {
        t.Fatal("mac-1 not found")
    }
    if n2.Platform != "mac" {
        t.Errorf("expected platform mac, got %q", n2.Platform)
    }
}

func TestDiscovery_HealthyNodesByPlatform(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "mac-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16, Platform: "mac"},
        config.ClusterNodeConfig{ID: "win-1", Address: "http://localhost:9002", GPU: "RTX4090", MemoryGB: 48, Platform: "windows-cuda"},
        config.ClusterNodeConfig{ID: "win-2", Address: "http://localhost:9003", GPU: "RTX4090", MemoryGB: 48, Platform: "windows-cuda"},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    if got := d.HealthyNodesByPlatform("windows-cuda"); got != 0 {
        t.Fatalf("expected 0 healthy windows-cuda, got %d", got)
    }

    n1, _ := d.GetNode("mac-1")
    n2, _ := d.GetNode("win-1")
    n3, _ := d.GetNode("win-2")
    n1.markHealthy()
    n2.markHealthy()
    n3.markHealthy()

    if got := d.HealthyNodesByPlatform("windows-cuda"); got != 2 {
        t.Fatalf("expected 2 healthy windows-cuda, got %d", got)
    }
    if got := d.HealthyNodesByPlatform("mac"); got != 1 {
        t.Fatalf("expected 1 healthy mac, got %d", got)
    }
    if got := d.HealthyNodesByPlatform("rocm"); got != 0 {
        t.Fatalf("expected 0 healthy on unknown platform, got %d", got)
    }
    if got := d.HealthyNodesByPlatform(""); got != 3 {
        t.Fatalf("expected 3 healthy total for empty platform, got %d", got)
    }
}

func TestDiscovery_SelectNodeByPlatform(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "mac-1", Address: "http://localhost:9001", GPU: "M1", MemoryGB: 16, Platform: "mac"},
        config.ClusterNodeConfig{ID: "win-1", Address: "http://localhost:9002", GPU: "RTX4090", MemoryGB: 48, Platform: "windows-cuda"},
        config.ClusterNodeConfig{ID: "win-2", Address: "http://localhost:9003", GPU: "RTX4090", MemoryGB: 48, Platform: "windows-cuda"},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    n1, _ := d.GetNode("mac-1")
    n2, _ := d.GetNode("win-1")
    n3, _ := d.GetNode("win-2")
    n1.markHealthy()
    n2.markHealthy()
    n3.markHealthy()

    n2.IncrInFlight()
    n2.IncrInFlight()
    n3.IncrInFlight()

    selected, err := d.SelectNodeByPlatform("least-connections", "windows-cuda")
    if err != nil {
        t.Fatal(err)
    }
    if selected.ID != "win-2" {
        t.Errorf("expected win-2 (fewer connections on windows-cuda), got %s", selected.ID)
    }
    if selected.Platform != "windows-cuda" {
        t.Errorf("selected node platform = %q, want windows-cuda", selected.Platform)
    }

    macSel, err := d.SelectNodeByPlatform("least-connections", "mac")
    if err != nil {
        t.Fatal(err)
    }
    if macSel.ID != "mac-1" {
        t.Errorf("expected mac-1, got %s", macSel.ID)
    }

    anySel, err := d.SelectNodeByPlatform("least-connections", "")
    if err != nil {
        t.Fatal(err)
    }
    if anySel.ID == "" {
        t.Fatal("expected a node for empty platform fallback")
    }
}

func TestDiscovery_SelectNodeByPlatform_NoHealthy(t *testing.T) {
    cfg := makeClusterCfg(true,
        config.ClusterNodeConfig{ID: "win-1", Address: "http://localhost:9002", GPU: "RTX4090", MemoryGB: 48, Platform: "windows-cuda"},
    )
    d := NewDiscovery(cfg)
    d.loadNodesFromConfig()

    _, err := d.SelectNodeByPlatform("least-connections", "windows-cuda")
    if err == nil {
        t.Fatal("expected error when no healthy nodes on platform")
    }
}

// TestDiscovery_MasterMode_ModelAwareRouting (AH3, audit P1): master mode skips
// healthCheckLoop, so without syncNodeModels a node's servesModel stays empty
// and SelectNodeByModel silently degrades every request to cloud. This test
// proves the AH3 fix — after master sync + model sync, SelectNodeByModel finds
// the node serving the requested model.
func TestDiscovery_MasterMode_ModelAwareRouting(t *testing.T) {
    t.Log("AH3: master mode populates per-node model registry so model-aware routing works")
    // One server serves both the master /api/nodes list and the worker's
    // /v1/models registry (the node's Address points back at this server).
    var srv *httptest.Server
    srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/api/nodes":
            resp := MasterNodesResponse{
                Total:  1,
                Online: 1,
                Nodes: []MasterNodeInfo{
                    {NodeID: "worker-1", Address: srv.URL, GPU: "M2", MemoryGB: 32, Status: "online"},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        case "/v1/models":
            // The worker exposes this model in its registry.
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

    // Wait for master sync + model sync to run.
    time.Sleep(300 * time.Millisecond)

    // AH3 core assertion: the node's model registry is populated, so
    // SelectNodeByModel finds worker-1 for served-model instead of returning
    // "no healthy node serving model" (the pre-fix silent cloud fallback).
    selected, err := d.SelectNodeByModel("least-connections", "served-model", 0)
    if err != nil {
        t.Fatalf("expected worker-1 serving served-model, got error: %v (AH3 model sync did not populate registry)", err)
    }
    if selected.ID != "worker-1" {
        t.Errorf("expected worker-1, got %s", selected.ID)
    }

    // A model no worker exposes still has no serving node (correct cloud fallback).
    if _, err := d.SelectNodeByModel("least-connections", "absent-model", 0); err == nil {
        t.Error("expected error when no node serves absent-model")
    }
}

// TestDiscovery_MasterMode_NoHealthyNodes_ModelSyncNoOp (AH3): syncNodeModels
// logs + returns when master reports no online nodes, without erroring.
func TestDiscovery_MasterMode_NoHealthyNodes_ModelSyncNoOp(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{Total: 1, Online: 0, Nodes: []MasterNodeInfo{
            {NodeID: "worker-1", Address: "http://10.0.0.1:8100", Status: "offline"},
        }}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        HealthCheckInterval: 100 * time.Millisecond,
    }
    d := NewDiscovery(cfg)
    d.Start(context.Background())
    defer d.Stop()

    time.Sleep(300 * time.Millisecond)

    all := d.AllNodes()
    if len(all) != 1 {
        t.Fatalf("expected 1 node synced (offline), got %d", len(all))
    }
    for _, n := range all {
        if n.State() == NodeStateHealthy {
            t.Errorf("expected all nodes offline, %s is healthy", n.ID)
        }
    }
}
