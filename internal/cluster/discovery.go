package cluster

// Cluster node discovery — health check, registration, failure eviction
// Called from: cmd/gateway/main.go (start/stop), internal/server/server.go (status), internal/router/engine.go (routing)
// Data schema: Node{ID,Address,GPU,MemoryGB, state,failures,inFlight} + Discovery{nodes,cfg,client}
// User instruction: "#23" — implement Task #23 cluster node config & registration discovery

import (
    "context"
    "encoding/json"
	"io"
    "fmt"
    "log/slog"
    "net/http"
    "sort"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

type NodeState string

const (
    NodeStateHealthy   NodeState = "healthy"
    NodeStateUnhealthy NodeState = "unhealthy"
    NodeStateDead      NodeState = "dead"
)

type Node struct {
    ID       string
    Address  string
    GPU      string
    MemoryGB int
    // Platform is the D4 runtime platform tag (issue #23/#25):
    // "mac" / "windows-cuda" / "" (legacy untyped). Set from
    // ClusterNodeConfig.Platform at registration.
    Platform string

    mu            sync.RWMutex
    state         NodeState
    failures      int
    lastCheck     time.Time
    lastHealth    time.Time
    inFlight      atomic.Int64
    remoteMetrics NodeRemoteMetrics
}

type NodeRemoteMetrics struct {
    MemoryUsedRatio float64
    QueueDepth      int
    GPUUtilization  float64
    CollectedAt     time.Time
}

func (n *Node) State() NodeState {
    n.mu.RLock()
    defer n.mu.RUnlock()
    return n.state
}

func (n *Node) InFlight() int64 {
    return n.inFlight.Load()
}

func (n *Node) IncrInFlight() {
    n.inFlight.Add(1)
}

func (n *Node) DecrInFlight() {
    n.inFlight.Add(-1)
}

func (n *Node) LastHealth() time.Time {
    n.mu.RLock()
    defer n.mu.RUnlock()
    return n.lastHealth
}

func (n *Node) RemoteMetrics() NodeRemoteMetrics {
    n.mu.RLock()
    defer n.mu.RUnlock()
    return n.remoteMetrics
}

func (n *Node) markHealthy() {
    n.mu.Lock()
    defer n.mu.Unlock()
    n.state = NodeStateHealthy
    n.failures = 0
    n.lastHealth = time.Now()
    n.lastCheck = time.Now()
}

func (n *Node) markUnhealthy() {
    n.mu.Lock()
    defer n.mu.Unlock()
    n.failures++
    n.state = NodeStateUnhealthy
    n.lastCheck = time.Now()
    slog.Warn("cluster node unhealthy", "node_id", n.ID, "failures", n.failures)
}

func (n *Node) markDead() {
    n.mu.Lock()
    defer n.mu.Unlock()
    n.state = NodeStateDead
    n.lastCheck = time.Now()
    slog.Warn("cluster node marked dead", "node_id", n.ID)
}

type Discovery struct {
    mu            sync.RWMutex
    nodes         map[string]*Node
    cfg           config.ClusterConfig
    client        *http.Client
    stopCh        chan struct{}
    running       atomic.Bool
    rrIndex       atomic.Uint64
    masterClient  *MasterClient
}

func NewDiscovery(cfg config.ClusterConfig) *Discovery {
    d := &Discovery{
        nodes:  make(map[string]*Node),
        cfg:    cfg,
        client: &http.Client{Timeout: 5 * time.Second},
        stopCh: make(chan struct{}),
    }

    if cfg.Mode == config.ClusterModeMaster && cfg.Master.Address != "" {
        d.masterClient = NewMasterClient(cfg.Master)
        slog.Info("cluster discovery using master mode", "master_address", cfg.Master.Address)
    }

    return d
}

func (d *Discovery) Start(ctx context.Context) {
    if !d.cfg.Enabled {
        slog.Info("cluster discovery disabled")
        return
    }

    if !d.running.CompareAndSwap(false, true) {
        slog.Warn("cluster discovery already running")
        return
    }

    mode := d.cfg.Mode
    if mode == "" {
        mode = config.ClusterModeStandalone
    }

    slog.Info("cluster discovery starting",
        "mode", mode,
        "check_interval", d.cfg.HealthCheckInterval,
        "load_balancer", d.cfg.LoadBalancer,
    )

    if mode == config.ClusterModeMaster {
        d.syncFromMaster()
        safego.Go("cluster_master_sync", func() { d.masterSyncLoop(ctx) })
    } else {
        d.loadNodesFromConfig()
        safego.Go("cluster_health_check", func() { d.healthCheckLoop(ctx) })
    }
}

func (d *Discovery) Stop() {
    if d.running.CompareAndSwap(true, false) {
        close(d.stopCh)
        slog.Info("cluster discovery stopped")
    }
}

func (d *Discovery) loadNodesFromConfig() {
    d.mu.Lock()
    defer d.mu.Unlock()

    for _, nodeCfg := range d.cfg.Nodes {
        if _, exists := d.nodes[nodeCfg.ID]; exists {
            continue
        }
        d.nodes[nodeCfg.ID] = &Node{
            ID:       nodeCfg.ID,
            Address:  nodeCfg.Address,
            GPU:      nodeCfg.GPU,
            MemoryGB: nodeCfg.MemoryGB,
            Platform: nodeCfg.Platform,
            state:    NodeStateUnhealthy,
        }
        slog.Info("cluster node registered from config",
            "node_id", nodeCfg.ID,
            "address", nodeCfg.Address,
            "gpu", nodeCfg.GPU,
            "memory_gb", nodeCfg.MemoryGB,
            "platform", nodeCfg.Platform,
        )
    }
}

func (d *Discovery) UpdateConfig(cfg config.ClusterConfig) {
    d.mu.Lock()
    d.cfg = cfg

    existingIDs := make(map[string]bool)
    for _, nodeCfg := range cfg.Nodes {
        existingIDs[nodeCfg.ID] = true
        if node, exists := d.nodes[nodeCfg.ID]; exists {
            node.mu.Lock()
            node.Address = nodeCfg.Address
            node.GPU = nodeCfg.GPU
            node.MemoryGB = nodeCfg.MemoryGB
            node.Platform = nodeCfg.Platform
            node.mu.Unlock()
            slog.Info("cluster node updated", "node_id", nodeCfg.ID)
        } else {
            d.nodes[nodeCfg.ID] = &Node{
                ID:       nodeCfg.ID,
                Address:  nodeCfg.Address,
                GPU:      nodeCfg.GPU,
                MemoryGB: nodeCfg.MemoryGB,
                Platform: nodeCfg.Platform,
                state:    NodeStateUnhealthy,
            }
            slog.Info("cluster node added on config update", "node_id", nodeCfg.ID)
        }
    }

    for id := range d.nodes {
        if !existingIDs[id] {
            delete(d.nodes, id)
            slog.Info("cluster node removed on config update", "node_id", id)
        }
    }
    d.mu.Unlock()
}

func (d *Discovery) healthCheckLoop(ctx context.Context) {
    interval := d.cfg.HealthCheckInterval
    if interval == 0 {
        interval = 10 * time.Second
    }

    d.checkAll()

    ticker := time.NewTicker(interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-d.stopCh:
            return
        case <-ticker.C:
            d.checkAll()
        }
    }
}

