package browser

import (
    "context"
    "crypto/sha1"
    "encoding/hex"
    "fmt"
    "log/slog"
    "sort"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/jitter"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

// NodeState is the liveness state of a browser node in the registry.
type NodeState string

const (
    NodeStateLive NodeState = "live"
    NodeStateDead NodeState = "dead"
)

// nodeSource records how a node entered the registry: static config seed or
// dial-in self-registration.
type nodeSource string

const (
    sourceConfig nodeSource = "config"
    sourceDialin nodeSource = "dialin"
)

// BrowserNode is one entry in the registry. The registry keys on the stable
// config id (NodeID), NOT the per-process node_id stored in Capacity — a
// browser restart mints a new node_id but the config label is stable, so the
// poll just overwrites the stale node_id under the same key (no placement
// churn).
type BrowserNode struct {
    NodeID     string        // stable config label (registry key)
    SocketPath string        // UDS path the gateway dials to forward
    Token      string        // per-node UDS auth credential (FR-10/H-5 handshake)
    Capacity   *FBNodeCapacity // nil until first successful poll
    State      NodeState
    failures   int
    lastPoll   time.Time
    source     nodeSource
}

// nodeView is a read-only snapshot of a node for the scheduler + admin map.
// Copied under the registry lock so callers never hold the lock across I/O
// (E8: one owner per field, no lock nesting into the scheduler/proxy).
type nodeView struct {
    NodeID     string
    SocketPath string
    Token      string
    Capacity   *FBNodeCapacity
    State      NodeState
    Failures   int
    LastPoll   time.Time
    Source     nodeSource
}

// Registry holds the browser node set + capacity snapshots under a RWMutex
// (one owner per field — no lock nesting, E8). Hybrid discovery: static
// config seed on New + DrainAndApply, dial-in register on a self-registering
// capacity frame. A 5s background poll (jittered, H5) refreshes capacity and
// flips nodes live/dead by failure_threshold.
type Registry struct {
    mu      sync.RWMutex
    nodes   map[string]*BrowserNode // keyed by NodeID (stable config label)

    client        *NodeClient
    pollInterval  time.Duration
    failureThreshold int
    recoveryInterval time.Duration

    worker *lifecycle.Worker
}

// NewRegistry seeds the node set from the static config (browser.nodes) and
// stores the poll knobs. Capacity is nil until the first poll succeeds. Does
// NOT start the poll — call Start to launch the worker. Config-id duplicates
// are rejected loudly (two nodes with the same label is an operator error).
func NewRegistry(client *NodeClient, pollInterval time.Duration, failureThreshold int, recoveryInterval time.Duration, seed []config.BrowserNodeConfig) (*Registry, error) {
    r := &Registry{
        nodes:            make(map[string]*BrowserNode),
        client:           client,
        pollInterval:     pollInterval,
        failureThreshold: failureThreshold,
        recoveryInterval: recoveryInterval,
    }
    for _, n := range seed {
        if _, dup := r.nodes[n.ID]; dup {
            return nil, fmt.Errorf("browser: duplicate node id %q in static seed", n.ID)
        }
        r.nodes[n.ID] = &BrowserNode{
            NodeID:     n.ID,
            SocketPath: n.SocketPath,
            Token:      n.Token,
            State:      NodeStateLive,
            source:     sourceConfig,
        }
    }
    slog.Info("browser registry seeded", "node_count", len(r.nodes))
    return r, nil
}

// Start launches the capacity poll worker via lifecycle.Worker (panic-recover
// + join-on-stop, EI10/H3). The worker is allowlisted by the CI bare-goroutine
// gate. Idempotent: a second Start is a no-op (logs a warning).
func (r *Registry) Start(ctx context.Context) {
    if r.worker != nil {
        slog.Warn("browser registry poll worker already started")
        return
    }
    r.worker = lifecycle.Start(ctx, "browser-poll", r.pollLoop)
}

// Stop cancels the poll worker and blocks until it exits (lifecycle.Worker
// join, EI10). Safe to call when never started or already stopped.
func (r *Registry) Stop() {
    if r.worker == nil {
        return
    }
    r.worker.Stop()
    r.worker = nil
}

// pollLoop is the worker body. Each iteration polls every live node (dead
// nodes wait recoveryInterval before re-probe), refreshes capacity, and flips
// state by failure_threshold. The tick is jittered (H5) so a cluster of
// gateways desyncs rather than spiking every node at the tick edge.
func (r *Registry) pollLoop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case <-jitter.After(r.pollInterval):
        }
        r.pollOnce(ctx)
    }
}

