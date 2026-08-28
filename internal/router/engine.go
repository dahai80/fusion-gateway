package router

import (
    "context"
    "fmt"
    "log/slog"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

type Backend string

const (
    LocalBackend   Backend = "local"
    CloudBackend   Backend = "cloud"
    ClusterBackend Backend = "cluster"
)

type RequestType string

const (
    RequestTypeChat      RequestType = "chat"
    RequestTypeEmbedding RequestType = "embedding"
    RequestTypeRerank    RequestType = "rerank"
)

// defaultRatioMinInputTokens is the floor below which the output/input ratio
// check is skipped when config does not set output_input_ratio_min_input_tokens.
// Tiny prompts produce statistically meaningless ratios (issue #48).
const defaultRatioMinInputTokens = 32

type RouteDecision struct {
    Backend     Backend
    Reason      string
    NodeID      string
    CloudTarget string
    // Adapter names the LoRA adapter (e.g. "lora-code") that the server layer
    // must hot-mount on fusion-mlx via the per-request "adapters" field before
    // forwarding. Empty = no adapter swap (bare base model). Set only by the
    // intent:code path. See fusion-gateway UDS zero-copy + intent-routing.
    Adapter string
}

type RouteRequest struct {
    Model    string
    Messages interface{}
    // Text is the flattened request text (last user message for chat). Populated
    // by the server for the D4 semantic intent classifier (issue #22) so the
    // router need not depend on the adapter message type. Empty for non-text
    // requests (embeddings/rerank) where classification is not meaningful.
    Text    string
    Stream  bool
    Tools   interface{}
    Type    RequestType
    SpaceID string
}

type ClusterSelector interface {
    HealthyNodes() int
    SelectNode(strategy string) (nodeID string, err error)
    // HealthyNodesByPlatform returns healthy node count for a given platform
    // (D4 dispatch-by-platform, issue #23/#25). Empty platform = all nodes.
    HealthyNodesByPlatform(platform string) int
    // SelectNodeByPlatform selects a healthy node on the given platform.
    // Empty platform falls back to platform-agnostic selection.
    SelectNodeByPlatform(strategy, platform string) (nodeID string, err error)
    // HealthyNodesByModel returns healthy node count that serve the given
    // model (model-aware cluster routing, issue #95). Empty model = all nodes.
    HealthyNodesByModel(model string) int
    // SelectNodeByModel selects a healthy node that serves the given model.
    // Empty model falls back to platform-agnostic selection. Error when no
    // healthy node serves the model (caller falls through to cloud).
    // maxConcurrent (>0) skips nodes whose in-flight slots are full (#102).
    SelectNodeByModel(strategy, model string, maxConcurrent int) (nodeID string, err error)
    // MarkNodeBreakerOpen marks nodeID not-selectable: the router's per-node
    // circuit breaker tripped open (R8). The node stays healthy but is skipped
    // by SelectNode* until MarkNodeBreakerClosed. This push makes Discovery the
    // single source of truth for routability, eliminating the prior post-hoc
    // bypass where Discovery reported a node routable while the router refused.
    MarkNodeBreakerOpen(nodeID string)
    // MarkNodeBreakerClosed clears the breaker-bypass flag (R8): the breaker
    // recovered to closed, the node is selectable again.
    MarkNodeBreakerClosed(nodeID string)
}

type Engine struct {
    // E8 (audit): Engine.mu was a single RWMutex serializing breakers,
    // session affinity, hardware metrics, and local in-flight wiring. The
    // audit's runtime premise was already mitigated (L1 lock-free Trip,
    // B1 intent classification outside RLock, SessionAffinity/Collector
    // self-lock, atomic in-flight). This split codifies per-concern locks so
    // the one genuinely contended write (breaker map rebuild in
    // DrainAndApply + lazy node create) gets its own lock, and the
    // rare-write wiring fields (cfg/localReady/localInFlight/localModels/
    // classifiers/adapterLookup/cluster/localQueue) share a second.
    // affinityMu/hardwareMu guard pointers that are currently immutable
    // post-construction (future-proof against a later reset). Contract:
    // NO goroutine holds two of these at once — Decide takes inFlightMu
    // (snapshot wiring), releases, then breakerMu (breaker reads).
    // DrainAndApply/RecordNode* use separate sequential windows. Leaf locks
    // (affinity/hardware) never nest. Zero lock-ordering surface.
    breakerMu  sync.RWMutex
    affinityMu sync.RWMutex
    hardwareMu sync.RWMutex
    inFlightMu sync.RWMutex

    cfg             *config.ConfigSnapshot
    hwCollector     *hardware.Collector
    breakers        map[string]*CircuitBreaker
    // nodeBreakers holds a per-node circuit breaker keyed by node ID (RR5,
    // audit P1). The prior design had a single "cluster" breaker: one bad
    // node's failures accumulated into it and tripped the whole cluster,
    // poisoning the N-1 healthy nodes (all traffic forced to cloud). Per-node
    // breakers isolate a failing node — it trips itself and traffic bypasses
    // it, while healthy nodes keep serving. The cluster breaker stays as an
    // aggregate view (cluster-wide conditions), never tripped by a single
    // node's failures. Lazy-created on first record/lookup.
    nodeBreakers    map[string]*CircuitBreaker
    localReady      bool
    localInFlight   func() int64
    localModels     func() map[string]bool
    cluster         ClusterSelector
    sessionAffinity *SessionAffinity
    classifier      IntentClassifier
    // heuristicClassifier is the in-process sub-ms intent classifier that
    // replaces the sync LLM RouterLightClassifier on the code path (latency
    // lever for <20ms gateway overhead). Wired via SetHeuristicClassifier;
    // nil = disabled (fall through to classifier/rule chain). Same
    // IntentClassifier interface so the engine treats both uniformly.
    heuristicClassifier IntentClassifier
    // adapterLookup is a read-only view over the local LoRA adapter index
    // (Stream D). Wired via SetAdapterLookup; nil = no index available, so
    // code-adapter validation is skipped (best-effort, log-only on miss).
    adapterLookup AdapterLookup
    // localQueue is the opt-in FIFO wait-queue over local inference slots,
    // engaged ONLY when routing.mode=local AND queue_enabled (#102 ADR-001
    // sub-task 3). nil in hybrid/cloud mode — the engine falls back to cloud
    // instead of queueing, so the handler never gates. The handler (not the
    // engine) calls Acquire before forwarding; the engine stays pure (no
    // blocking). QueueTimeout lives in config so hot-reload can tune it.
    localQueue *slotQueue
}

// routeSnapshot is the read-only capture of every wiring field Decide needs,
// snapshotted under a single inFlightMu.RLock (plus the leaf affinity/hardware
// pointer reads) and RELEASED before breakerMu is taken for decideLocked. E8:
// the hot path must never hold inFlightMu and breakerMu simultaneously (no
// lock nesting -> no lock-ordering deadlock). The breakers are NOT in the
// snapshot — they are read under breakerMu inside decideLocked/tryCluster*
// because they are the one contended write surface.
type routeSnapshot struct {
    localReady          bool
    localInFlight       func() int64
    localModels         func() map[string]bool
    hwCollector         *hardware.Collector
    sessionAffinity     *SessionAffinity
    classifier          IntentClassifier
    heuristicClassifier IntentClassifier
    adapterLookup       AdapterLookup
    cluster             ClusterSelector
    localQueue          *slotQueue
}

func NewEngine(cfg *config.ConfigSnapshot, hwCollector *hardware.Collector) *Engine {
    breakers := map[string]*CircuitBreaker{
        "local":  NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cloud":  NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cluster": NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
    }

    e := &Engine{
        cfg:             cfg,
        hwCollector:     hwCollector,
        breakers:        breakers,
        nodeBreakers:    make(map[string]*CircuitBreaker),
        localReady:      false,
        localInFlight:   func() int64 { return 0 },
        localModels:     func() map[string]bool { return nil },
        sessionAffinity: NewSessionAffinity(30 * time.Minute),
        classifier:      NoopClassifier{},
    }

    // Sub-task 3 (#102 ADR-001): opt-in local wait-queue, engaged ONLY in
    // mode=local + queue_enabled. In hybrid/cloud mode the engine falls back
    // to cloud (no queue), so localQueue stays nil and the handler never
    // gates — zero behavior change for the default hybrid path.
    e.localQueue = buildLocalQueue(cfg)

    return e
}

// buildLocalQueue constructs the opt-in local wait-queue from a config
// snapshot: a *slotQueue when routing.mode=local AND
// local_priority.queue_enabled, else nil. Shared between NewEngine (initial
// build) and DrainAndApply (hot-reload rebuild, RR6) so both sites stay in
// sync — the queue is NEVER silently left stale across a config change.
// max_concurrent<=0 defaults to 8 (same as the original inline build).
func buildLocalQueue(cfg *config.ConfigSnapshot) *slotQueue {
    if cfg.Config.Routing.Mode != "local" || !cfg.Config.Routing.LocalPriority.QueueEnabled {
        return nil
    }
    maxConcurrent := cfg.Config.Routing.LocalPriority.MaxConcurrent
    if maxConcurrent <= 0 {
        maxConcurrent = 8
    }
    q := newSlotQueue(maxConcurrent)
    slog.Info("local slot wait-queue built",
        "mode", "local",
        "max_concurrent", maxConcurrent,
        "queue_timeout", cfg.Config.Routing.LocalPriority.QueueTimeout)
    return q
}

// queueStateString renders a localQueue for the RR6 hot-reload transition
// log: "disabled" (nil) or "cap=N" so an operator sees a queue flip or
// capacity change explicitly rather than a generic "config applied".
func queueStateString(q *slotQueue) string {
    if q == nil {
        return "disabled"
    }
    return fmt.Sprintf("cap=%d", cap(q.sem))
}

func (e *Engine) SetClusterSelector(cs ClusterSelector) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.cluster = cs
    slog.Info("cluster selector wired to router engine")
}