func (d *Discovery) syncFromMaster() {
    if d.masterClient == nil {
        slog.Warn("master client not configured, skipping sync")
        return
    }

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    resp, err := d.masterClient.ListNodes(ctx)
    if err != nil {
        slog.Error("sync from master failed", "error", err)
        return
    }

    d.mu.Lock()
    defer d.mu.Unlock()

    existingIDs := make(map[string]bool)

    for _, mn := range resp.Nodes {
        existingIDs[mn.NodeID] = true

        state := NodeStateUnhealthy
        if mn.Status == "online" {
            state = NodeStateHealthy
        }

        if node, ok := d.nodes[mn.NodeID]; ok {
            node.mu.Lock()
            node.Address = mn.Address
            node.GPU = mn.GPU
            node.MemoryGB = mn.MemoryGB
            node.state = state
            node.remoteMetrics = NodeRemoteMetrics{
                MemoryUsedRatio: mn.MemoryUsed,
                QueueDepth:      mn.QueueDepth,
                CollectedAt:     time.Now(),
            }
            node.mu.Unlock()
        } else {
            d.nodes[mn.NodeID] = &Node{
                ID:       mn.NodeID,
                Address:  mn.Address,
                GPU:      mn.GPU,
                MemoryGB: mn.MemoryGB,
                remoteMetrics: NodeRemoteMetrics{
                    MemoryUsedRatio: mn.MemoryUsed,
                    QueueDepth:      mn.QueueDepth,
                    CollectedAt:     time.Now(),
                },
            }
            n := d.nodes[mn.NodeID]
            n.mu.Lock()
            n.state = state
            n.mu.Unlock()
        }
    }

    for id := range d.nodes {
        if !existingIDs[id] {
            delete(d.nodes, id)
            slog.Info("cluster node removed (not in master)", "node_id", id)
        }
    }

    slog.Info("synced nodes from master",
        "total", resp.Total,
        "online", resp.Online,
        "local_count", len(d.nodes),
    )
}

