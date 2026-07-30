package cluster

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestMasterClient_ListNodes(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/nodes" {
            t.Errorf("unexpected path: %s", r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer test-token" {
            t.Errorf("missing auth header")
        }
        resp := MasterNodesResponse{
            Total:  2,
            Online: 2,
            Nodes: []MasterNodeInfo{
                {NodeID: "node-1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 16, Status: "online"},
                {NodeID: "node-2", Address: "http://10.0.0.2:8100", GPU: "M2Ultra", MemoryGB: 128, Status: "online"},
            },
        }
        json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{
        Address:     srv.URL,
        SharedToken: "test-token",
    })

    resp, err := mc.ListNodes(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if resp.Total != 2 || resp.Online != 2 {
        t.Errorf("expected total=2 online=2, got total=%d online=%d", resp.Total, resp.Online)
    }
    if len(resp.Nodes) != 2 {
        t.Fatalf("expected 2 nodes, got %d", len(resp.Nodes))
    }
}

func TestMasterClient_HealthCheck(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(MasterNodesResponse{Total: 0, Online: 0})
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    if err := mc.HealthCheck(context.Background()); err != nil {
        t.Fatal(err)
    }
}

func TestMasterClient_HealthCheck_Failure(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    if err := mc.HealthCheck(context.Background()); err == nil {
        t.Fatal("expected error on 500")
    }
}

func TestDiscovery_MasterMode_Sync(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{
            Total:  1,
            Online: 1,
            Nodes: []MasterNodeInfo{
                {NodeID: "worker-1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "online", MemoryUsed: 0.3, QueueDepth: 2},
            },
        }
        json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:             true,
        Mode:                config.ClusterModeMaster,
        Master:              config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer:        "least-connections",
    }

    d := NewDiscovery(cfg)
    d.syncFromMaster()

    all := d.AllNodes()
    if len(all) != 1 {
        t.Fatalf("expected 1 node after sync, got %d", len(all))
    }

    n, ok := d.GetNode("worker-1")
    if !ok {
        t.Fatal("worker-1 not found")
    }
    if n.Address != "http://10.0.0.1:8100" {
        t.Errorf("expected address http://10.0.0.1:8100, got %s", n.Address)
    }
    if n.State() != NodeStateHealthy {
        t.Errorf("expected healthy for online node, got %s", n.State())
    }
    metrics := n.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0.3 {
        t.Errorf("expected mem_used 0.3, got %f", metrics.MemoryUsedRatio)
    }
    if metrics.QueueDepth != 2 {
        t.Errorf("expected queue_depth 2, got %d", metrics.QueueDepth)
    }
    if d.HealthyNodes() != 1 {
        t.Errorf("expected 1 healthy, got %d", d.HealthyNodes())
    }
}

func TestDiscovery_MasterMode_SelectNode(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{
            Total:  2,
            Online: 2,
            Nodes: []MasterNodeInfo{
                {NodeID: "worker-1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "online"},
                {NodeID: "worker-2", Address: "http://10.0.0.2:8100", GPU: "M2Ultra", MemoryGB: 128, Status: "online"},
            },
        }
        json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    cfg := config.ClusterConfig{
        Enabled:      true,
        Mode:         config.ClusterModeMaster,
        Master:       config.ClusterMasterConfig{Address: srv.URL},
        LoadBalancer: "least-connections",
    }

    d := NewDiscovery(cfg)
    d.syncFromMaster()

    selected, err := d.SelectNode("least-connections")
    if err != nil {
        t.Fatal(err)
    }
    if selected.ID != "worker-1" && selected.ID != "worker-2" {
        t.Errorf("unexpected node selected: %s", selected.ID)
    }
}