// SetIntentClassifier wires a D4 semantic intent classifier (issue #22).
// When nil or NoopClassifier, the semantic layer is a no-op and the existing
// P0-P7 rule chain decides routing unchanged.
func (e *Engine) SetIntentClassifier(c IntentClassifier) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.classifier = c
    slog.Info("intent classifier wired to router engine")
}

// SetHeuristicClassifier wires the in-process sub-ms heuristic intent
// classifier (latency lever for <20ms gateway overhead, replaces the sync LLM
// classifier on the code path). When nil, the heuristic layer is a no-op and
// routing falls through to the LLM classifier (if enabled) then the rule chain.
// Safe to call on hot-reload — guarded by inFlightMu like the LLM one.
func (e *Engine) SetHeuristicClassifier(c IntentClassifier) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.heuristicClassifier = c
    if c == nil {
        slog.Info("heuristic classifier disabled (nil), routing falls through to classifier/rule chain")
    } else {
        slog.Info("heuristic classifier wired to router engine")
    }
}

// SetAdapterLookup wires the read-only LoRA adapter index (Stream D) used for
// best-effort code_adapter validation on the heuristic code path. nil = no
// index, validation skipped (the index may legitimately be absent until the
// first refresh lands). Safe to call on hot-reload.
func (e *Engine) SetAdapterLookup(a AdapterLookup) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.adapterLookup = a
    if a == nil {
        slog.Info("adapter lookup disabled (nil), code_adapter validation skipped")
    } else {
        slog.Info("adapter lookup wired to router engine")
    }
}

func (e *Engine) UpdateConfig(cfg *config.ConfigSnapshot) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.cfg = cfg
    slog.Info("router engine config updated", "version", cfg.Version)
}

func (e *Engine) DrainAndApply(cfg *config.ConfigSnapshot) {
    drainTimeout := cfg.Config.HotReload.BreakerDrainTimeout
    if drainTimeout == 0 {
        drainTimeout = 10 * time.Second
    }

    // Phase 1: Drain - wait for in-flight requests to drop. localInFlight is a
    // func callback; snapshot the (stable-during-drain) pointer under
    // inFlightMu.RLock once so the loop does not re-acquire per poll.
    e.inFlightMu.RLock()
    inFlightFn := e.localInFlight
    e.inFlightMu.RUnlock()
    deadline := time.Now().Add(drainTimeout)
    for time.Now().Before(deadline) {
        if inFlightFn() == 0 {
            slog.Info("drain complete: no in-flight requests")
            break
        }
        slog.Info("draining: waiting for in-flight requests", "in_flight", inFlightFn())
        time.Sleep(200 * time.Millisecond)
    }
    if inFlightFn() > 0 {
        slog.Warn("drain timeout: in-flight requests remain", "in_flight", inFlightFn())
    }

    // Phase 2: Apply — split across two SEPARATE lock windows (E8: never hold
    // inFlightMu and breakerMu simultaneously). The cfg pointer + the
    // localQueue (both inFlightMu-owned) swap first; the breaker maps
    // (breakerMu-owned) swap second. cfg is the function param so breaker
    // construction reads the param, not the field — no cross-lock read.
    e.inFlightMu.Lock()
    e.cfg = cfg
    oldQueueState := queueStateString(e.localQueue)
    e.localQueue = buildLocalQueue(cfg)
    newQueueState := queueStateString(e.localQueue)
    e.inFlightMu.Unlock()
    if oldQueueState != newQueueState {
        slog.Info("config applied: local queue rebuilt", "version", cfg.Version, "before", oldQueueState, "after", newQueueState)
    }

    // EI3: inherit the OLD breakers' trip state onto the NEW ones. Without
    // inheritance, a hot-reload swaps e.breakers for brand-new closed breakers
    // — an already-open (failing) backend looks healthy to the new breaker, so
    // requests keep hitting it until the new breaker re-accumulates enough
    // failures to trip again. A concurrent RecordFailure landing on the old
    // breaker (GC'd post-swap) was the original race; inheriting state means the
    // new breaker opens immediately and the operator sees WHY (tripReason),
    // not a blank "config applied". Snapshot under breakerMu.Lock BEFORE the
    // swap so the snapshot+swap is atomic w.r.t. concurrent breaker reads.
    e.breakerMu.Lock()
    inheritedLocal := snapshotBreakerLocked(e.breakers, "local")
    inheritedCloud := snapshotBreakerLocked(e.breakers, "cloud")
    inheritedCluster := snapshotBreakerLocked(e.breakers, "cluster")
    inheritedNodes := make(map[string]BreakerSnapshot, len(e.nodeBreakers))
    for nodeID := range e.nodeBreakers {
        inheritedNodes[nodeID] = snapshotBreakerLocked(e.nodeBreakers, nodeID)
    }
    e.breakers = map[string]*CircuitBreaker{
        "local":   NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cloud":   NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
        "cluster": NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker),
    }
    e.breakers["local"].InheritSnapshot(inheritedLocal)
    e.breakers["cloud"].InheritSnapshot(inheritedCloud)
    e.breakers["cluster"].InheritSnapshot(inheritedCluster)
    // RR5: per-node breakers rebuilt fresh (lazy-recreated on next access via
    // nodeBreakerLocked) but inherit prior trip state so a known-bad node is
    // still seen as bad right after reload, not rediscovered by failure.
    e.nodeBreakers = make(map[string]*CircuitBreaker, len(inheritedNodes))
    for nodeID, snap := range inheritedNodes {
        nb := NewCircuitBreaker(cfg.Config.Routing.CircuitBreaker)
        nb.InheritSnapshot(snap)
        e.nodeBreakers[nodeID] = nb
    }
    e.breakerMu.Unlock()
    slog.Info("config applied: circuit breakers rebuilt", "version", cfg.Version)

    // Phase 3: Warmup - set local breaker to half_open for gradual recovery
    warmupSuccess := cfg.Config.HotReload.BreakerWarmupSuccess
    if warmupSuccess <= 0 {
        warmupSuccess = 3
    }
    e.breakerMu.RLock()
    if b, ok := e.breakers["local"]; ok {
        b.ResetToHalfOpen()
    }
    e.breakerMu.RUnlock()
    slog.Info("warmup started: local breaker set to half_open", "warmup_success_target", warmupSuccess)
}

