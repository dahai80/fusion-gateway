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
    "os"
    "runtime/debug"
    "sort"
    "strings"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/jitter"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
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
    // models is the per-node model registry polled from GET /v1/models
    // (#95). Empty when the node hasn't reported or doesn't implement
    // /v1/models — such a node is never selected by model (cloud fallback).
    models []string
    // modelsReady is set true only after the first successful /v1/models
    // poll (RR13). Until then servesModel returns false for any specific
    // model — a node marked healthy but never polled must not be routed a
    // model-specific request off a stale/empty registry. Distinguishes
    // "never polled" (not ready) from "polled, serves nothing" (ready,
    // legitimately empty), which a bare empty models slice cannot.
    modelsReady bool
    // breakerBypassed is the R8 coordination flag: set by the router when a
    // node's per-node circuit breaker opens (RecordNodeFailure → open), cleared
    // when it recovers (RecordNodeSuccess → closed). A bypassed node is healthy
    // (the /health probe still succeeds — it tripped on real request failures,
    // not on being down) but NOT selectable until the breaker cooldown elapses.
    // This makes Discovery the single source of truth for routability: the
    // prior design selected a healthy node then bypassed it post-hoc, so
    // Discovery.Status() reported it routable while the router refused — the
    // two systems drifted. Decoupled from failures/state (the probe-driven
    // checkFailureThreshold → markDead lifecycle) so a breaker trip does NOT
    // inflate the probe failure counter or force markDead — double-counting
    // would poison the dead/recovery lifecycle with request-failure signals.
    breakerBypassed atomic.Bool
}

// Models returns a snapshot of the node's served-model list (#95).
// Copy-under-lock so callers may iterate without mutating the live slice.
func (n *Node) Models() []string {
    n.mu.RLock()
    defer n.mu.RUnlock()
    out := make([]string, len(n.models))
    copy(out, n.models)
    return out
}