// pollOnce queries every node due for a poll. Live nodes poll every tick;
// dead nodes re-probe only after recoveryInterval (avoid hammering a down
// node). A successful poll resets failures + flips dead→live; a failure
// increments failures and flips live→dead at the threshold.
func (r *Registry) pollOnce(ctx context.Context) {
    due := r.dueNodes()
    for _, view := range due {
        cap, err := r.client.Capacity(ctx, view.SocketPath, view.Token)
        r.mu.Lock()
        node, ok := r.nodes[view.NodeID]
        if !ok {
            // Node removed via hot-reload between snapshot + poll; drop it.
            r.mu.Unlock()
            continue
        }
        if err != nil {
            r.recordFailure(node, err)
            r.mu.Unlock()
            continue
        }
        r.recordSuccess(node, cap)
        r.mu.Unlock()
    }
}

// dueNodes returns a snapshot of nodes that should be polled this tick. Live
// nodes are always due; dead nodes are due only if recoveryInterval has
// elapsed since the last poll (rate-limits re-probe of a down node).
func (r *Registry) dueNodes() []nodeView {
    r.mu.RLock()
    defer r.mu.RUnlock()
    now := time.Now()
    var out []nodeView
    for _, n := range r.nodes {
        if n.State == NodeStateDead {
            if now.Sub(n.lastPoll) < r.recoveryInterval {
                continue
            }
        }
        out = append(out, r.view(n))
    }
    return out
}

// recordSuccess stores the fresh capacity snapshot, resets failures, and
// flips a recovering dead node back to live. Caller holds the write lock.
func (r *Registry) recordSuccess(node *BrowserNode, cap *FBNodeCapacity) {
    wasDead := node.State == NodeStateDead
    node.Capacity = cap
    node.failures = 0
    node.lastPoll = time.Now()
    node.State = NodeStateLive
    if wasDead {
        slog.Info("browser node recovered", "node", node.NodeID, "socket", node.SocketPath)
    }
}

// recordFailure increments the failure count and flips the node dead at the
// threshold. The node stays in the map (the admin map shows dead nodes) but
// is skipped by placement. Caller holds the write lock.
func (r *Registry) recordFailure(node *BrowserNode, err error) {
    node.failures++
    node.lastPoll = time.Now()
    if node.failures >= r.failureThreshold && node.State == NodeStateLive {
        node.State = NodeStateDead
        slog.Warn("browser node marked dead",
            "node", node.NodeID, "socket", node.SocketPath,
            "failures", node.failures, "threshold", r.failureThreshold, "error", err)
    } else {
        slog.Debug("browser node poll failed",
            "node", node.NodeID, "failures", node.failures, "error", err)
    }
}

// RegisterDialin adds a self-registering node learned from a dial-in capacity
// frame. The socket path is how the gateway will forward later (the dialer's
// UDS peer address). token is the auth credential for subsequent ops on this
// node — a dial-in node that authenticated its registration frame carries the
// same token for forwarding (FR-10/H-5 handshake on every dial). Mints a
// config-style label dialin-<short-hash(socket)>. If the socket already has a
// config-seed node, the dial-in is a no-op (the config id wins — it is the
// operator's stable label).
func (r *Registry) RegisterDialin(socketPath, token string, cap *FBNodeCapacity) string {
    id := dialinLabel(socketPath)
    r.mu.Lock()
    defer r.mu.Unlock()
    if existing, ok := r.findBySocket(socketPath); ok {
        // Already known (config seed or prior dial-in); refresh capacity only.
        existing.Capacity = cap
        existing.failures = 0
        existing.lastPoll = time.Now()
        existing.State = NodeStateLive
        slog.Debug("browser dial-in node already registered, refreshed",
            "node", existing.NodeID, "socket", socketPath)
        return existing.NodeID
    }
    r.nodes[id] = &BrowserNode{
        NodeID:     id,
        SocketPath: socketPath,
        Token:      token,
        Capacity:   cap,
        State:      NodeStateLive,
        lastPoll:   time.Now(),
        source:     sourceDialin,
    }
    slog.Info("browser dial-in node registered", "node", id, "socket", socketPath)
    return id
}

// findBySocket locates a node by socket path (caller holds the lock). Used by
// RegisterDialin to dedup a dial-in against an existing config/dial-in node.
func (r *Registry) findBySocket(socketPath string) (*BrowserNode, bool) {
    for _, n := range r.nodes {
        if n.SocketPath == socketPath {
            return n, true
        }
    }
    return nil, false
}