func (e *Engine) SetLocalReady(ready bool) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.localReady = ready
}

func (e *Engine) SetLocalInFlight(fn func() int64) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.localInFlight = fn
}

// LocalInFlight exposes the local backend in-flight count for observability
// (#96 in_flight_requests gauge). Returns 0 before SetLocalInFlight is wired.
func (e *Engine) LocalInFlight() int64 {
    e.inFlightMu.RLock()
    fn := e.localInFlight
    e.inFlightMu.RUnlock()
    if fn == nil {
        return 0
    }
    return fn()
}

// LocalQueue exposes the opt-in local wait-queue (#102 ADR-001 sub-task 3).
// Returns nil when the queue is disabled (hybrid/cloud mode, or mode=local
// without queue_enabled) — callers MUST nil-check before Acquire. The handler
// (not the engine) calls Acquire before forwarding to keep the engine pure.
func (e *Engine) LocalQueue() *slotQueue {
    e.inFlightMu.RLock()
    defer e.inFlightMu.RUnlock()
    return e.localQueue
}

// Shutdown releases engine-owned background goroutines. B9: the session
// affinity evict loop is a long-lived worker launched in NewSessionAffinity;
// without an explicit Stop it survives process exit as a dangling goroutine
// (matters under graceful shutdown / config-reload test harnesses that reuse
// the engine). Called from cmd/gateway/main.go after srv.Shutdown + discovery
// stop, mirroring the F7 shutdown order.
func (e *Engine) Shutdown() {
    e.affinityMu.RLock()
    sa := e.sessionAffinity
    e.affinityMu.RUnlock()
    if sa != nil {
        sa.Stop()
    }
    slog.Info("router engine shutdown complete")
}

// QueueTimeout returns the configured wait budget for the local queue. Read
// from the live config snapshot so hot-reload can tune it without rebuilding
// the engine. Falls back to 5s when unset.
func (e *Engine) QueueTimeout() time.Duration {
    e.inFlightMu.RLock()
    cfg := e.cfg
    e.inFlightMu.RUnlock()
    t := cfg.Config.Routing.LocalPriority.QueueTimeout
    if t <= 0 {
        return 5 * time.Second
    }
    return t
}

func (e *Engine) SetLocalModels(fn func() map[string]bool) {
    e.inFlightMu.Lock()
    defer e.inFlightMu.Unlock()
    e.localModels = fn
}

func (e *Engine) CircuitBreakerState(backend string) CircuitBreakerState {
    e.breakerMu.RLock()
    defer e.breakerMu.RUnlock()
    if b, ok := e.breakers[backend]; ok {
        return b.State()
    }
    return StateOpen
}

// PublishBreakerStates pushes every breaker's current state to the Prometheus
// circuit_breaker_state gauge (#96). Catches lazy half_open transitions
// (open->half_open happens on State() read after Timeout, not at a call site)
// that the per-transition publishes in RecordSuccess/RecordFailure/Trip miss.
func (e *Engine) PublishBreakerStates() {
    e.breakerMu.RLock()
    backends := make([]string, 0, len(e.breakers))
    for backend := range e.breakers {
        backends = append(backends, backend)
    }
    nodeIDs := make([]string, 0, len(e.nodeBreakers))
    for nodeID := range e.nodeBreakers {
        nodeIDs = append(nodeIDs, nodeID)
    }
    e.breakerMu.RUnlock()
    for _, backend := range backends {
        observability.UpdateCircuitBreakerState(backend, int(e.CircuitBreakerState(backend)))
    }
    // RR5: also publish per-node breaker states so /metrics surfaces an
    // individual node tripping without it being hidden inside the aggregate.
    for _, nodeID := range nodeIDs {
        observability.UpdateCircuitBreakerState("node:"+nodeID, int(e.NodeBreakerState(nodeID)))
    }
}

// RecordAffinity records a space_id→provider binding. sessionAffinity has its
// OWN internal lock (SessionAffinity.mu), so the only Engine state read here
// is the pointer itself — snapshot it under affinityMu (leaf lock, never
// nested) then call sa.Record() lock-free relative to the engine.
func (e *Engine) RecordAffinity(spaceID, providerName string) {
    e.affinityMu.RLock()
    sa := e.sessionAffinity
    e.affinityMu.RUnlock()
    if sa != nil {
        sa.Record(spaceID, providerName)
    }
}

func (e *Engine) RecordSuccess(backend string) {
    // L1 fix: RLock to find breaker, then call RecordSuccess outside RLock.
    // E8: breakers live under breakerMu now.
    e.breakerMu.RLock()
    b, ok := e.breakers[backend]
    e.breakerMu.RUnlock()
    if ok {
        b.RecordSuccess()
        // Publish resulting state to Prometheus (#96): half_open->closed is a
        // real transition the /metrics gauge must reflect.
        observability.UpdateCircuitBreakerState(backend, int(b.State()))
    }
}

func (e *Engine) RecordFailure(backend string) {
    // L1 fix: RLock to find breaker, then call RecordFailure outside RLock.
    // E8: breakers live under breakerMu now.
    e.breakerMu.RLock()
    b, ok := e.breakers[backend]
    e.breakerMu.RUnlock()
    if ok {
        b.RecordFailure()
        // Publish resulting state to Prometheus (#96): closed->open on failure
        // threshold must reflect on the gauge, not stay 0.
        state := b.State()
        observability.UpdateCircuitBreakerState(backend, int(state))
        if state == StateOpen {
            // RecordFailure opened the breaker — count the implicit trip so the
            // trips counter reflects all open transitions, not just force-Trip.
            observability.RecordCircuitBreakerTrip(backend, "failure_threshold")
        }
    }
}

// nodeBreakerLocked returns the per-node circuit breaker for nodeID, creating
// it lazily under breakerMu (RR5). Caller MUST hold e.breakerMu (or use the
// public record/nodeState helpers that lock internally).
func (e *Engine) nodeBreakerLocked(nodeID string, cbCfg config.CircuitBreakerConfig) *CircuitBreaker {
    b, ok := e.nodeBreakers[nodeID]
    if !ok {
        b = NewCircuitBreaker(cbCfg)
        e.nodeBreakers[nodeID] = b
    }
    return b
}

// snapshotBreakerLocked returns the trip-state snapshot of the breaker keyed by
// k in m, or a zero (StateClosed) snapshot if absent. Used by DrainAndApply
// (EI3) to capture prior trip state before swapping the breaker map. Caller
// MUST hold e.breakerMu so the map read + breaker snapshot are atomic w.r.t.
// the swap.
func snapshotBreakerLocked(m map[string]*CircuitBreaker, k string) BreakerSnapshot {
    if b, ok := m[k]; ok {
        return b.Snapshot()
    }
    return BreakerSnapshot{}
}

