package cluster

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
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
        _ = json.NewEncoder(w).Encode(resp)
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

func TestMasterClient_ListNodes_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = io.WriteString(w, "internal error")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.ListNodes(context.Background())
    if err == nil {
        t.Fatal("expected error on non-200")
    }
    if !strings.Contains(err.Error(), "status 500") {
        t.Errorf("error should mention status 500: %v", err)
    }
}

func TestMasterClient_ListNodes_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, "not json")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.ListNodes(context.Background())
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestMasterClient_ListNodes_ConnectionFailed(t *testing.T) {
    mc := NewMasterClient(config.ClusterMasterConfig{Address: "http://127.0.0.1:1"})
    mc.client.Timeout = 1e9
    _, err := mc.ListNodes(context.Background())
    if err == nil {
        t.Fatal("expected connection error")
    }
}

func TestMasterClient_GetNode_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        expected := "/api/nodes/worker-1"
        if r.URL.Path != expected {
            t.Errorf("expected path %s, got %s", expected, r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer my-token" {
            t.Errorf("missing auth header")
        }
        node := MasterNodeInfo{
            NodeID:     "worker-1",
            Address:    "http://10.0.0.1:8100",
            GPU:        "M2",
            MemoryGB:   32,
            Status:     "online",
            MemoryUsed: 0.4,
            QueueDepth: 3,
        }
        _ = json.NewEncoder(w).Encode(node)
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{
        Address:     srv.URL,
        SharedToken: "my-token",
    })

    node, err := mc.GetNode(context.Background(), "worker-1")
    if err != nil {
        t.Fatal(err)
    }
    if node.NodeID != "worker-1" {
        t.Errorf("expected worker-1, got %s", node.NodeID)
    }
    if node.MemoryGB != 32 {
        t.Errorf("expected 32 GB, got %d", node.MemoryGB)
    }
    if node.MemoryUsed != 0.4 {
        t.Errorf("expected mem_used 0.4, got %f", node.MemoryUsed)
    }
    if node.QueueDepth != 3 {
        t.Errorf("expected queue_depth 3, got %d", node.QueueDepth)
    }
}

func TestMasterClient_GetNode_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusNotFound)
        _, _ = io.WriteString(w, "node not found")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.GetNode(context.Background(), "nonexistent")
    if err == nil {
        t.Fatal("expected error on non-200")
    }
    if !strings.Contains(err.Error(), "status 404") {
        t.Errorf("error should mention status 404: %v", err)
    }
}

func TestMasterClient_GetNode_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, "not json")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.GetNode(context.Background(), "worker-1")
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestMasterClient_GetNode_ConnectionFailed(t *testing.T) {
    mc := NewMasterClient(config.ClusterMasterConfig{Address: "http://127.0.0.1:1"})
    mc.client.Timeout = 1e9
    _, err := mc.GetNode(context.Background(), "worker-1")
    if err == nil {
        t.Fatal("expected connection error")
    }
}

func TestMasterClient_SubmitTask_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/tasks/submit" {
            t.Errorf("expected /api/tasks/submit, got %s", r.URL.Path)
        }
        if r.Method != http.MethodPost {
            t.Errorf("expected POST, got %s", r.Method)
        }
        if r.Header.Get("Content-Type") != "application/json" {
            t.Errorf("expected Content-Type application/json")
        }
        if r.Header.Get("Authorization") != "Bearer task-token" {
            t.Errorf("missing auth header")
        }

        var req TaskSubmitRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            t.Errorf("decode request: %v", err)
        }

        resp := TaskSubmitResponse{
            TaskID:    "task-123",
            Name:      req.Name,
            Status:    "assigned",
            NodeID:    "worker-1",
            CreatedAt: 1700000000.0,
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{
        Address:     srv.URL,
        SharedToken: "task-token",
    })

    req := &TaskSubmitRequest{
        Name:               "test-task",
        Mode:               "inference",
        ModelName:          "qwen-7b",
        TimeoutSeconds:     300,
        User:               "test-user",
        RequiredCapability: "gpu",
        PreferredNodeID:    "worker-1",
        Priority:           1,
    }
    resp, err := mc.SubmitTask(context.Background(), req)
    if err != nil {
        t.Fatal(err)
    }
    if resp.TaskID != "task-123" {
        t.Errorf("expected task-123, got %s", resp.TaskID)
    }
    if resp.NodeID != "worker-1" {
        t.Errorf("expected worker-1, got %s", resp.NodeID)
    }
    if resp.Status != "assigned" {
        t.Errorf("expected assigned, got %s", resp.Status)
    }
}

