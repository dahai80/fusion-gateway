package cluster

// MasterClient — HTTP client for fusion-multi-node Master API
// Called from: internal/cluster/discovery.go (mode=master node discovery)
// API surface: /api/nodes (list), /api/tasks/submit (submit), /api/nodes/{id} (get)
// User instruction: "#25" — Task #25 integrate fusion-multi-node Master

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

// MasterAPI is the fusion-multi-node master API surface consumed by the
// discovery loop. The concrete *MasterClient implements it; the #159-C
// masterPool also implements it (delegating to a healthy pooled master with
// least-conn selection + failover). Discovery holds a MasterAPI so it is
// agnostic to single-master vs dual-master active-active — the pool is a
// drop-in wrapper. GetNode/SubmitTask are included for completeness (task
// dispatch path) even though discovery only calls ListNodes/RoutingSummary.
type MasterAPI interface {
    ListNodes(ctx context.Context) (*MasterNodesResponse, error)
    GetNode(ctx context.Context, nodeID string) (*MasterNodeInfo, error)
    SubmitTask(ctx context.Context, req *TaskSubmitRequest) (*TaskSubmitResponse, error)
    RoutingSummary(ctx context.Context) (*MasterRoutingSummary, error)
    HealthCheck(ctx context.Context) error
}

type MasterClient struct {
    address     string
    sharedToken string
    client      *http.Client
}

func NewMasterClient(cfg config.ClusterMasterConfig) *MasterClient {
    // H4 (audit P1): route the master sync/poll client through
    // TransportForBackend so it inherits the MaxConnsPerHost FD cap. A bare
    // &http.Client{} inherits http.DefaultTransport (MaxConnsPerHost=0 =
    // unlimited); the master is a single host so the cap bounds burst conns.
    return &MasterClient{
        address:     cfg.Address,
        sharedToken: cfg.SharedToken,
        client: &http.Client{
            Timeout:   10 * time.Second,
            Transport: httpx.TransportForBackend(config.BackendConfig{BaseURL: cfg.Address}),
        },
    }
}

func (c *MasterClient) doRequest(ctx context.Context, method, path string) (*http.Response, error) {
    url := c.address + path
    req, err := http.NewRequestWithContext(ctx, method, url, nil)
    if err != nil {
        return nil, fmt.Errorf("create request: %w", err)
    }
    if c.sharedToken != "" {
        req.Header.Set("Authorization", "Bearer "+c.sharedToken)
    }
    return c.client.Do(req)
}

type MasterNodeInfo struct {
    NodeID        string  `json:"node_id"`
    Address       string  `json:"address"`
    GPU           string  `json:"gpu"`
    MemoryGB      int     `json:"memory_gb"`
    Status        string  `json:"status"`
    MemoryUsed    float64 `json:"memory_used_ratio"`
    QueueDepth    int     `json:"queue_depth"`
    LastHeartbeat float64 `json:"last_heartbeat"`
}

type MasterNodesResponse struct {
    Total  int              `json:"total"`
    Online int              `json:"online"`
    Nodes  []MasterNodeInfo `json:"nodes"`
}

func (c *MasterClient) ListNodes(ctx context.Context) (*MasterNodesResponse, error) {
    resp, err := c.doRequest(ctx, http.MethodGet, "/api/nodes")
    if err != nil {
        return nil, fmt.Errorf("list nodes from master: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        body := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("master list nodes status %d: %s", resp.StatusCode, string(body))
    }

    var result MasterNodesResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&result); err != nil {
        return nil, fmt.Errorf("decode master nodes response: %w", err)
    }

    slog.Debug("master nodes listed",
        "total", result.Total,
        "online", result.Online,
    )
    return &result, nil
}

func (c *MasterClient) GetNode(ctx context.Context, nodeID string) (*MasterNodeInfo, error) {
    resp, err := c.doRequest(ctx, http.MethodGet, "/api/nodes/"+nodeID)
    if err != nil {
        return nil, fmt.Errorf("get node %s from master: %w", nodeID, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        body := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("master get node %s status %d: %s", nodeID, resp.StatusCode, string(body))
    }

    var node MasterNodeInfo
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&node); err != nil {
        return nil, fmt.Errorf("decode master node response: %w", err)
    }
    return &node, nil
}

type TaskSubmitRequest struct {
    Name               string `json:"name"`
    Mode               string `json:"mode"`
    ModelName          string `json:"model_name"`
    TimeoutSeconds     int    `json:"timeout_seconds"`
    User               string `json:"user"`
    RequiredCapability string `json:"required_capability"`
    PreferredNodeID    string `json:"preferred_node_id"`
    Priority           int    `json:"priority"`
}

type TaskSubmitResponse struct {
    TaskID    string  `json:"task_id"`
    Name      string  `json:"name"`
    Status    string  `json:"status"`
    NodeID    string  `json:"assigned_node_id"`
    CreatedAt float64 `json:"created_at"`
}

func (c *MasterClient) SubmitTask(ctx context.Context, req *TaskSubmitRequest) (*TaskSubmitResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal task request: %w", err)
    }

    url := c.address + "/api/tasks/submit"
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create task submit request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if c.sharedToken != "" {
        httpReq.Header.Set("Authorization", "Bearer "+c.sharedToken)
    }

    resp, err := c.client.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("submit task to master: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        respBody := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("master submit task status %d: %s", resp.StatusCode, string(respBody))
    }

    var result TaskSubmitResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&result); err != nil {
        return nil, fmt.Errorf("decode task submit response: %w", err)
    }

    slog.Info("task submitted to master",
        "task_id", result.TaskID,
        "assigned_node", result.NodeID,
    )
    return &result, nil
}

type MasterRoutingSummary struct {
    Strategy   string  `json:"strategy"`
    TotalNodes int     `json:"total_nodes"`
    AvgLoad    float64 `json:"avg_load"`
    TotalTasks int     `json:"total_tasks"`
}

func (c *MasterClient) RoutingSummary(ctx context.Context) (*MasterRoutingSummary, error) {
    resp, err := c.doRequest(ctx, http.MethodGet, "/api/routing/summary")
    if err != nil {
        return nil, fmt.Errorf("routing summary from master: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        body := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("master routing summary status %d: %s", resp.StatusCode, string(body))
    }

    var result MasterRoutingSummary
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&result); err != nil {
        return nil, fmt.Errorf("decode routing summary: %w", err)
    }
    return &result, nil
}

func (c *MasterClient) HealthCheck(ctx context.Context) error {
    resp, err := c.doRequest(ctx, http.MethodGet, "/api/nodes")
    if err != nil {
        return fmt.Errorf("master health check: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("master health check status %d", resp.StatusCode)
    }
    return nil
}