// RecordNodeSuccess records a success on the per-node breaker for nodeID (RR5).
// Used instead of RecordSuccess("cluster") when a request was served by a
// specific cluster node, so one node's failures never accumulate into the
// shared cluster breaker. No-op for empty nodeID.
//
// E8: reads cfg (inFlightMu) + cluster (inFlightMu) in a SEPARATE window from
// the breaker lazy-create (breakerMu) — never holds both at once.
func (e *Engine) RecordNodeSuccess(nodeID string) {
    if nodeID == "" {
        return
    }
    e.inFlightMu.RLock()
    cbCfg := e.cfg.Config.Routing.CircuitBreaker
    cluster := e.cluster
    e.inFlightMu.RUnlock()
    e.breakerMu.Lock()
    b := e.nodeBreakerLocked(nodeID, cbCfg)
    e.breakerMu.Unlock()
    prevState := b.State()
    b.RecordSuccess()
    state := b.State()
    observability.UpdateCircuitBreakerState("node:"+nodeID, int(state))
    // R8: when the breaker recovers to closed (half_open→closed, or open→closed
    // on a successful half-open probe), push the recovery into Discovery so the
    // node is selectable again. Only push on a real transition (prev != closed)
    // so a steady-state success does not spam MarkNodeBreakerClosed.
    if state == StateClosed && prevState != StateClosed && cluster != nil {
        cluster.MarkNodeBreakerClosed(nodeID)
    }
}

// RecordNodeFailure records a failure on the per-node breaker for nodeID (RR5).
// When the node's breaker opens, tryCluster* bypasses it (the node trips
// itself; healthy nodes keep serving). No-op for empty nodeID.
//
// E8: reads cfg (inFlightMu) + cluster (inFlightMu) in a SEPARATE window from
// the breaker lazy-create (breakerMu) — never holds both at once.
func (e *Engine) RecordNodeFailure(nodeID string) {
    if nodeID == "" {
        return
    }
    e.inFlightMu.RLock()
    cbCfg := e.cfg.Config.Routing.CircuitBreaker
    cluster := e.cluster
    e.inFlightMu.RUnlock()
    e.breakerMu.Lock()
    b := e.nodeBreakerLocked(nodeID, cbCfg)
    e.breakerMu.Unlock()
    b.RecordFailure()
    state := b.State()
    observability.UpdateCircuitBreakerState("node:"+nodeID, int(state))
    if state == StateOpen {
        slog.Warn("per-node circuit breaker opened", "node_id", nodeID)
        observability.RecordCircuitBreakerTrip("node:"+nodeID, "failure_threshold")
        // R8: push the open transition into Discovery so it marks the node
        // breaker-bypassed (not selectable). This makes Discovery the single
        // source of truth for routability — the post-hoc NodeBreakerOpen
        // bypass in tryCluster* is now belt-and-suspenders, not the primary
        // gate, so Discovery.Status() and the router can never disagree.
        if cluster != nil {
            cluster.MarkNodeBreakerOpen(nodeID)
        }
    }
}

// NodeBreakerOpen reports whether the per-node breaker for nodeID is open (RR5).
// tryCluster* consults this to bypass a tripped node instead of poisoning the
// whole cluster. False for unknown/empty nodeID (no failures recorded yet).
func (e *Engine) NodeBreakerOpen(nodeID string) bool {
    if nodeID == "" {
        return false
    }
    e.breakerMu.RLock()
    defer e.breakerMu.RUnlock()
    return e.nodeBreakerOpenLocked(nodeID)
}

// nodeBreakerOpenLocked is the lock-free inner of NodeBreakerOpen (R1 audit
// fix). The caller MUST hold e.breakerMu (RLock is fine): tryClusterLocked and
// tryClusterByPlatformLocked already run under breakerMu.RLock via
// decideLocked, so re-taking it inside NodeBreakerOpen was a redundant nested
// RLock — not a self-deadlock (Go allows recursive RLock), but it widened the
// reader's critical section unnecessarily and gave every pending writer a
// longer wait. Reading the map under the caller's existing lock keeps the
// snapshot consistent while shortening the held window.
func (e *Engine) nodeBreakerOpenLocked(nodeID string) bool {
    if nodeID == "" {
        return false
    }
    b, ok := e.nodeBreakers[nodeID]
    if !ok {
        return false
    }
    return b.State() == StateOpen
}

// NodeBreakerState returns the per-node breaker's state (RR5). Used by
// PublishBreakerStates to push each node's state to the /metrics gauge.
// StateClosed for unknown/empty nodeID (no failures recorded yet).
func (e *Engine) NodeBreakerState(nodeID string) CircuitBreakerState {
    if nodeID == "" {
        return StateClosed
    }
    e.breakerMu.RLock()
    b, ok := e.nodeBreakers[nodeID]
    e.breakerMu.RUnlock()
    if !ok {
        return StateClosed
    }
    return b.State()
}

func (e *Engine) Trip(backend, reason string) {
    // L1 fix: RLock to find breaker, then call Trip outside RLock. E8: breakers
    // live under breakerMu now.
    e.breakerMu.RLock()
    b, ok := e.breakers[backend]
    e.breakerMu.RUnlock()
    if ok {
        b.Trip(reason)
        slog.Warn("circuit breaker tripped", "backend", backend, "reason", reason)
        observability.RecordCircuitBreakerTrip(backend, reason)
        observability.UpdateCircuitBreakerState(backend, int(StateOpen))
    }
}

// snapshotWiring captures every inFlightMu/affinityMu/hardwareMu-owned field
// Decide needs, returning a routeSnapshot. E8: the hot path takes this in ONE
// pass (inFlightMu.RLock for the wiring fields, plus the two leaf pointer
// reads) and RELEASES before acquiring breakerMu — the two engine locks are
// never held simultaneously. cfg comes from context (already decoupled from
// the mutex), not from the snapshot.
func (e *Engine) snapshotWiring() routeSnapshot {
    e.inFlightMu.RLock()
    snap := routeSnapshot{
        localReady:          e.localReady,
        localInFlight:       e.localInFlight,
        localModels:         e.localModels,
        classifier:          e.classifier,
        heuristicClassifier: e.heuristicClassifier,
        adapterLookup:       e.adapterLookup,
        cluster:             e.cluster,
        localQueue:          e.localQueue,
    }
    e.inFlightMu.RUnlock()
    e.affinityMu.RLock()
    snap.sessionAffinity = e.sessionAffinity
    e.affinityMu.RUnlock()
    e.hardwareMu.RLock()
    snap.hwCollector = e.hwCollector
    e.hardwareMu.RUnlock()
    return snap
}

func (e *Engine) Decide(ctx context.Context, req *RouteRequest) *RouteDecision {
    cfg := config.SnapshotFromContext(ctx)

    // B1: classify intent OUTSIDE any engine lock. The LLM classifier does a
    // blocking HTTP call with a 2s timeout; running it under a lock starves
    // every engine writer. E8: snapshot ALL wiring (classifiers + the rest)
    // under inFlightMu/leaf locks in one pass, release, run classification
    // lock-free, then take breakerMu for the dispatch. The IntentResult is
    // threaded back into decideLocked.
    snap := e.snapshotWiring()
    intentResult := e.classifyIntentUnlocked(ctx, cfg, req, snap.classifier, snap.heuristicClassifier)

    // L1 fix: collect trip reasons during RLock, apply Trip after unlock. E8:
    // decideLocked runs under breakerMu (breaker reads); wiring reads come
    // from the pre-captured snap, so breakerMu is the ONLY lock held here.
    var trips []string
    e.breakerMu.RLock()
    decision := e.decideLocked(ctx, cfg, req, &trips, intentResult, snap)
    e.breakerMu.RUnlock()

    // Apply deferred trip calls outside read lock (use e.Trip for proper locking)
    for _, reason := range trips {
        e.Trip("local", reason)
    }
    return decision
}