func TestMasterClient_SubmitTask_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusServiceUnavailable)
        _, _ = io.WriteString(w, "no available nodes")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    req := &TaskSubmitRequest{Name: "test", Mode: "inference", ModelName: "test"}
    _, err := mc.SubmitTask(context.Background(), req)
    if err == nil {
        t.Fatal("expected error on non-200")
    }
    if !strings.Contains(err.Error(), "status 503") {
        t.Errorf("error should mention status 503: %v", err)
    }
}

func TestMasterClient_SubmitTask_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, "not json")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    req := &TaskSubmitRequest{Name: "test", Mode: "inference", ModelName: "test"}
    _, err := mc.SubmitTask(context.Background(), req)
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestMasterClient_SubmitTask_ConnectionFailed(t *testing.T) {
    mc := NewMasterClient(config.ClusterMasterConfig{Address: "http://127.0.0.1:1"})
    mc.client.Timeout = 1e9
    req := &TaskSubmitRequest{Name: "test", Mode: "inference", ModelName: "test"}
    _, err := mc.SubmitTask(context.Background(), req)
    if err == nil {
        t.Fatal("expected connection error")
    }
}

func TestMasterClient_SubmitTask_NoSharedToken(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no shared token")
        }
        w.WriteHeader(http.StatusOK)
        resp := TaskSubmitResponse{TaskID: "task-1", Status: "assigned", NodeID: "w1", CreatedAt: 1.0}
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    req := &TaskSubmitRequest{Name: "test", Mode: "inference", ModelName: "test"}
    _, err := mc.SubmitTask(context.Background(), req)
    if err != nil {
        t.Fatal(err)
    }
}

func TestMasterClient_RoutingSummary_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/routing/summary" {
            t.Errorf("expected /api/routing/summary, got %s", r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer routing-token" {
            t.Errorf("missing auth header")
        }
        resp := MasterRoutingSummary{
            Strategy:   "least-connections",
            TotalNodes: 5,
            AvgLoad:    0.45,
            TotalTasks: 12,
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{
        Address:     srv.URL,
        SharedToken: "routing-token",
    })

    summary, err := mc.RoutingSummary(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if summary.Strategy != "least-connections" {
        t.Errorf("expected least-connections, got %s", summary.Strategy)
    }
    if summary.TotalNodes != 5 {
        t.Errorf("expected 5 nodes, got %d", summary.TotalNodes)
    }
    if summary.AvgLoad != 0.45 {
        t.Errorf("expected 0.45, got %f", summary.AvgLoad)
    }
    if summary.TotalTasks != 12 {
        t.Errorf("expected 12 tasks, got %d", summary.TotalTasks)
    }
}

func TestMasterClient_RoutingSummary_Non200(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = io.WriteString(w, "error")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.RoutingSummary(context.Background())
    if err == nil {
        t.Fatal("expected error on non-200")
    }
}

func TestMasterClient_RoutingSummary_DecodeError(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, "not json")
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.RoutingSummary(context.Background())
    if err == nil {
        t.Fatal("expected decode error")
    }
}

func TestMasterClient_RoutingSummary_ConnectionFailed(t *testing.T) {
    mc := NewMasterClient(config.ClusterMasterConfig{Address: "http://127.0.0.1:1"})
    mc.client.Timeout = 1e9
    _, err := mc.RoutingSummary(context.Background())
    if err == nil {
        t.Fatal("expected connection error")
    }
}

func TestMasterClient_HealthCheck(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(MasterNodesResponse{Total: 0, Online: 0})
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

func TestMasterClient_doRequest_BadURL(t *testing.T) {
    mc := NewMasterClient(config.ClusterMasterConfig{
        Address:     "://bad-address",
        SharedToken: "token",
    })
    _, err := mc.doRequest(context.Background(), http.MethodGet, "/test")
    if err == nil {
        t.Fatal("expected error on bad URL")
    }
}

func TestMasterClient_doRequest_NoSharedToken(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Errorf("Authorization should not be set when no shared token")
        }
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(MasterNodesResponse{})
    }))
    defer srv.Close()

    mc := NewMasterClient(config.ClusterMasterConfig{Address: srv.URL})
    _, err := mc.doRequest(context.Background(), http.MethodGet, "/api/nodes")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
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
        _ = json.NewEncoder(w).Encode(resp)
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
        _ = json.NewEncoder(w).Encode(resp)
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