// dialinLabel mints a stable label for a dial-in node from its socket path.
// Short hash so the admin map is readable; collisions are benign (two nodes
// on the same socket are the same node).
func dialinLabel(socketPath string) string {
    h := sha1.Sum([]byte(socketPath))
    return "dialin-" + hex.EncodeToString(h[:4])
}

// Snapshot returns a copy of all nodes (live + dead) for the scheduler +
// admin map. The scheduler filters to live; the admin map shows both. Copying
// under RLock means callers never hold the lock across the placement I/O
// (E8). Returns the views sorted by NodeID for deterministic output.
func (r *Registry) Snapshot() []nodeView {
    r.mu.RLock()
    defer r.mu.RUnlock()
    out := make([]nodeView, 0, len(r.nodes))
    for _, n := range r.nodes {
        out = append(out, r.view(n))
    }
    sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
    return out
}

// view copies a node into a read-only snapshot. The Capacity pointer is
// shared (FBNodeCapacity is never mutated in place — recordSuccess replaces
// it), so the snapshot is safe to read lock-free.
func (r *Registry) view(n *BrowserNode) nodeView {
    return nodeView{
        NodeID:     n.NodeID,
        SocketPath: n.SocketPath,
        Token:      n.Token,
        Capacity:   n.Capacity,
        State:      n.State,
        Failures:   n.failures,
        LastPoll:   n.lastPoll,
        Source:     n.source,
    }
}

// SocketOf returns the UDS socket path for a node id, for the proxy to dial
// when forwarding. Returns ok=false for an unknown id (a pin pointing at a
// removed node — hot-reload dropped it).
func (r *Registry) SocketOf(nodeID string) (string, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    n, ok := r.nodes[nodeID]
    if !ok {
        return "", false
    }
    return n.SocketPath, true
}

// TokenOf returns the per-node auth token for a node id, for the proxy to send
// in the auth handshake before forwarding. Returns ok=false for an unknown id.
// The token travels alongside SocketOf: every forward dials the socket AND
// authenticates with this token (FR-10/H-5 — the first frame on every dial
// must be auth).
func (r *Registry) TokenOf(nodeID string) (string, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    n, ok := r.nodes[nodeID]
    if !ok {
        return "", false
    }
    return n.Token, true
}

// IsDead reports whether a node is currently dead (for the proxy's dead-pin
// check). A pin pointing at a dead node means the in-flight session is LOST
// per contract — surface honestly, do not retry internally.
func (r *Registry) IsDead(nodeID string) bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    n, ok := r.nodes[nodeID]
    if !ok {
        // Unknown node is treated as dead for forwarding (no socket to dial).
        return true
    }
    return n.State == NodeStateDead
}

// DrainAndApply rebuilds the node set from a hot-reloaded config seed. Live
// capacity snapshots for unchanged socket paths are preserved (a reload that
// merely toggles an unrelated knob must not drop known capacity); removed
// nodes are dropped; new nodes are added live with nil capacity. Dial-in
// nodes are preserved across the reload (they were learned at runtime, not
// from config). EI3 lesson applied: prior breaker state is NOT inherited
// here because capacity is a live signal re-probed on the next tick, not a
// sticky trip state — a re-added node starts fresh and proves itself on the
// next poll.
func (r *Registry) DrainAndApply(seed []config.BrowserNodeConfig) {
    r.mu.Lock()
    defer r.mu.Unlock()
    newSet := make(map[string]*BrowserNode, len(seed))
    for _, n := range seed {
        if old, ok := r.nodes[n.ID]; ok && old.SocketPath == n.SocketPath {
            // Unchanged config node: preserve capacity + state (it is still
            // the same node the poll already knows).
            newSet[n.ID] = old
            continue
        }
        newSet[n.ID] = &BrowserNode{
            NodeID:     n.ID,
            SocketPath: n.SocketPath,
            Token:      n.Token,
            State:      NodeStateLive,
            source:     sourceConfig,
        }
    }
    // Preserve dial-in nodes not in the new config seed (runtime-learned).
    for id, n := range r.nodes {
        if n.source == sourceDialin {
            if _, keep := newSet[id]; !keep {
                newSet[id] = n
            }
        }
    }
    r.nodes = newSet
    slog.Info("browser registry reloaded", "node_count", len(r.nodes))
}