func (e *Engine) decideLocked(ctx context.Context, cfg *config.ConfigSnapshot, req *RouteRequest, trips *[]string, intent *IntentResult, snap routeSnapshot) *RouteDecision {

    // Fast path: embedding/rerank request type routing
    switch req.Type {
    case RequestTypeEmbedding:
        return e.decideEmbeddingLocked(ctx, cfg, req.Model, snap)
    case RequestTypeRerank:
        return e.decideRerankLocked(ctx, cfg, req.Model, snap)
    }

    // Mode fast-path: explicit routing mode overrides all other rules
    mode := cfg.Config.Routing.Mode
    switch mode {
    case "local":
        slog.Info("routing mode: local, forcing all requests to local backend")
        return &RouteDecision{Backend: LocalBackend, Reason: "mode_local"}
    case "cloud":
        if decision := e.tryClusterLocked(cfg, req.Model, snap); decision != nil {
            return decision
        }
        slog.Info("routing mode: cloud, forcing all requests to cloud backend")
        return &RouteDecision{Backend: CloudBackend, Reason: "mode_cloud"}
    case "hybrid":
        // fall through to existing priority-chain logic
    }

    // P-1: D4 semantic intent layer (issue #22/#23/#25). Runs before the
    // rule chain so semantic dispatch wins, with the rule chain as fallback.
    // Disabled by default (intent_classifier.enabled); NoopClassifier makes
    // this a no-op so existing P0-P7 behavior is unchanged.
    // B1: the intent classification itself (heuristic + LLM HTTP call) runs
    // lock-free in classifyIntentUnlocked before Decide takes the RLock; this
    // branch consumes the precomputed result and only does lock-protected
    // dispatch (tryCluster*Locked).
    if decision := e.decideIntentLocked(ctx, cfg, req, intent, snap); decision != nil {
        return decision
    }

    // P0: Circuit breaker check — local
    if e.breakers["local"].State() == StateOpen {
        if decision := e.tryClusterLocked(cfg, req.Model, snap); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "circuit_breaker_open"}
    }

    // P0.3: Session affinity — same space_id routes to same provider
    if req.SpaceID != "" && snap.sessionAffinity != nil {
        if providerName, ok := snap.sessionAffinity.Lookup(req.SpaceID); ok {
            slog.Info("session affinity hit", "space_id", req.SpaceID, "provider", providerName)
            switch providerName {
            case "fusion-mlx":
                if e.breakers["local"].State() != StateOpen && snap.localReady {
                    return &RouteDecision{Backend: LocalBackend, Reason: "session_affinity:local"}
                }
                slog.Warn("session affinity target unavailable (local breaker open or not ready), re-routing", "space_id", req.SpaceID)
            default:
                return &RouteDecision{Backend: CloudBackend, Reason: "session_affinity:" + providerName, CloudTarget: providerName}
            }
        }
    }

    // P0.5: Hardware metrics collection error
    hwMetrics := snap.hwCollector.Latest()
    if hwMetrics.CollectionError != nil && cfg.Config.Hardware.CollectionErrorProtection {
        slog.Error("hardware metrics collection error, refusing local routing",
            "error", hwMetrics.CollectionError)
        return e.cloudOrFallbackLocked(cfg, req, snap, &RouteDecision{Backend: CloudBackend, Reason: "metrics_collection_error"})
    }

    // P1: System memory overload
    if hwMetrics.MemoryUsedRatio > cfg.Config.Routing.LocalPriority.MaxSystemMemoryRatio {
        *trips = append(*trips, "memory_overload")
        return e.cloudOrFallbackLocked(cfg, req, snap, &RouteDecision{Backend: CloudBackend, Reason: "memory_overload"})
    }

    // P1.5: MLX memory overload (independent check)
    if hwMetrics.MLXActiveMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
        mlxRatio := float64(hwMetrics.MLXActiveMemory) / float64(hwMetrics.GPUInUseMemory)
        if mlxRatio > cfg.Config.Routing.LocalPriority.MaxMLXMemoryRatio {
            return e.cloudOrFallbackLocked(cfg, req, snap, &RouteDecision{Backend: CloudBackend, Reason: "mlx_memory_overload"})
        }
    }

    // P2: Swap page rate
    swapThreshold := cfg.Config.Routing.LocalPriority.SwapPageRateThreshold
    if hwMetrics.SwapPageInRate > swapThreshold || hwMetrics.SwapPageOutRate > swapThreshold {
        *trips = append(*trips, "swap_thrashing")
        return e.cloudOrFallbackLocked(cfg, req, snap, &RouteDecision{Backend: CloudBackend, Reason: "swap_thrashing"})
    }

    // P2.5: GPU memory low
    if hwMetrics.GPUAllocMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
        gpuAvail := hwMetrics.GPUAllocMemory - hwMetrics.GPUInUseMemory
        if gpuAvail < uint64(float64(hwMetrics.GPUAllocMemory)*0.2) {
            return e.cloudOrFallbackLocked(cfg, req, snap, &RouteDecision{Backend: CloudBackend, Reason: "gpu_memory_low"})
        }
    }

    // P3: Local not ready
    if !snap.localReady {
        return e.cloudOrFallbackLocked(cfg, req, snap, &RouteDecision{Backend: CloudBackend, Reason: "local_not_ready"})
    }

    // P3.5: Local-exclusive model guard (issue #83)
    // A model present in the local model set but absent from
    // routing.fallback.model_mapping is local-exclusive: no cloud
    // backend serves it, so a P4/P4.5 token/ratio cloud-divert would
    // route it to a cloud that rejects the model name with 400
    // ("Invalid model name ..."). Short-circuit to local before the
    // token/ratio rules. P0-P2 hardware/breaker cloud-diverts stay
    // upstream; under those, a local-exclusive 400 is a separate
    // overload condition (rare). When model_mapping is disabled or
    // empty, every model in the local set is treated as local-exclusive.
    if localModels := snap.localModels(); localModels != nil {
        if localModels[req.Model] {
            mapping := cfg.Config.Routing.Fallback.ModelMapping
            if !cfg.Config.Routing.Fallback.Enabled || mapping == nil {
                slog.Info("local-exclusive model, forcing local",
                    "model", req.Model,
                    "reason", "model_mapping disabled, cloud cannot serve",
                )
                return &RouteDecision{Backend: LocalBackend, Reason: "local_exclusive_model"}
            }
            if _, mapped := mapping[req.Model]; !mapped {
                slog.Info("local-exclusive model, forcing local",
                    "model", req.Model,
                    "reason", "not in model_mapping, cloud cannot serve",
                )
                return &RouteDecision{Backend: LocalBackend, Reason: "local_exclusive_model"}
            }
        }
    }

    // P4: Token budget exceeded
    budget, ok := tokenizer.BudgetFromContext(ctx)
    if !ok {
        slog.Warn("token budget not found in context, defaulting to cloud")
        return e.cloudOrLocalLocked(req, &RouteDecision{Backend: CloudBackend, Reason: "token_budget_missing"})
    }
    // L4 fix: zero-token (empty prompt) should route local, not waste cloud quota
    if budget.InputTokens == 0 {
        slog.Debug("empty prompt, routing to local")
        return &RouteDecision{Backend: LocalBackend, Reason: "empty_prompt_local"}
    }

    if budget.InputTokens > cfg.Config.Routing.TokenThreshold {
        decision := &RouteDecision{Backend: CloudBackend, Reason: "token_budget_exceeded"}
        if cfg.Config.Routing.TokenTiers.Enabled {
            decision.CloudTarget = resolveCloudByTier(budget, cfg.Config.Routing.TokenTiers)
            if decision.CloudTarget != "" {
                decision.Reason = "token_budget_exceeded:tier:" + decision.CloudTarget
                slog.Info("token tier matched",
                    "cloud_target", decision.CloudTarget,
                    "input_tokens", budget.InputTokens,
                    "total_budget", budget.TotalBudget,
                )
            }
        }
        return e.cloudOrLocalLocked(req, decision)
    }

    // P4.5: Output/Input ratio routing
    // Skip the ratio check for tiny requests: with very few input tokens the
    // predicted-output/input ratio is statistically meaningless (e.g. 5/4=1.25
    // for a 4-token "say pong" prompt) and would misroute local-eligible
    // requests to cloud (issue #48). Guard with a minimum input-token floor.
    minInputTokens := cfg.Config.Routing.OutputInputRatioMinInputTokens
    if minInputTokens <= 0 {
        minInputTokens = defaultRatioMinInputTokens
    }
    if budget.InputTokens > 0 && budget.InputTokens < minInputTokens {
        slog.Info("output/input ratio skipped: input tokens below floor",
            "input_tokens", budget.InputTokens,
            "min_input_tokens", minInputTokens,
            "predict_output_tokens", budget.PredictOutputTokens,
        )
    } else if budget.InputTokens > 0 {
        ratio := float64(budget.PredictOutputTokens) / float64(budget.InputTokens)

        // P4.5a: RatioTiers — segment routing by ratio to different backends
        if cfg.Config.Routing.RatioTiers.Enabled && len(cfg.Config.Routing.RatioTiers.Rules) > 0 {
            target := resolveCloudByRatio(ratio, cfg.Config.Routing.RatioTiers)
            if target != "" {
                slog.Info("ratio tier matched, routing to cloud",
                    "ratio", fmt.Sprintf("%.2f", ratio),
                    "cloud_target", target,
                    "input_tokens", budget.InputTokens,
                    "predict_output_tokens", budget.PredictOutputTokens,
                )
                return e.cloudOrLocalLocked(req, &RouteDecision{Backend: CloudBackend, Reason: "ratio_tier_matched", CloudTarget: target})
            }
        }

        // P4.5b: Fallback single threshold (when RatioTiers disabled)
        ratioThreshold := cfg.Config.Routing.OutputInputRatioThreshold
        if ratioThreshold > 0 && ratio > ratioThreshold {
            slog.Info("output/input ratio exceeded, routing to cloud",
                "ratio", fmt.Sprintf("%.2f", ratio),
                "threshold", fmt.Sprintf("%.2f", ratioThreshold),
                "input_tokens", budget.InputTokens,
                "predict_output_tokens", budget.PredictOutputTokens,
            )
            return e.cloudOrLocalLocked(req, &RouteDecision{Backend: CloudBackend, Reason: "output_input_ratio_exceeded"})
        }
    }

    // P5: Concurrent limit (advisory early-shed — B2 / RR4).
    // This is a TOCTOU soft read: localInFlight is read here under RLock, but
    // the counter is incremented later in the adapter (fusion_mlx.go
    // tryInFlightAcquire) inside Chat/StreamChat/Embedding/Rerank, after
    // Decide returns. N concurrent Decide calls can each observe
    // inFlight == maxConcurrent-1 and all route local, overshooting by up to
    // N-1 before any of them increments the counter. RR4 closes that window:
    // the adapter's tryInFlightAcquire is now an atomic CAS hard cap — when
    // local is actually full it returns ErrLocalSlotFull and the handler
    // diverts the request to cloud (no breaker failure recorded). So this P5
    // read is now purely an advisory early-shed: it diverts to cluster/cloud
    // BEFORE reaching the adapter when local already LOOKS saturated, saving
    // a round-trip. Any overshoot that slips past P5 is caught and corrected
    // by the adapter CAS + cloud diversion. The opt-in slotQueue (#102
    // ADR-001, mode=local + queue_enabled) remains a separate handler-side
    // gate that blocks/queues instead of diverting.
    maxConcurrent := cfg.Config.Routing.LocalPriority.MaxConcurrent
    inFlightNow := snap.localInFlight()
    if maxConcurrent > 0 && inFlightNow >= int64(maxConcurrent) {
        slog.Debug("advisory concurrent limit reached, diverting from local",
            "in_flight", inFlightNow, "max_concurrent", maxConcurrent, "model", req.Model)
        if decision := e.tryClusterLocked(cfg, req.Model, snap); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "concurrent_limit"}
    }

    // P6: Model availability check
    if localModels := snap.localModels(); localModels != nil {
        if !localModels[req.Model] {
            // P6.5: Context window fallback — check if a larger model can serve this
            if fallback, ok := cfg.Config.Routing.Fallback.ContextWindowFallback[req.Model]; ok {
                if localModels[fallback] {
                    slog.Info("context window fallback: using larger model",
                        "original", req.Model,
                        "fallback", fallback,
                    )
                    return &RouteDecision{Backend: LocalBackend, Reason: "context_window_fallback:" + fallback}
                }
            }
            if decision := e.tryClusterLocked(cfg, req.Model, snap); decision != nil {
                return decision
            }
            return &RouteDecision{Backend: CloudBackend, Reason: "model_not_available_locally"}
        }
    }

    // P7: Local priority (default)
    return &RouteDecision{Backend: LocalBackend, Reason: "local_priority"}
}