// servesModel reports whether model is in the node's registry (#95, RR13).
// model=="" is model-agnostic and matches any node. For a specific model the
// node must have completed at least one /v1/models poll (modelsReady) — a
// healthy-but-unpolled node is NOT a match, so SelectNodeByModel falls back to
// cloud instead of routing off a stale/empty registry (RR13 stale window).
func (n *Node) servesModel(model string) bool {
    if model == "" {
        return true
    }
    n.mu.RLock()
    ready := n.modelsReady
    models := n.models
    n.mu.RUnlock()
    if !ready {
        return false
    }
    for _, m := range models {
        if m == model {
            return true
        }
    }
    return false
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

// selectable reports whether a node is routable: healthy per the /health probe
// AND not breaker-bypassed (R8). This is the single routability gate the
// SelectNode* functions apply, so Discovery is the single source of truth — a
// breaker-open node never reaches a strategy selector, eliminating the
// post-hoc bypass that caused Discovery.Status() and the router to disagree.
func (n *Node) selectable() bool {
    return n.State() == NodeStateHealthy && !n.BreakerBypassed()
}

func (n *Node) InFlight() int64 {
    return n.inFlight.Load()
}

func (n *Node) IncrInFlight() {
    n.inFlight.Add(1)
    observability.UpdateInFlight("cluster-"+n.ID, n.inFlight.Load())
}

func (n *Node) DecrInFlight() {
    n.inFlight.Add(-1)
    observability.UpdateInFlight("cluster-"+n.ID, n.inFlight.Load())
}

// BreakerBypassed reports whether the router's per-node circuit breaker has
// tripped this node open (R8). A bypassed node is healthy but not selectable.
func (n *Node) BreakerBypassed() bool {
    return n.breakerBypassed.Load()
}

// SetBreakerBypassed sets the R8 coordination flag. Called by the router on
// breaker open (true) / recovered-closed (false) transitions. Logs the
// transition so a bypassed node is traceable in /metrics + logs, not silent.
func (n *Node) SetBreakerBypassed(b bool) {
    prev := n.breakerBypassed.Swap(b)
    if prev != b {
        if b {
            slog.Warn("cluster node breaker-bypassed (not selectable while cooldown elapses)",
                "node_id", n.ID)
        } else {
            slog.Info("cluster node breaker-recovered (selectable again)", "node_id", n.ID)
        }
    }
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
    // #159-C: masterClient is a MasterAPI (single *MasterClient or a
    // dual-master active-active *masterPool). nil when not in master mode.
    masterClient  MasterAPI
    // EI10: wg tracks the long-lived healthCheck/masterSync goroutine so Stop
    // joins (waits for exit) instead of just closing stopCh and racing a
    // mid-iteration stop that writes node state after Shutdown returned.
    wg            sync.WaitGroup
    // #119: masterStrategy caches the routing strategy the fusion-multi-node
    // master owns (master.RoutingSummary.Strategy). Fetched every master-sync
    // tick alongside node membership. When non-empty and master mode honors
    // strategy (IgnoreMasterStrategy=false), SelectNode uses this instead of
    // the caller's local Cluster.LoadBalancer — so the strategy a user
    // configured in fusion-studio is authoritative for inference, not divergent
    // from it.
    // R3 (audit P2): holds a masterStrategyEntry{strategy, cachedAt}, NOT a
    // bare string. A fetch failure leaves the cache untouched — before R3 that
    // meant a strategy cached at T0 was trusted forever across an indefinite
    // master outage (route to dead nodes, sustained outage, no operator
    // signal). cachedAt lets resolveStrategy bound trust: older than
    // cfg.Master.MaxStaleAge → fall back to local load_balancer + Warn.
    masterStrategy atomic.Value
    // #163: admission view fields wired by main.go via SetAdmissionView so
    // Status() can expose the gateway-owned per-node concurrency budget and
    // the router's aggregated per-node breaker state to clients. DI (not a
    // direct router.Engine ref) avoids a cluster→router import cycle and
    // mirrors the callback-DI pattern used for localInFlight/localModels on
    // the engine. Both zero-value to nil-safe: Status() reports uncapped /
    // "closed" when unwired (standalone, cluster disabled).
    maxConcurrent int
    breakerStateFn func(nodeID string) string
}

// masterStrategyEntry is the timestamped cached master strategy (R3). The
// zero value (strategy=="") means "nothing cached yet". cachedAt is the wall
// time of the successful sync that stored it, used by resolveStrategy's
// staleness check.
type masterStrategyEntry struct {
    strategy string
    cachedAt time.Time
}

// envClusterBackend reads FUSION_CLUSTER_BACKEND. When "multi-node", it forces
// master mode (the master becomes the single source of node membership +
// strategy) regardless of cluster.mode in config — a 12-factor switch that
// promotes a standalone-config gateway into master mode without a config edit.
// Other values (incl. "" unset) leave cluster.mode to govern.
func envClusterBackend() string {
    return strings.TrimSpace(os.Getenv("FUSION_CLUSTER_BACKEND"))
}

func NewDiscovery(cfg config.ClusterConfig) *Discovery {
    // #119: FUSION_CLUSTER_BACKEND=multi-node forces master mode (single source
    // of truth). Requires Master.Address; without it the env gate is a no-op
    // (logged) so a misconfigured env var does not silently empty the node set.
    if envClusterBackend() == "multi-node" {
        if cfg.Master.Address != "" {
            if cfg.Mode != config.ClusterModeMaster {
                slog.Info("FUSION_CLUSTER_BACKEND=multi-node forcing cluster mode master",
                    "prior_mode", cfg.Mode, "master_address", cfg.Master.Address)
            }
            cfg.Mode = config.ClusterModeMaster
        } else {
            slog.Warn("FUSION_CLUSTER_BACKEND=multi-node set but cluster.master.address empty — staying in configured mode (set cluster.master.address to enable master mode)",
                "configured_mode", cfg.Mode)
        }
    }

    // H4 (audit P1): the discovery polling client fans out to every node's
    // /health and /v1/models on a fixed tick. A bare &http.Client{} inherits
    // http.DefaultTransport (MaxConnsPerHost=0 = unlimited) — with many nodes
    // a single tick burst could open dozens of connections per node. Route
    // through TransportForBackend so the per-host FD cap applies. BaseURL is
    // empty: the cap is per-host keyed on the dialed URL, not the config field.
    d := &Discovery{
        nodes:  make(map[string]*Node),
        cfg:    cfg,
        client: &http.Client{
            Timeout:   5 * time.Second,
            Transport: httpx.TransportForBackend(config.BackendConfig{}),
        },
        stopCh: make(chan struct{}),
    }

    // #159-C: construct the master client as a pool when multiple addresses
    // are configured (dual-master active-active), or a single-client pool when
    // only the singular Address is set (backward-compat). NewMasterPool handles
    // both shapes and returns nil when no address is configured.
    if cfg.Mode == config.ClusterModeMaster && len(resolveMasterAddresses(cfg.Master)) > 0 {
        d.masterClient = NewMasterPool(cfg.Master)
        slog.Info("cluster discovery using master mode (#159-C pool)",
            "masters", len(resolveMasterAddresses(cfg.Master)),
            "honor_master_strategy", !cfg.Master.IgnoreMasterStrategy)
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
        // #119: cache the master-owned strategy before the first selection so a
        // request arriving before the first sync tick still honors the master.
        d.syncMasterStrategy()
        // AH3: populate per-node model registries so master-mode deployments
        // don't silently degrade every request to cloud (servesModel empty →
        // SelectNodeByModel finds no node → cloud fallback, no warning).
        d.syncNodeModels()
        d.wg.Add(1)
        // H3: restart on panic so a single panic does not permanently kill the
        // master sync loop (silent cloud-degrade). wg.Done fires only on clean
        // exit or circuit-breaker trip; backoff respects stopCh for fast shutdown.
        d.runRestartable(ctx, "cluster_master_sync", d.masterSyncLoop)
    } else {
        d.loadNodesFromConfig()
        d.wg.Add(1)
        // H3: restart on panic so a single panic does not permanently kill the
        // health-check loop (stale node states, no recovery).
        d.runRestartable(ctx, "cluster_health_check", d.healthCheckLoop)
    }
}

// runRestartable runs a long-lived loop (H3) with panic-restart + exponential
// backoff + a consecutive-panic circuit breaker, so a single panic no longer
// permanently silences the worker (audit H3). wg.Done fires exactly once on a
// terminal state: clean return (ctx/stopCh observed by the loop) or circuit
// breaker trip. Backoff waits respect stopCh so Stop does not stall in backoff.
func (d *Discovery) runRestartable(ctx context.Context, name string, fn func(context.Context)) {
    safego.Go(name, func() {
        defer d.wg.Done()
        const (
            baseBackoff    = 100 * time.Millisecond
            maxBackoff     = 30 * time.Second
            gracePeriod    = 30 * time.Second
            maxConsecutive = 10
        )
        consecutive := 0
        backoff := baseBackoff
        for {
            started := time.Now()
            panicked := true
            func() {
                defer func() {
                    if r := recover(); r != nil {
                        ran := time.Since(started)
                        if ran >= gracePeriod {
                            consecutive = 0
                            backoff = baseBackoff
                        }
                        consecutive++
                        slog.Error("cluster worker panic recovered, restarting",
                            "worker", name,
                            "panic", r,
                            "stack", string(debug.Stack()),
                            "consecutive_panics", consecutive,
                            "ran_before_panic", ran.String(),
                            "next_backoff", backoff.String(),
                        )
                        return
                    }
                    panicked = false
                }()
                fn(ctx)
            }()
            if !panicked {
                return
            }
            if consecutive > maxConsecutive {
                slog.Error("cluster worker circuit breaker tripped, permanently disabled",
                    "worker", name,
                    "consecutive_panics", consecutive,
                    "max_consecutive", maxConsecutive,
                    "grace_period", gracePeriod.String(),
                )
                return
            }
            select {
            case <-time.After(backoff):
            case <-d.stopCh:
                return
            }
            next := backoff * 2
            if next > maxBackoff {
                next = maxBackoff
            }
            backoff = next
        }
    })
}

func (d *Discovery) Stop() {
    if d.running.CompareAndSwap(true, false) {
        close(d.stopCh)
        // EI10: wait for the long-lived loop to observe stopCh/ctx.Done and
        // return before Stop returns — so Shutdown does not race a mid-iterate
        // node-state write.
        d.wg.Wait()
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
    d.mu.RLock()
    interval := d.cfg.HealthCheckInterval
    d.mu.RUnlock()
    if interval == 0 {
        interval = 10 * time.Second
    }

    d.checkAll()

    for {
        // H5 (audit P1): jittered per-tick wait instead of a fixed
        // time.NewTicker. A fixed ticker makes every gateway in a cluster
        // poll on the same interval edge → synchronized 150 req/s spikes
        // against each node's /health+/v1/status+/v1/models at every tick.
        // jitter.After spreads each gateway's tick across ±20% of the
        // interval, smearing the herd into steady load. First iteration
        // already ran checkAll above; this loop only gates the next wake.
        select {
        case <-ctx.Done():
            return
        case <-d.stopCh:
            return
        case <-jitter.After(interval):
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
    d.mu.RLock()
    interval := d.cfg.HealthCheckInterval
    d.mu.RUnlock()
    if interval == 0 {
        interval = 10 * time.Second
    }

    d.syncFromMaster()
    // #119: pull the master-owned routing strategy so SelectNode honors it.
    d.syncMasterStrategy()
    // AH3: keep per-node model registries fresh on every master sync.
    d.syncNodeModels()

    for {
        // H5 (audit P1): jittered per-tick wait, not a fixed ticker — see
        // healthCheckLoop. Master mode fans out to master /api/nodes +
        // routing/summary + every node's /v1/models; 5 gateways on a shared
        // 10s ticker edge spike the single-threaded master admin loop. The
        // ±20% spread keeps the aggregate rate while killing the herd.
        select {
        case <-ctx.Done():
            return
        case <-d.stopCh:
            return
        case <-jitter.After(interval):
            d.syncFromMaster()
            // #119: refresh the master strategy each tick (a user can change
            // it in fusion-studio; the master's RoutingSummary reflects it).
            d.syncMasterStrategy()
            // AH3: refresh model registries after the node list may have changed.
            d.syncNodeModels()
        }
    }
}

// syncMasterStrategy fetches the routing strategy the fusion-multi-node master
// owns and caches it in masterStrategy. #119: this is what makes the user's
// fusion-studio strategy choice authoritative for inference node selection,
// not the gateway's local cluster.load_balancer. Failures are logged at Warn
// (not Error) and leave the cache untouched — SelectNode falls back to the
// caller's local strategy, so a transient master blip does not stall routing.
func (d *Discovery) syncMasterStrategy() {
    if d.masterClient == nil {
        return
    }
    d.mu.RLock()
    ignore := d.cfg.Master.IgnoreMasterStrategy
    d.mu.RUnlock()
    if ignore {
        slog.Debug("cluster master strategy sync skipped (ignore_strategy=true, local load_balancer governs)")
        return
    }

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    summary, err := d.masterClient.RoutingSummary(ctx)
    if err != nil {
        slog.Warn("master strategy sync failed — falling back to local load_balancer for this interval",
            "error", err)
        return
    }
    if summary.Strategy == "" {
        slog.Warn("master routing summary returned empty strategy — keeping prior cached strategy, falling back to local if none cached",
            "total_nodes", summary.TotalNodes)
        return
    }
    // R3: store the strategy WITH its sync timestamp so resolveStrategy can
    // bound how long a cached strategy is trusted across failed syncs.
    d.masterStrategy.Store(masterStrategyEntry{strategy: summary.Strategy, cachedAt: time.Now()})
    slog.Info("cluster master strategy cached", "strategy", summary.Strategy,
        "total_nodes", summary.TotalNodes, "avg_load", summary.AvgLoad)
}

// resolveStrategy returns the effective selection strategy for a SelectNode*
// call. #119: in master mode with a cached master strategy and
// IgnoreMasterStrategy=false, the master's strategy overrides the caller's
// local strategy (single source of truth). Otherwise the caller's strategy
// governs (standalone mode, opt-out, or no strategy cached yet).
func (d *Discovery) resolveStrategy(local string) string {
    if d.masterClient == nil {
        return local
    }
    d.mu.RLock()
    ignore := d.cfg.Master.IgnoreMasterStrategy
    maxStale := d.cfg.Master.MaxStaleAge
    d.mu.RUnlock()
    if ignore {
        return local
    }
    entry, ok := d.masterStrategy.Load().(masterStrategyEntry)
    if !ok || entry.strategy == "" {
        return local
    }
    // R3 (audit P2): bound how long a cached strategy is trusted. A fetch
    // failure leaves the cache untouched, so without a staleness bound a
    // strategy cached at T0 is trusted forever across an indefinite master
    // outage — routing to dead nodes, sustained outage, no operator signal.
    // Older than max_stale_age → fall back to the caller's local strategy and
    // log so the operator sees the strategy is过期. maxStale==0 disables the
    // bound (legacy永久-sticky opt-out).
    if maxStale > 0 && time.Since(entry.cachedAt) > maxStale {
        slog.Warn("cluster master strategy is stale beyond max_stale_age — falling back to local load_balancer",
            "cached_strategy", entry.strategy,
            "age", time.Since(entry.cachedAt).Round(time.Second),
            "max_stale_age", maxStale)
        return local
    }
    return entry.strategy
}

func (d *Discovery) checkAll() {
    d.mu.RLock()
    nodes := make([]*Node, 0, len(d.nodes))
    for _, n := range d.nodes {
        nodes = append(nodes, n)
    }
    d.mu.RUnlock()

    // B7: check nodes concurrently. checkNode is a 5s-timeout HTTP call per
    // node; a sequential loop over N nodes where a few are down takes up to
    // N*5s to complete a single health tick, stalling routing-decision reads
    // of node state and delaying eviction of dead nodes. Each node's state
    // mutations (markHealthy/markUnhealthy/failures) are guarded by the
    // per-node mutex, so parallel checks are safe. Each worker carries panic
    // recovery (checkNode → fetchRemoteMetrics/fetchModels decode upstream
    // JSON; a malformed payload must not abort the whole health round) and
    // still signals the WaitGroup so the barrier is honored on any exit.
    var wg sync.WaitGroup
    for _, node := range nodes {
        wg.Add(1)
        go func(n *Node) {
            defer wg.Done()
            defer func() {
                if r := recover(); r != nil {
                    // RR13: a panic (e.g. malformed upstream JSON in
                    // fetchRemoteMetrics/fetchModels) must not leave the node
                    // stuck in its prior state — if it panicked mid-check it
                    // did not complete a clean health confirmation this round.
                    // Mark unhealthy + run the threshold so a persistently
                    // panicking node trends to dead instead of staying "healthy
                    // but never polled" until the next tick.
                    slog.Error("cluster health check panic recovered",
                        "node_id", n.ID, "panic", r, "stack", string(debug.Stack()))
                    n.markUnhealthy()
                    d.checkFailureThreshold(n)
                }
            }()
            d.checkNode(n)
        }(node)
    }
    wg.Wait()
}

// syncNodeModels populates each healthy node's model registry in master mode
// (AH3, audit P1). Master mode skips healthCheckLoop (the master owns node
// liveness), so fetchModels — which only runs inside checkNode — never fires
// and every node's servesModel stays empty. SelectNodeByModel then finds no
// node serving the requested model and silently degrades every request to
// cloud, with no error or warning. This method closes that gap by polling
// each healthy node's /v1/models after every master sync, concurrent + panic
// recovered like checkAll. Non-fatal: a node whose /v1/models fails keeps an
// empty registry and is skipped by SelectNodeByModel (cloud fallback).
func (d *Discovery) syncNodeModels() {
    d.mu.RLock()
    nodes := make([]*Node, 0, len(d.nodes))
    for _, n := range d.nodes {
        if n.State() == NodeStateHealthy {
            nodes = append(nodes, n)
        }
    }
    d.mu.RUnlock()

    if len(nodes) == 0 {
        slog.Warn("master mode: no healthy cluster nodes to sync models from")
        return
    }

    var wg sync.WaitGroup
    for _, node := range nodes {
        wg.Add(1)
        go func(n *Node) {
            defer wg.Done()
            defer func() {
                if r := recover(); r != nil {
                    slog.Error("cluster model sync panic recovered",
                        "node_id", n.ID, "panic", r, "stack", string(debug.Stack()))
                }
            }()
            d.fetchModels(n)
        }(node)
    }
    wg.Wait()
    slog.Info("master mode node model registries synced", "node_count", len(nodes))
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
        // RR13: fetch metrics + models BEFORE marking healthy. The prior order
        // (markHealthy then fetchModels) opened a stale window — a node was
        // selectable by SelectNodeByModel with a stale/empty model registry
        // (first boot: servesModel empty → cloud fallback; or a node that just
        // unloaded a model but fetchModels hadn't re-run → routed → 400).
        // Fetching first means by the time the node is healthy its registry is
        // current; modelsReady (set in fetchModels) gates servesModel so an
        // unpolled healthy node is never routed a model-specific request.
        d.fetchRemoteMetrics(node)
        d.fetchModels(node)
        node.markHealthy()
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
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&statusResp); err != nil {
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

// fetchModels polls the node's GET /v1/models to build the per-node model
// registry (#95). Non-fatal: a node that doesn't implement /v1/models or
// returns an error keeps an empty registry and is never selected by model
// (cloud fallback). Runs on every health-check tick alongside
// fetchRemoteMetrics.
func (d *Discovery) fetchModels(node *Node) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.Address+"/v1/models", nil)
    if err != nil {
        slog.Debug("cluster models: create request failed", "node_id", node.ID, "error", err)
        return
    }

    resp, err := d.client.Do(req)
    if err != nil {
        slog.Debug("cluster models fetch failed", "node_id", node.ID, "error", err)
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        _, _ = io.Copy(io.Discard, resp.Body)
        slog.Debug("cluster models: non-200 status", "node_id", node.ID, "status", resp.StatusCode)
        return
    }

    var models []adapter.ModelInfo
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&models); err != nil {
        _, _ = io.Copy(io.Discard, resp.Body)
        slog.Debug("cluster models decode failed", "node_id", node.ID, "error", err)
        return
    }

    ids := make([]string, 0, len(models))
    for _, m := range models {
        if m.ID != "" {
            ids = append(ids, m.ID)
        }
    }

    node.mu.Lock()
    node.models = ids
    node.modelsReady = true
    node.mu.Unlock()

    slog.Debug("cluster models updated",
        "node_id", node.ID,
        "model_count", len(ids),
    )
}

func (d *Discovery) checkFailureThreshold(node *Node) {
    // d.cfg is written by UpdateConfig under d.mu; read it under RLock so a
    // hot-reload does not race the health-check goroutine (caught by -race in
    // TestRun_OnReloadCallback). config.ClusterConfig is a value type copied
    // on assignment, so snapshotting the one field under RLock is safe.
    d.mu.RLock()
    threshold := d.cfg.FailureThreshold
    d.mu.RUnlock()
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
        if n.selectable() {
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
        if n.selectable() && n.Platform == platform {
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
        if n.selectable() && n.Platform == platform {
            healthy = append(healthy, n)
        }
    }
    d.mu.RUnlock()

    if len(healthy) == 0 {
        return nil, fmt.Errorf("no healthy cluster nodes on platform %q", platform)
    }

    strategy = d.resolveStrategy(strategy)
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

// HealthyNodesByModel returns the count of healthy nodes whose model registry
// contains the given model. Empty model matches all healthy nodes (legacy
// behavior). Model-aware cluster routing (#95).
func (d *Discovery) HealthyNodesByModel(model string) int {
    if model == "" {
        return d.HealthyNodes()
    }
    d.mu.RLock()
    defer d.mu.RUnlock()
    count := 0
    for _, n := range d.nodes {
        if n.selectable() && n.servesModel(model) {
            count++
        }
    }
    return count
}

// SelectNodeByModel selects a healthy node that serves the given model using
// the configured strategy. Empty model falls back to platform-agnostic
// SelectNode. Model-aware cluster routing (#95) — prevents the upstream 404
// when a selected node doesn't serve the requested model.
//
// maxConcurrent (>0) applies a per-node slot cap (#102 ADR-001): a healthy
// node serving the model whose InFlight >= maxConcurrent is skipped — its
// slots are full. This reuses routing.local_priority.max_concurrent as the
// per-node ceiling (a node and local share the same slot-budget notion).
// maxConcurrent <= 0 disables the cap (legacy behavior).
func (d *Discovery) SelectNodeByModel(strategy, model string, maxConcurrent int) (*Node, error) {
    if model == "" {
        return d.SelectNode(strategy)
    }
    d.mu.RLock()
    var healthy []*Node
    var skipped int
    for _, n := range d.nodes {
        if !n.selectable() || !n.servesModel(model) {
            continue
        }
        if maxConcurrent > 0 && n.InFlight() >= int64(maxConcurrent) {
            skipped++
            slog.Debug("cluster node at slot cap, skipping",
                "node_id", n.ID, "in_flight", n.InFlight(), "max_concurrent", maxConcurrent)
            continue
        }
        healthy = append(healthy, n)
    }
    d.mu.RUnlock()

    if len(healthy) == 0 {
        if skipped > 0 {
            return nil, fmt.Errorf("no healthy cluster nodes serving model %q below slot cap (max_concurrent=%d, %d node(s) full)", model, maxConcurrent, skipped)
        }
        return nil, fmt.Errorf("no healthy cluster nodes serving model %q", model)
    }

    strategy = d.resolveStrategy(strategy)
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

// SelectNodeIDByModel wraps SelectNodeByModel, returning the node ID string
// for the router.ClusterSelector interface (#95).
func (d *Discovery) SelectNodeIDByModel(strategy, model string, maxConcurrent int) (string, error) {
    node, err := d.SelectNodeByModel(strategy, model, maxConcurrent)
    if err != nil {
        return "", err
    }
    return node.ID, nil
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

// NodePlatform returns the platform tag of the node with the given id, or ""
// when the node is unknown (issue #150 Gap2). Platform is set at registration
// and never mutated, so it is read lock-free here — the d.mu.RLock only
// protects the nodes map lookup, not the Platform field read.
func (d *Discovery) NodePlatform(id string) string {
    d.mu.RLock()
    n, ok := d.nodes[id]
    d.mu.RUnlock()
    if !ok {
        return ""
    }
    return n.Platform
}

func (d *Discovery) Status() []NodeStatus {
    d.mu.RLock()
    defer d.mu.RUnlock()

    result := make([]NodeStatus, 0, len(d.nodes))
    for _, n := range d.nodes {
        n.mu.RLock()
        st := NodeStatus{
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
            MaxConcurrent: d.maxConcurrent,
        }
        n.mu.RUnlock()
        // #163: BreakerBypassed is an atomic.Bool — read lock-free outside
        // n.mu so we never hold the node lock while calling the router
        // breaker resolver (avoids any lock-ordering risk with breakerMu).
        bypassed := n.BreakerBypassed()
        st.BreakerBypassed = bypassed
        st.Routable = n.State() == NodeStateHealthy && !bypassed
        if d.breakerStateFn != nil {
            st.BreakerState = d.breakerStateFn(n.ID)
        }
        if st.BreakerState == "" {
            // Unknown node or unwired resolver: a node with no recorded
            // failures is closed by definition (matches engine.NodeBreakerState).
            if bypassed {
                st.BreakerState = "open"
            } else {
                st.BreakerState = "closed"
            }
        }
        result = append(result, st)
    }
    return result
}

// SetAdmissionView wires the gateway-owned per-node concurrency budget and the
// router's aggregated per-node breaker resolver into Discovery (#163), so
// Status() can expose them to clients. DI from main.go avoids a cluster→router
// import cycle. maxConcurrent is the per-node slot cap (0 = uncapped/legacy,
// matching RR4 semantics); breakerState returns "closed"|"open"|"half_open"
// for a node ID. Safe to call once at startup; reads are lock-free (the two
// fields are only written here before serving begins).
func (d *Discovery) SetAdmissionView(maxConcurrent int, breakerState func(nodeID string) string) {
    d.maxConcurrent = maxConcurrent
    d.breakerStateFn = breakerState
    slog.Info("cluster admission view wired (#163 client visibility)",
        "max_concurrent", maxConcurrent, "resolver_wired", breakerState != nil)
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
    // #163: client-visibility fields exposing the gateway's aggregated
    // admission budget + shared per-node breaker state. BreakerBypassed is the
    // R8 flag (node tripped open by the router); Routable is the single
    // selectable() gate (healthy AND not bypassed); MaxConcurrent is the
    // gateway-owned per-node slot budget (0 = uncapped/legacy); BreakerState
    // is the granular "closed"|"open"|"half_open". Clients poll these to route
    // around a failed node without per-process rediscovery (#163 P1-R4).
    BreakerBypassed bool   `json:"breaker_bypassed"`
    Routable        bool   `json:"routable"`
    MaxConcurrent   int    `json:"max_concurrent"`
    BreakerState    string `json:"breaker_state"`
}

func (d *Discovery) SelectNode(strategy string) (*Node, error) {
    healthy := d.HealthyNodeList()
    if len(healthy) == 0 {
        return nil, fmt.Errorf("no healthy cluster nodes available")
    }

    strategy = d.resolveStrategy(strategy)
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
        if n.selectable() {
            count++
        }
    }
    return count
}

// MarkNodeBreakerOpen marks nodeID breaker-bypassed (R8): the router's per-node
// circuit breaker tripped open on repeated request failures. The node stays
// healthy (the /health probe may still succeed) but is not selectable until
// MarkNodeBreakerClosed. No-op for unknown nodeID. This is the push side of the
// R8 single-source-of-truth coordination: the breaker transition is the one
// event that updates routability, so Discovery and the router can never
// disagree about whether a node is routable.
func (d *Discovery) MarkNodeBreakerOpen(nodeID string) {
    if nodeID == "" {
        return
    }
    d.mu.RLock()
    n, ok := d.nodes[nodeID]
    d.mu.RUnlock()
    if !ok {
        slog.Debug("MarkNodeBreakerOpen: unknown node", "node_id", nodeID)
        return
    }
    n.SetBreakerBypassed(true)
}

// MarkNodeBreakerClosed clears the breaker-bypassed flag (R8): the breaker
// recovered to closed (cooldown elapsed + a half-open probe succeeded). The
// node is selectable again on the next SelectNode* call. No-op for unknown
// nodeID or an already-selectable node.
func (d *Discovery) MarkNodeBreakerClosed(nodeID string) {
    if nodeID == "" {
        return
    }
    d.mu.RLock()
    n, ok := d.nodes[nodeID]
    d.mu.RUnlock()
    if !ok {
        slog.Debug("MarkNodeBreakerClosed: unknown node", "node_id", nodeID)
        return
    }
    n.SetBreakerBypassed(false)
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

func (a *ClusterSelectorAdapter) HealthyNodesByModel(model string) int {
    return a.discovery.HealthyNodesByModel(model)
}

func (a *ClusterSelectorAdapter) SelectNodeByModel(strategy, model string, maxConcurrent int) (string, error) {
    return a.discovery.SelectNodeIDByModel(strategy, model, maxConcurrent)
}

// MarkNodeBreakerOpen implements router.ClusterSelector (R8).
func (a *ClusterSelectorAdapter) MarkNodeBreakerOpen(nodeID string) {
    a.discovery.MarkNodeBreakerOpen(nodeID)
}

// MarkNodeBreakerClosed implements router.ClusterSelector (R8).
func (a *ClusterSelectorAdapter) MarkNodeBreakerClosed(nodeID string) {
    a.discovery.MarkNodeBreakerClosed(nodeID)
}

// NodePlatform implements router.ClusterSelector (#150 Gap2 HA routing).
func (a *ClusterSelectorAdapter) NodePlatform(nodeID string) string {
    return a.discovery.NodePlatform(nodeID)
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

    // MemoryGB is mutated by syncFromMaster/applyStaticNodes under node.mu;
    // read it once under the read lock so nodeScore is race-free against a
    // concurrent sync (the periodic masterSyncLoop rewrites node fields).
    n.mu.RLock()
    memGB := float64(n.MemoryGB)
    n.mu.RUnlock()

    memScore := memGB

    if metrics.CollectedAt.IsZero() {
        // No remote metrics yet — fall back to static scoring
        inf := n.InFlight()
        if inf > 0 {
            memScore /= float64(inf + 1)
        }
        return memScore
    }

    // Memory headroom: (1 - used_ratio) * totalGB
    memAvail := (1.0 - metrics.MemoryUsedRatio) * memGB
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
    score := memAvail*0.6 + memGB*queueFactor*0.3 + memGB*inFlightFactor*0.1

    slog.Debug("cluster node score",
        "node_id", n.ID,
        "mem_avail_gb", memAvail,
        "queue_depth", metrics.QueueDepth,
        "in_flight", inf,
        "score", score,
    )

    return score
}