func (d *Discovery) masterSyncLoop(ctx context.Context) {
    interval := d.cfg.HealthCheckInterval
    if interval == 0 {
        interval = 10 * time.Second
    }

    d.syncFromMaster()

    ticker := time.NewTicker(interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-d.stopCh:
            return
        case <-ticker.C:
            d.syncFromMaster()
        }
    }
}

func (d *Discovery) checkAll() {
    d.mu.RLock()
    nodes := make([]*Node, 0, len(d.nodes))
    for _, n := range d.nodes {
        nodes = append(nodes, n)
    }
    d.mu.RUnlock()

    for _, node := range nodes {
        d.checkNode(node)
    }
}

func (d *Discovery) checkNode(node *Node) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.Address+"/health", nil)
    if err != nil {
        slog.Error("cluster health check: create request failed", "node_id", node.ID, "error", err)
        node.markUnhealthy()
        d.checkFailureThreshold(node)
        return
    }

    resp, err := d.client.Do(req)
    if err != nil {
        slog.Warn("cluster health check failed", "node_id", node.ID, "address", node.Address, "error", err)
        node.markUnhealthy()
        d.checkFailureThreshold(node)
        return
    }
    // R2 fix: drain body to allow connection reuse
    _, _ = io.Copy(io.Discard, resp.Body)
    resp.Body.Close()

    if resp.StatusCode == http.StatusOK {
        node.markHealthy()
        d.fetchRemoteMetrics(node)
    } else {
        slog.Warn("cluster node returned non-200", "node_id", node.ID, "status", resp.StatusCode)
        node.markUnhealthy()
        d.checkFailureThreshold(node)
    }
}