func (e *Engine) decideEmbeddingLocked(ctx context.Context, cfg *config.ConfigSnapshot, model string, snap routeSnapshot) *RouteDecision {
    // Mode fast-path for embedding
    mode := cfg.Config.Routing.Mode
    switch mode {
    case "local":
        slog.Info("routing mode: local, forcing embedding to local backend")
        return &RouteDecision{Backend: LocalBackend, Reason: "mode_local:embedding"}
    case "cloud":
        if decision := e.tryClusterLocked(cfg, model, snap); decision != nil {
            return decision
        }
        slog.Info("routing mode: cloud, forcing embedding to cloud backend")
        return &RouteDecision{Backend: CloudBackend, Reason: "mode_cloud:embedding"}
    case "hybrid":
        // fall through
    }

    // Embedding: local-first if breaker closed + local ready
    if e.breakers["local"].State() == StateOpen || !snap.localReady {
        if decision := e.tryClusterLocked(cfg, model, snap); decision != nil {
            return decision
        }
        return &RouteDecision{Backend: CloudBackend, Reason: "embedding_local_unavailable"}
    }
    return &RouteDecision{Backend: LocalBackend, Reason: "embedding_local_priority"}
}

func (e *Engine) decideRerankLocked(ctx context.Context, cfg *config.ConfigSnapshot, model string, snap routeSnapshot) *RouteDecision {
    // Mode fast-path for rerank
    mode := cfg.Config.Routing.Mode
    switch mode {
    case "local":
        slog.Info("routing mode: local, forcing rerank to local backend")
        return &RouteDecision{Backend: LocalBackend, Reason: "mode_local:rerank"}
    case "cloud":
        if decision := e.tryClusterLocked(cfg, model, snap); decision != nil {
            return decision
        }
        slog.Info("routing mode: cloud, forcing rerank to cloud backend")
        return &RouteDecision{Backend: CloudBackend, Reason: "mode_cloud:rerank"}
    case "hybrid":
        // fall through
    }

    // Rerank: typically cloud-only unless local model available
    localModels := snap.localModels()
    if snap.localReady && localModels != nil {
        for model := range localModels {
            if isRerankModel(model) {
                if e.breakers["local"].State() != StateOpen {
                    return &RouteDecision{Backend: LocalBackend, Reason: "rerank_local_available"}
                }
            }
        }
    }

    if decision := e.tryClusterLocked(cfg, model, snap); decision != nil {
        return decision
    }
    return &RouteDecision{Backend: CloudBackend, Reason: "rerank_cloud_default"}
}

