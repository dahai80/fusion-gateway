package browser

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "sync"
)

// Proxy is the browser-session coordinator: it owns the in-memory session→node
// pin map and the five op handlers (Create/Execute/Close/Metrics/Nodes). It
// holds *Registry + *NodeClient + *Scheduler. The pin map is in-memory only
// — a non-persistent browser engine means a persisted pin would point at a
// dead session after any browser restart, a false guarantee. On gateway
// restart all pins reset; clients recreate sessions (honest for a non-
// persistent system).
type Proxy struct {
    registry *Registry
    client   *NodeClient
    scheduler *Scheduler

    mu   sync.Mutex // one owner for the pin map (E8)
    pins map[string]string // sessionID -> nodeID (stable config label)
}

// NewProxy binds a proxy to its registry/client/scheduler. Callers register
// the HTTP routes via the Handler (handler.go), not here.
func NewProxy(reg *Registry, client *NodeClient, sched *Scheduler) *Proxy {
    return &Proxy{
        registry:  reg,
        client:    client,
        scheduler: sched,
        pins:      make(map[string]string),
    }
}

// CreateResult is what Create returns to the HTTP handler: the session id the
// node minted, the config-label node id it was pinned to, and whether the node
// injected a credential. The handler serializes this as the 201 body.
type CreateResult struct {
    SessionID          string `json:"session_id"`
    NodeID             string `json:"node_id"`
    CredentialInjected bool   `json:"credential_injected"`
}

// Create runs the placement + forward + pin flow for a create_session:
//  1. Scheduler.Pick → nodeID or a 503 sentinel (quota/headroom).
//  2. NodeClient.Create → forward to the chosen node, decode session_id.
//  3. Record pin sessionID → nodeID.
// A node FBError is returned as a *NodeError the handler relays with the
// node's own code/retryable (never coerced to 502 — RC1 lesson).
func (p *Proxy) Create(ctx context.Context, req *CreateSessionRequest) (*CreateResult, error) {
    nodeID, err := p.scheduler.Pick()
    if err != nil {
        return nil, err
    }
    socket, ok := p.registry.SocketOf(nodeID)
    if !ok {
        // Picked a node that vanished between pick + lookup (hot-reload race).
        // Surface as no_headroom so the caller retries against the fresh set.
        slog.Warn("browser create: picked node vanished before forward",
            "node", nodeID)
        return nil, ErrNoNodeHeadroom
    }
    token, _ := p.registry.TokenOf(nodeID) // node exists (SocketOf ok) → token known
    resp, err := p.client.Create(ctx, socket, token, req)
    if err != nil {
        return nil, err
    }
    p.mu.Lock()
    p.pins[resp.SessionID] = nodeID
    p.mu.Unlock()
    slog.Info("browser session created + pinned",
        "session", resp.SessionID, "node", nodeID, "socket", socket)
    return &CreateResult{
        SessionID:          resp.SessionID,
        NodeID:             nodeID,
        CredentialInjected: resp.CredentialInjected,
    }, nil
}

// ExecuteResult is the raw state response the proxy relays verbatim. The
// handler writes the payload bytes as-is (no re-encode — schema-drift safe).
type ExecuteResult struct {
    Payload json.RawMessage
}

// Execute runs the pin lookup + forward flow for an execute action:
//  1. Pin lookup by sessionID — miss → 404 session_not_found.
//  2. Pin node dead → 503 session_lost (in-flight session on dead node is
//     LOST per contract; surface honestly, do not retry internally).
//  3. NodeClient.Execute → forward the action, relay state response verbatim.
// A node FBError is returned as *NodeError for the handler to relay.
func (p *Proxy) Execute(ctx context.Context, req *BrowserActionRequest) (*ExecuteResult, error) {
    nodeID, ok := p.lookupPin(req.SessionID)
    if !ok {
        return nil, &OpError{Code: ErrCodeSessionNotFound, Message: "session unknown to this gateway (created on a stale gateway or pre-existing)", Retryable: false, HTTPStatus: 404}
    }
    if p.registry.IsDead(nodeID) {
        // Best-effort: evict the dead pin so a repeat does not re-hit it.
        p.evictPin(req.SessionID)
        return nil, &OpError{Code: "session_lost", Message: "pinned node is dead; in-flight session is lost", Retryable: true, HTTPStatus: 503}
    }
    socket, ok := p.registry.SocketOf(nodeID)
    if !ok {
        p.evictPin(req.SessionID)
        return nil, &OpError{Code: "session_lost", Message: "pinned node removed from registry", Retryable: true, HTTPStatus: 503}
    }
    token, _ := p.registry.TokenOf(nodeID)
    resp, err := p.client.Execute(ctx, socket, token, req)
    if err != nil {
        return nil, err
    }
    return &ExecuteResult{Payload: resp.Payload}, nil
}

// Close runs the pin lookup + forward + evict flow for a close. Close is
// idempotent: a pin miss still returns success (204) because the session is
// already gone from the gateway's view (a repeat close, or a close of a
// session created elsewhere). A dead pin is best-effort forwarded (the node
// may already be gone) then the pin is evicted regardless of the outcome —
// the session is gone either way.
func (p *Proxy) Close(ctx context.Context, sessionID string) error {
    nodeID, ok := p.lookupPin(sessionID)
    if !ok {
        // No pin = nothing to close from the gateway's view. Idempotent 204.
        slog.Debug("browser close: no pin for session (idempotent)", "session", sessionID)
        return nil
    }
    socket, socketOK := p.registry.SocketOf(nodeID)
    if socketOK && !p.registry.IsDead(nodeID) {
        token, _ := p.registry.TokenOf(nodeID)
        if err := p.client.Close(ctx, socket, token, sessionID); err != nil {
            // Log but do not fail the close: the session is gone from the
            // gateway's view, and close is idempotent. A node error here is
            // observability, not a caller-facing failure.
            if ne := AsNodeError(err); ne != nil {
                slog.Warn("browser close: node returned error (evicting pin anyway)",
                    "session", sessionID, "node", nodeID, "code", ne.Code, "message", ne.Message)
            } else {
                slog.Warn("browser close: forward failed (evicting pin anyway)",
                    "session", sessionID, "node", nodeID, "error", err)
            }
        }
    } else {
        slog.Debug("browser close: pinned node dead/removed, evicting pin",
            "session", sessionID, "node", nodeID)
    }
    p.evictPin(sessionID)
    return nil
}