func TestDiscovery_MasterMode_SyncUpdateExistingNode(t *testing.T) {
    callCount := 0
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        callCount++
        if callCount == 1 {
            resp := MasterNodesResponse{
                Total: 1, Online: 1,
                Nodes: []MasterNodeInfo{
                    {NodeID: "w1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "online", MemoryUsed: 0.2, QueueDepth: 1},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        } else {
            resp := MasterNodesResponse{
                Total: 1, Online: 1,
                Nodes: []MasterNodeInfo{
                    {NodeID: "w1", Address: "http://10.0.0.1:8100", GPU: "M2Ultra", MemoryGB: 64, Status: "online", MemoryUsed: 0.5, QueueDepth: 5},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        }
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

    n, _ := d.GetNode("w1")
    if n.GPU != "M2" {
        t.Errorf("expected M2 after first sync, got %s", n.GPU)
    }

    d.syncFromMaster()
    n, _ = d.GetNode("w1")
    if n.GPU != "M2Ultra" {
        t.Errorf("expected M2Ultra after second sync, got %s", n.GPU)
    }
    if n.MemoryGB != 64 {
        t.Errorf("expected 64 GB, got %d", n.MemoryGB)
    }
    metrics := n.RemoteMetrics()
    if metrics.MemoryUsedRatio != 0.5 {
        t.Errorf("expected mem_used 0.5, got %f", metrics.MemoryUsedRatio)
    }
    if metrics.QueueDepth != 5 {
        t.Errorf("expected queue_depth 5, got %d", metrics.QueueDepth)
    }
}

func TestDiscovery_MasterMode_SyncRemoveNode(t *testing.T) {
    callCount := 0
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        callCount++
        if callCount == 1 {
            resp := MasterNodesResponse{
                Total: 2, Online: 2,
                Nodes: []MasterNodeInfo{
                    {NodeID: "w1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "online"},
                    {NodeID: "w2", Address: "http://10.0.0.2:8100", GPU: "M2", MemoryGB: 32, Status: "online"},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        } else {
            resp := MasterNodesResponse{
                Total: 1, Online: 1,
                Nodes: []MasterNodeInfo{
                    {NodeID: "w1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "online"},
                },
            }
            _ = json.NewEncoder(w).Encode(resp)
        }
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
    if len(d.AllNodes()) != 2 {
        t.Fatalf("expected 2 nodes, got %d", len(d.AllNodes()))
    }

    d.syncFromMaster()
    if len(d.AllNodes()) != 1 {
        t.Fatalf("expected 1 node after removal, got %d", len(d.AllNodes()))
    }
    _, ok := d.GetNode("w2")
    if ok {
        t.Error("w2 should be removed")
    }
}

func TestDiscovery_MasterMode_SyncOfflineNode(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        resp := MasterNodesResponse{
            Total: 1, Online: 0,
            Nodes: []MasterNodeInfo{
                {NodeID: "w1", Address: "http://10.0.0.1:8100", GPU: "M2", MemoryGB: 32, Status: "offline"},
            },
        }
        _ = json.NewEncoder(w).Encode(resp)
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

    n, _ := d.GetNode("w1")
    if n.State() != NodeStateUnhealthy {
        t.Errorf("expected unhealthy for offline node, got %s", n.State())
    }
}

func TestDiscovery_MasterMode_SyncNoClient(t *testing.T) {
    cfg := config.ClusterConfig{
        Enabled:      true,
        Mode:         config.ClusterModeMaster,
        LoadBalancer: "least-connections",
    }
    d := NewDiscovery(cfg)
    d.syncFromMaster()
    if len(d.AllNodes()) != 0 {
        t.Errorf("expected 0 nodes when no master client")
    }
}

func TestDiscovery_MasterMode_SyncFailedRequest(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
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
    if len(d.AllNodes()) != 0 {
        t.Errorf("expected 0 nodes on failed sync, got %d", len(d.AllNodes()))
    }
}