func isRerankModel(model string) bool {
    for _, sub := range []string{"rerank", "reranker", "bge-rerank", "cohere-rerank"} {
        if strings.Contains(model, sub) {
            return true
        }
    }
    return false
}

func (e *Engine) tryClusterLocked(cfg *config.ConfigSnapshot, model string, snap routeSnapshot) *RouteDecision {
    if snap.cluster == nil || !cfg.Config.Cluster.Enabled {
        return nil
    }

    if e.breakers["cluster"].State() == StateOpen {
        slog.Debug("cluster breaker open, skipping cluster fallback")
        return nil
    }

    if snap.cluster.HealthyNodes() == 0 {
        slog.Debug("no healthy cluster nodes available")
        return nil
    }

    strategy := cfg.Config.Cluster.LoadBalancer
    if strategy == "" {
        strategy = "least-connections"
    }

    nodeID, err := snap.cluster.SelectNodeByModel(strategy, model, cfg.Config.Routing.LocalPriority.MaxConcurrent)
    if err != nil {
        slog.Debug("cluster has no node serving model below slot cap, falling back to cloud",
            "model", model, "strategy", strategy, "error", err)
        return nil
    }

    // RR5: bypass a node whose per-node breaker is open — it tripped itself on
    // repeated failure, so routing to it would just fail again. Fall through to
    // cloud for this request; the node's health-check will reconcile its state.
    // R1: caller (decideLocked) already holds breakerMu.RLock, so use the
    // lock-free variant instead of re-entering the mutex.
    if e.nodeBreakerOpenLocked(nodeID) {
        slog.Info("cluster node breaker open, bypassing to cloud",
            "node_id", nodeID, "model", model)
        return nil
    }

    slog.Info("routing to cluster node",
        "node_id", nodeID, "strategy", strategy, "model", model)
    return &RouteDecision{Backend: ClusterBackend, Reason: "cluster_fallback", NodeID: nodeID}
}

// cloudOrLocalLocked returns the caller-built cloud decision when the cloud breaker
// is closed (cloud reachable); when the cloud breaker is OPEN (cloud known-down) it
// falls back to local instead of returning a cloud decision that would 502 against a
// dead upstream (RC2). No cluster attempt — for request-size rules (P4/P4.5) where a
// cluster node is the same memory class as local and cannot relieve an oversized
// request. reason/CloudTarget from cloudDecision are preserved on the cloud path; the
// local-fallback path appends a _local_fallback suffix for tracing.
// Called inside decideLocked under breakerMu.RLock — uses the lock-free breaker read.
func (e *Engine) cloudOrLocalLocked(req *RouteRequest, cloudDecision *RouteDecision) *RouteDecision {
    if e.breakers["cloud"].State() != StateOpen {
        return cloudDecision
    }
    slog.Warn("cloud breaker open, falling back to local",
        "original_reason", cloudDecision.Reason, "model", req.Model)
    return &RouteDecision{Backend: LocalBackend, Reason: cloudDecision.Reason + "_local_fallback"}
}

// cloudOrFallbackLocked is the cloud-return helper for local-health rules
// (P0.5/P1/P1.5/P2/P2.5/P3): it preserves the existing cluster-first preference
// (another healthy local node beats cloud), then delegates to cloudOrLocalLocked for
// the cloud-vs-local decision. Local-health rules degrade local specifically, so a
// peer cluster node is a valid intermediate escape — unlike request-size rules.
func (e *Engine) cloudOrFallbackLocked(cfg *config.ConfigSnapshot, req *RouteRequest, snap routeSnapshot, cloudDecision *RouteDecision) *RouteDecision {
    if decision := e.tryClusterLocked(cfg, req.Model, snap); decision != nil {
        return decision
    }
    return e.cloudOrLocalLocked(req, cloudDecision)
}

// tryClusterByPlatformLocked attempts to route to a cluster node on a specific
// platform (D4 dispatch-by-platform, issue #23/#25). Returns nil if cluster is
// disabled, the breaker is open, or no healthy node on that platform — the
// caller then falls back to the rule chain or cloud.
func (e *Engine) tryClusterByPlatformLocked(cfg *config.ConfigSnapshot, platform string, snap routeSnapshot) *RouteDecision {
    if snap.cluster == nil || !cfg.Config.Cluster.Enabled || platform == "" {
        return nil
    }
    if e.breakers["cluster"].State() == StateOpen {
        slog.Debug("cluster breaker open, skipping platform cluster dispatch", "platform", platform)
        return nil
    }
    if snap.cluster.HealthyNodesByPlatform(platform) == 0 {
        slog.Debug("no healthy cluster nodes on platform", "platform", platform)
        return nil
    }
    strategy := cfg.Config.Cluster.LoadBalancer
    if strategy == "" {
        strategy = "least-connections"
    }
    nodeID, err := snap.cluster.SelectNodeByPlatform(strategy, platform)
    if err != nil {
        slog.Warn("platform cluster node selection failed", "platform", platform, "error", err)
        return nil
    }
    // RR5: bypass a node whose per-node breaker is open (see tryClusterLocked).
    // R1: caller (decideLocked) already holds breakerMu.RLock — use the
    // lock-free variant instead of re-entering the mutex.
    if e.nodeBreakerOpenLocked(nodeID) {
        slog.Info("cluster node breaker open, bypassing platform dispatch to cloud",
            "node_id", nodeID, "platform", platform)
        return nil
    }
    slog.Info("routing to cluster node by platform", "node_id", nodeID, "platform", platform, "strategy", strategy)
    return &RouteDecision{Backend: ClusterBackend, Reason: "cluster_platform:" + platform, NodeID: nodeID}
}

// classifyIntentUnlocked runs the heuristic and (if enabled) the LLM intent
// classifier LOCK-FREE. B1: the LLM classifier is a blocking HTTP call (2s
// timeout); doing it under e.mu.RLock starved every engine writer (breaker
// transitions, config updates) for the duration of one slow classification.
// The caller snapshots e.classifier / e.heuristicClassifier under RLock and
// passes the pointer values here, so a concurrent SetIntentClassifier cannot
// race the read (the classifier it points at remains valid — implementations
// are safe for concurrent use per the interface doc). Returns the IntentResult
// the dispatch side should act on, or nil to defer to the rule chain. No
// shared engine state is read here; the adapter lookup is consulted under lock
// in decideIntentLocked.
func (e *Engine) classifyIntentUnlocked(ctx context.Context, cfg *config.ConfigSnapshot, req *RouteRequest, llmClassifier, heuristicClassifier IntentClassifier) *IntentResult {
    // Heuristic-first: the in-process sub-ms classifier runs before the LLM
    // classifier on every request (latency lever for <20ms gateway overhead).
    // When it returns IntentCode with sufficient confidence, it short-circuits
    // the sync LLM classifier entirely (the dominant latency killer on the
    // code path). The result is tagged so the dispatch side logs the heuristic
    // source label rather than the LLM one.
    hc := cfg.Config.Routing.HeuristicClassifier
    if hc.Enabled && heuristicClassifier != nil {
        hRes := classifyAndLog(ctx, heuristicClassifier, req)
        if hRes.Intent == IntentCode && hRes.Confidence >= hc.MinConfidence {
            if hRes.Params == nil {
                hRes.Params = map[string]string{}
            }
            hRes.Params["_source"] = "heuristic"
            return hRes
        }
        if hRes.Intent != IntentUnknown {
            slog.Debug("heuristic classified non-code intent, falling through to LLM classifier/rule chain",
                "intent", hRes.Intent, "confidence", hRes.Confidence, "model", req.Model)
        }
    }

    ic := cfg.Config.Routing.IntentClassifier
    if !ic.Enabled {
        return nil
    }
    classifier := llmClassifier
    if classifier == nil {
        classifier = NoopClassifier{}
    }
    res := classifyAndLog(ctx, classifier, req)
    if res.Intent == IntentUnknown {
        return nil
    }
    if res.Confidence < ic.MinConfidence {
        slog.Info("intent confidence below threshold, deferring to rule chain",
            "intent", res.Intent, "confidence", res.Confidence, "min", ic.MinConfidence)
        return nil
    }
    if res.Params == nil {
        res.Params = map[string]string{}
    }
    res.Params["_source"] = "llm"
    return res
}