func (d *Discovery) fetchRemoteMetrics(node *Node) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.Address+"/v1/status", nil)
    if err != nil {
        slog.Debug("cluster metrics: create request failed", "node_id", node.ID, "error", err)
        return
    }

    resp, err := d.client.Do(req)
    if err != nil {
        slog.Debug("cluster metrics fetch failed", "node_id", node.ID, "error", err)
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // R2 fix: drain body to allow connection reuse
        _, _ = io.Copy(io.Discard, resp.Body)
        slog.Debug("cluster metrics: non-200 status", "node_id", node.ID, "status", resp.StatusCode)
        return
    }

    var statusResp struct {
        Hardware struct {
            MemoryUsedRatio float64 `json:"memory_used_ratio"`
        } `json:"hardware"`
        Backends struct {
            FusionMLX struct {
                QueueDepth int `json:"queue_depth"`
            } `json:"fusion_mlx"`
        } `json:"backends"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
        // R2 fix: drain remaining body to allow connection reuse
        _, _ = io.Copy(io.Discard, resp.Body)
        slog.Debug("cluster metrics decode failed", "node_id", node.ID, "error", err)
        return
    }

    node.mu.Lock()
    node.remoteMetrics = NodeRemoteMetrics{
        MemoryUsedRatio: statusResp.Hardware.MemoryUsedRatio,
        QueueDepth:      statusResp.Backends.FusionMLX.QueueDepth,
        CollectedAt:     time.Now(),
    }
    node.mu.Unlock()

    slog.Debug("cluster metrics updated",
        "node_id", node.ID,
        "mem_ratio", statusResp.Hardware.MemoryUsedRatio,
        "queue_depth", statusResp.Backends.FusionMLX.QueueDepth,
    )
}

func (d *Discovery) checkFailureThreshold(node *Node) {
    threshold := d.cfg.FailureThreshold
    if threshold == 0 {
        threshold = 3
    }

    node.mu.RLock()
    failures := node.failures
    node.mu.RUnlock()

    if failures >= threshold {
        node.markDead()
    }
}

func (d *Discovery) HealthyNodeList() []*Node {
    d.mu.RLock()
    defer d.mu.RUnlock()

    var result []*Node
    for _, n := range d.nodes {
        if n.State() == NodeStateHealthy {
            result = append(result, n)
        }
    }
    return result
}

// HealthyNodesByPlatform returns the count of healthy nodes whose Platform
// matches the given tag. Empty platform matches all healthy nodes (legacy
// behavior). D4 dispatch-by-platform (issue #23/#25).
func (d *Discovery) HealthyNodesByPlatform(platform string) int {
    if platform == "" {
        return d.HealthyNodes()
    }
    d.mu.RLock()
    defer d.mu.RUnlock()
    count := 0
    for _, n := range d.nodes {
        if n.State() == NodeStateHealthy && n.Platform == platform {
            count++
        }
    }
    return count
}

// SelectNodeByPlatform selects a healthy node on the given platform using the
// configured strategy. Empty platform falls back to platform-agnostic
// SelectNode. D4 dispatch-by-platform (issue #23/#25).
func (d *Discovery) SelectNodeByPlatform(strategy, platform string) (*Node, error) {
    if platform == "" {
        return d.SelectNode(strategy)
    }
    d.mu.RLock()
    var healthy []*Node
    for _, n := range d.nodes {
        if n.State() == NodeStateHealthy && n.Platform == platform {
            healthy = append(healthy, n)
        }
    }
    d.mu.RUnlock()

    if len(healthy) == 0 {
        return nil, fmt.Errorf("no healthy cluster nodes on platform %q", platform)
    }

    switch strategy {
    case "least-connections":
        return d.selectLeastConnections(healthy), nil
    case "hardware-aware":
        return d.selectHardwareAware(healthy), nil
    case "round-robin":
        return d.selectRoundRobin(healthy), nil
    default:
        return d.selectLeastConnections(healthy), nil
    }
}

func (d *Discovery) AllNodes() []*Node {
    d.mu.RLock()
    defer d.mu.RUnlock()

    result := make([]*Node, 0, len(d.nodes))
    for _, n := range d.nodes {
        result = append(result, n)
    }
    return result
}

func (d *Discovery) GetNode(id string) (*Node, bool) {
    d.mu.RLock()
    defer d.mu.RUnlock()
    n, ok := d.nodes[id]
    return n, ok
}

func (d *Discovery) Status() []NodeStatus {
    d.mu.RLock()
    defer d.mu.RUnlock()

    result := make([]NodeStatus, 0, len(d.nodes))
    for _, n := range d.nodes {
        n.mu.RLock()
        result = append(result, NodeStatus{
            ID:           n.ID,
            Address:      n.Address,
            GPU:          n.GPU,
            MemoryGB:     n.MemoryGB,
            State:        string(n.state),
            Failures:     n.failures,
            InFlight:     n.InFlight(),
            LastHealth:   n.lastHealth.Format(time.RFC3339),
            LastCheck:    n.lastCheck.Format(time.RFC3339),
            MemUsedRatio: n.remoteMetrics.MemoryUsedRatio,
            QueueDepth:   n.remoteMetrics.QueueDepth,
        })
        n.mu.RUnlock()
    }
    return result
}

type NodeStatus struct {
    ID         string  `json:"id"`
    Address    string  `json:"address"`
    GPU        string  `json:"gpu"`
    MemoryGB   int     `json:"memory_gb"`
    State      string  `json:"state"`
    Failures   int     `json:"failures"`
    InFlight   int64   `json:"in_flight"`
    LastHealth string  `json:"last_health"`
    LastCheck  string  `json:"last_check"`
    MemUsedRatio float64 `json:"mem_used_ratio"`
    QueueDepth   int     `json:"queue_depth"`
}

func (d *Discovery) SelectNode(strategy string) (*Node, error) {
    healthy := d.HealthyNodeList()
    if len(healthy) == 0 {
        return nil, fmt.Errorf("no healthy cluster nodes available")
    }

    switch strategy {
    case "least-connections":
        return d.selectLeastConnections(healthy), nil
    case "hardware-aware":
        return d.selectHardwareAware(healthy), nil
    case "round-robin":
        return d.selectRoundRobin(healthy), nil
    default:
        return d.selectLeastConnections(healthy), nil
    }
}

// HealthyNodeCount implements router.ClusterSelector
func (d *Discovery) HealthyNodes() int {
    d.mu.RLock()
    defer d.mu.RUnlock()

    count := 0
    for _, n := range d.nodes {
        if n.State() == NodeStateHealthy {
            count++
        }
    }
    return count
}

// SelectNodeID implements router.ClusterSelector — returns node ID string
func (d *Discovery) SelectNodeID(strategy string) (string, error) {
    node, err := d.SelectNode(strategy)
    if err != nil {
        return "", err
    }
    return node.ID, nil
}

// ClusterSelectorAdapter wraps Discovery to implement router.ClusterSelector
type ClusterSelectorAdapter struct {
    discovery *Discovery
}

func NewClusterSelectorAdapter(d *Discovery) *ClusterSelectorAdapter {
    return &ClusterSelectorAdapter{discovery: d}
}

func (a *ClusterSelectorAdapter) HealthyNodes() int {
    return a.discovery.HealthyNodes()
}

func (a *ClusterSelectorAdapter) SelectNode(strategy string) (string, error) {
    return a.discovery.SelectNodeID(strategy)
}

func (a *ClusterSelectorAdapter) HealthyNodesByPlatform(platform string) int {
    return a.discovery.HealthyNodesByPlatform(platform)
}

func (a *ClusterSelectorAdapter) SelectNodeByPlatform(strategy, platform string) (string, error) {
    node, err := a.discovery.SelectNodeByPlatform(strategy, platform)
    if err != nil {
        return "", err
    }
    return node.ID, nil
}

func (d *Discovery) selectRoundRobin(nodes []*Node) *Node {
    sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
    idx := d.rrIndex.Add(1) - 1
    return nodes[int(idx)%len(nodes)]
}

func (d *Discovery) selectLeastConnections(nodes []*Node) *Node {
    var best *Node
    var minInFlight int64 = -1

    for _, n := range nodes {
        inf := n.InFlight()
        if minInFlight < 0 || inf < minInFlight {
            minInFlight = inf
            best = n
        }
    }
    return best
}

func (d *Discovery) selectHardwareAware(nodes []*Node) *Node {
    var best *Node
    bestScore := -1.0

    for _, n := range nodes {
        score := d.nodeScore(n)
        if score > bestScore {
            bestScore = score
            best = n
        }
    }
    return best
}

func (d *Discovery) nodeScore(n *Node) float64 {
    metrics := n.RemoteMetrics()

    memScore := float64(n.MemoryGB)

    if metrics.CollectedAt.IsZero() {
        // No remote metrics yet — fall back to static scoring
        inf := n.InFlight()
        if inf > 0 {
            memScore /= float64(inf + 1)
        }
        return memScore
    }

    // Memory headroom: (1 - used_ratio) * totalGB
    memAvail := (1.0 - metrics.MemoryUsedRatio) * float64(n.MemoryGB)
    if memAvail < 0 {
        memAvail = 0
    }

    // Queue depth penalty: fewer queued = higher score
    queueFactor := 1.0
    if metrics.QueueDepth > 0 {
        queueFactor = 1.0 / float64(metrics.QueueDepth+1)
    }

    // In-flight penalty
    inf := n.InFlight()
    inFlightFactor := 1.0
    if inf > 0 {
        inFlightFactor = 1.0 / float64(inf+1)
    }

    // Weighted: 60% memory availability + 30% queue factor + 10% in-flight
    score := memAvail*0.6 + float64(n.MemoryGB)*queueFactor*0.3 + float64(n.MemoryGB)*inFlightFactor*0.1

    slog.Debug("cluster node score",
        "node_id", n.ID,
        "mem_avail_gb", memAvail,
        "queue_depth", metrics.QueueDepth,
        "in_flight", inf,
        "score", score,
    )

    return score
}