// MetricsResult is the raw metrics response the proxy relays verbatim to the
// admin handler. Counters/latency are opaque [name,value] pairs the gateway
// does not interpret.
type MetricsResult struct {
    Payload json.RawMessage
}

// Metrics forwards a metrics query to a chosen node. If nodeID is empty, the
// proxy picks the first live node from the registry snapshot (round-robin is
// unnecessary for an admin read of a single node). Returns the raw payload.
func (p *Proxy) Metrics(ctx context.Context, nodeID string) (*MetricsResult, error) {
    if nodeID == "" {
        nodeID = p.firstLiveNode()
        if nodeID == "" {
            return nil, &OpError{Code: "no_live_node", Message: "no live browser node to query metrics", Retryable: true, HTTPStatus: 503}
        }
    }
    socket, ok := p.registry.SocketOf(nodeID)
    if !ok {
        return nil, &OpError{Code: ErrCodeSessionNotFound, Message: "unknown node id", Retryable: false, HTTPStatus: 404}
    }
    if p.registry.IsDead(nodeID) {
        return nil, &OpError{Code: "node_unreachable", Message: "selected node is dead", Retryable: true, HTTPStatus: 503}
    }
    token, _ := p.registry.TokenOf(nodeID)
    resp, err := p.client.Metrics(ctx, socket, token)
    if err != nil {
        return nil, err
    }
    return &MetricsResult{Payload: resp.Payload}, nil
}

// firstLiveNode returns the config id of the first live node in the registry
// snapshot, or "" if none. Deterministic (snapshot is sorted by id) so the
// same node is chosen until the set changes.
func (p *Proxy) firstLiveNode() string {
    for _, v := range p.registry.Snapshot() {
        if v.State == NodeStateLive {
            return v.NodeID
        }
    }
    return ""
}

// lookupPin returns the node id a session is pinned to. Caller-unlocked read
// of the pin map under the proxy mutex.
func (p *Proxy) lookupPin(sessionID string) (string, bool) {
    p.mu.Lock()
    defer p.mu.Unlock()
    nodeID, ok := p.pins[sessionID]
    return nodeID, ok
}

// evictPin removes a session from the pin map. Called on close (always) and
// on dead-pin execute (best-effort cleanup so a repeat does not re-hit the
// dead node).
func (p *Proxy) evictPin(sessionID string) {
    p.mu.Lock()
    delete(p.pins, sessionID)
    p.mu.Unlock()
}

// OpError is a gateway-originated error the HTTP handler maps to a status +
// relayed code/message/retryable. Distinct from *NodeError (which carries a
// node's own FBError); OpError is the gateway's own decision (pin miss,
// dead pin, no live node). Both implement the errorRelay interface so the
// handler can extract code/retryable uniformly.
type OpError struct {
    Code       string
    Message    string
    Retryable  bool
    HTTPStatus int
}

func (e *OpError) Error() string {
    return fmt.Sprintf("browser op error %s: %s", e.Code, e.Message)
}

// errorRelay is the common shape the HTTP handler extracts from either an
// OpError (gateway-origin) or a NodeError (node-origin) to build the 503/404
// response body. Both ad-hoc satisfy it via the accessors below.
type errorRelay interface {
    relayCode() string
    relayMessage() string
    relayRetryable() bool
}

func (e *OpError) relayCode() string       { return e.Code }
func (e *OpError) relayMessage() string    { return e.Message }
func (e *OpError) relayRetryable() bool    { return e.Retryable }

func (e *NodeError) relayCode() string     { return e.Code }
func (e *NodeError) relayMessage() string  { return e.Message }
func (e *NodeError) relayRetryable() bool  { return e.Retryable }

// relayError turns an error into the fields the handler needs to build the
// HTTP response. Gateway errors (OpError) carry their own HTTPStatus; node
// errors (NodeError) default to 503 with the node's retryable. Unknown
// errors are wrapped as a generic node_unreachable 503 (the dial/frame
// failure path) — never silent, never coerced to 502.
func relayError(err error) (status int, code, message string, retryable bool) {
    var oe *OpError
    if errors.As(err, &oe) {
        return oe.HTTPStatus, oe.relayCode(), oe.relayMessage(), oe.relayRetryable()
    }
    var ne *NodeError
    if errors.As(err, &ne) {
        return 503, ne.relayCode(), ne.relayMessage(), ne.relayRetryable()
    }
    // Scheduler sentinels → 503 with their own code.
    if errors.Is(err, ErrGlobalQuotaExceeded) {
        return 503, "quota_exceeded", err.Error(), false
    }
    if errors.Is(err, ErrNoNodeHeadroom) {
        return 503, "no_headroom", err.Error(), true
    }
    // Dial / frame / decode failures → node_unreachable 503 retryable.
    slog.Warn("browser: unmapped error relayed as node_unreachable", "error", err)
    return 503, "node_unreachable", err.Error(), true
}