// decideIntentLocked is the D4 semantic intent layer (issue #22/#23/#25).
// It runs before the P0-P7 rule chain. Returns nil to defer to the rule chain
// (intent unknown, low confidence, disabled, or no target platform available).
// B1: the intent classification (heuristic + LLM HTTP) now runs lock-free in
// classifyIntentUnlocked before Decide takes any lock; this method only does
// dispatch (adapter lookup + tryCluster*Locked) from the precomputed result.
// E8: called under breakerMu.RLock — breaker reads from e.breakers, wiring
// (adapterLookup, cluster) from the pre-captured snap. No e.mu.
func (e *Engine) decideIntentLocked(ctx context.Context, cfg *config.ConfigSnapshot, req *RouteRequest, res *IntentResult, snap routeSnapshot) *RouteDecision {
    if res == nil {
        return nil
    }

    // IntentLightweight: prefer Mac local. Don't force — let the rule chain
    // apply hardware/circuit-breaker protections; it already routes to local
    // when healthy. So defer (return nil) for lightweight.
    if res.Intent == IntentLightweight {
        return nil
    }

    source := res.Params["_source"]

    // IntentCode (heuristic or LLM path): dispatch to LocalBackend + LoRA
    // hot-swap using the configured code_adapter. The adapter comes from the
    // classifier Params (if it set one) or the heuristic config fallback. With
    // no adapter configured, defer to the rule chain (bare base).
    if res.Intent == IntentCode {
        adapter := res.Params["code_adapter"]
        if adapter == "" {
            adapter = cfg.Config.Routing.HeuristicClassifier.CodeAdapter
        }
        if adapter == "" {
            slog.Info(source+": code intent but no code_adapter configured, deferring to rule chain",
                "confidence", res.Confidence, "model", req.Model)
            return nil
        }
        // Best-effort adapter validation (heuristic path only — the LLM path
        // preserved the original behavior of no pre-dispatch index check): if
        // an adapter index is wired and does not list this adapter, log a
        // warning but still dispatch — the index may be stale or not yet
        // refreshed, and suppressing a valid code intent is worse than a
        // possibly-missing adapter (fusion-mlx will error on hot-swap if truly
        // absent).
        if source == "heuristic" && snap.adapterLookup != nil && !snap.adapterLookup.Has(adapter) {
            slog.Warn("heuristic: code_adapter not found in adapter index, dispatching anyway (index may be stale)",
                "adapter", adapter, "model", req.Model)
        }
        // Resolve the bare adapter name to the absolute adapter directory path
        // that fusion-mlx's per-request "adapters" field requires (a bare name
        // is rejected with AdapterPathError). When the index has no path (nil
        // lookup or stale entry), fall back to the bare name so dispatch still
        // proceeds (best-effort); fusion-mlx surfaces the error if the adapter
        // is truly unresolvable.
        adapterPath := resolveAdapterPath(snap.adapterLookup, adapter)
        slog.Info(source+": code intent routed to local + lora hot-swap",
            "adapter", adapter, "adapter_path", adapterPath,
            "confidence", res.Confidence, "model", req.Model)
        return &RouteDecision{
            Backend: LocalBackend,
            Reason:  "intent:code:lora:" + adapter,
            Adapter: adapterPath,
        }
    }

    // Heavy model / diffusion: dispatch to the target platform's cluster node
    // (Windows CUDA by default). Fall back to cloud if no platform node.
    pr := cfg.Config.Cluster.PlatformRouting
    platform := PlatformForIntent(res.Intent, pr.HeavyModelPlatform, pr.DiffusionPlatform)
    if platform == "" {
        return nil
    }
    if decision := e.tryClusterByPlatformLocked(cfg, platform, snap); decision != nil {
        decision.Reason = "intent:" + string(res.Intent) + ":" + decision.Reason
        return decision
    }
    // No healthy node on target platform — fall back to cloud, with a reason
    // that records the original intent for observability.
    slog.Info("no cluster node on target platform, falling back to cloud",
        "intent", res.Intent, "platform", platform)
    return &RouteDecision{Backend: CloudBackend, Reason: "intent:" + string(res.Intent) + ":no_platform_node"}
}

// resolveAdapterPath maps a bare LoRA adapter name (the configured
// code_adapter, e.g. "lora-code") to the absolute adapter directory path that
// fusion-mlx's per-request "adapters" field requires. fusion-mlx rejects a
// bare name with AdapterPathError; the full path (sourced from the adapter
// index's adapter_path field, populated by fusion-mlx's
// GET /admin/api/fine-tune/adapters) is what hot-swap consumes.
//
// Best-effort: a nil lookup (no index wired) or an absent/stale entry returns
// the bare name unchanged. Dispatch still proceeds so a stale index never
// suppresses a valid code intent; fusion-mlx surfaces the hot-swap error if
// the adapter is truly unresolvable, and the index refreshes on its schedule.
func resolveAdapterPath(lookup AdapterLookup, name string) string {
    if lookup == nil || name == "" {
        return name
    }
    if path, ok := lookup.Path(name); ok {
        return path
    }
    slog.Warn("adapter path not resolved from index, dispatching bare name",
        "adapter", name)
    return name
}

func resolveCloudByTier(budget tokenizer.TokenBudget, tier config.TokenTierConfig) string {
    var tokenValue int
    switch tier.Metric {
    case "input":
        tokenValue = budget.InputTokens
    case "output":
        tokenValue = budget.PredictOutputTokens
    default:
        tokenValue = budget.TotalBudget
    }

    slog.Debug("resolving cloud by token tier",
        "metric", tier.Metric,
        "token_value", tokenValue,
        "rules_count", len(tier.Rules),
    )

    for _, rule := range tier.Rules {
        if rule.MaxTokens == 0 || tokenValue <= rule.MaxTokens {
            slog.Debug("token tier rule matched",
                "max_tokens", rule.MaxTokens,
                "backend", rule.Backend,
                "token_value", tokenValue,
            )
            return rule.Backend
        }
    }

    slog.Debug("no token tier rule matched", "token_value", tokenValue)
    return ""
}

func resolveCloudByRatio(ratio float64, rt config.RatioTierConfig) string {
    slog.Debug("resolving cloud by ratio tier",
        "ratio", ratio,
        "rules_count", len(rt.Rules),
    )

    for _, rule := range rt.Rules {
        if ratio <= rule.MaxRatio {
            slog.Debug("ratio tier rule matched",
                "max_ratio", rule.MaxRatio,
                "backend", rule.Backend,
                "ratio", ratio,
            )
            return rule.Backend
        }
    }

    slog.Debug("no ratio tier rule matched, ratio exceeds all tiers", "ratio", ratio)
    return ""
}