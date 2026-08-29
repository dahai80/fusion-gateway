package browser

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "net"
    "time"
)

// NodeClient dials a fusion-browser node over UDS, sends one length-prefixed
// frame, and reads one response frame. Per-call dial (no pooled connection):
// fusion-browser runs a per-client read loop and closes the connection when
// the client disconnects, so a pooled conn would race the close. A fresh dial
// per request matches the engine's per-client lifecycle and keeps the gateway
// stateless per node.
type NodeClient struct {
    dialTimeout time.Duration
    frameMax    int
    frameTimeout time.Duration
}

// NewNodeClient builds a client with the framing + dial bounds from config.
// Zero values fall back to the framing.go defaults (8 MiB cap, 30s timeout).
func NewNodeClient(dialTimeout time.Duration, frameMax int, frameTimeout time.Duration) *NodeClient {
    if dialTimeout <= 0 {
        dialTimeout = 2 * time.Second
    }
    if frameMax <= 0 {
        frameMax = defaultFrameMaxBytes
    }
    if frameTimeout <= 0 {
        frameTimeout = defaultFrameTimeout
    }
    return &NodeClient{
        dialTimeout:  dialTimeout,
        frameMax:     frameMax,
        frameTimeout: frameTimeout,
    }
}

// dialUDS opens a UDS connection to socketPath bounded by dialTimeout. The
// context is respected for cancellation (a request cancel propagates to the
// dial, releasing the slot promptly). Returns the conn or a wrapped error
// the caller surfaces as node_unreachable (503 retryable).
func (c *NodeClient) dialUDS(ctx context.Context, socketPath string) (net.Conn, error) {
    d := net.Dialer{}
    if c.dialTimeout > 0 {
        d.Timeout = c.dialTimeout
    }
    conn, err := d.DialContext(ctx, "unix", socketPath)
    if err != nil {
        return nil, fmt.Errorf("browser: dial node %q: %w", socketPath, err)
    }
    return conn, nil
}

// roundTrip sends req as one frame to the node at socketPath and reads one
// response frame. It returns the raw response envelope bytes (decoded by the
// caller) and closes the connection before returning (per-call dial). Any
// error is wrapped distinctly so the proxy can map it to the right HTTP code
// (dial/frame errors → node_unreachable; FBError from the node → relayed).
func (c *NodeClient) roundTrip(ctx context.Context, socketPath string, req any) (*ResponseFrame, error) {
    conn, err := c.dialUDS(ctx, socketPath)
    if err != nil {
        return nil, err
    }
    // Close on all paths — a leaked conn would hold the node's per-client slot.
    defer func() {
        if cerr := conn.Close(); cerr != nil {
            slog.Debug("browser node conn close error", "socket", socketPath, "error", cerr)
        }
    }()

    if err := WriteFrame(conn, req); err != nil {
        return nil, fmt.Errorf("browser: write request to %q: %w", socketPath, err)
    }

    body, err := ReadFrame(conn, c.frameMax, c.frameTimeout)
    if err != nil {
        return nil, fmt.Errorf("browser: read response from %q: %w", socketPath, err)
    }

    var resp ResponseFrame
    if err := json.Unmarshal(body, &resp); err != nil {
        return nil, fmt.Errorf("browser: decode response envelope from %q: %w", socketPath, err)
    }
    return &resp, nil
}

// Capacity queries a node's {type:"capacity"} and returns the decoded
// FBNodeCapacity — the placement signal the registry stores on each poll.
// This is one of the two fully-decoded ops (create_session is the other);
// the rest forward verbatim.
func (c *NodeClient) Capacity(ctx context.Context, socketPath string) (*FBNodeCapacity, error) {
    resp, err := c.roundTrip(ctx, socketPath, RequestFrame{Type: reqTypeCapacity})
    if err != nil {
        return nil, err
    }
    if resp.Type == respTypeError {
        return nil, decodeNodeError(resp.Payload, socketPath)
    }
    if resp.Type != respTypeCapacity {
        return nil, fmt.Errorf("browser: unexpected capacity response type %q from %q", resp.Type, socketPath)
    }
    var cap FBNodeCapacity
    if err := json.Unmarshal(resp.Payload, &cap); err != nil {
        return nil, fmt.Errorf("browser: decode capacity from %q: %w", socketPath, err)
    }
    return &cap, nil
}

// Create forwards a create_session request and returns the decoded
// CreateSessionResponse (the gateway needs session_id to record the pin). On
// a node error response, returns the relayed FBError so the proxy maps it to
// the node's own code/retryable (never coerced to 502 — RC1 lesson).
func (c *NodeClient) Create(ctx context.Context, socketPath string, req *CreateSessionRequest) (*CreateSessionResponse, error) {
    payload, err := marshalPayload(req)
    if err != nil {
        return nil, err
    }
    frame := RequestFrame{Type: reqTypeCreateSession, Payload: payload}
    resp, err := c.roundTrip(ctx, socketPath, frame)
    if err != nil {
        return nil, err
    }
    if resp.Type == respTypeError {
        return nil, decodeNodeError(resp.Payload, socketPath)
    }
    if resp.Type != respTypeCreateSession {
        return nil, fmt.Errorf("browser: unexpected create_session response type %q from %q", resp.Type, socketPath)
    }
    var out CreateSessionResponse
    if err := json.Unmarshal(resp.Payload, &out); err != nil {
        return nil, fmt.Errorf("browser: decode create_session response from %q: %w", socketPath, err)
    }
    return &out, nil
}

// Execute forwards an execute request and returns the raw state response
// envelope. The proxy relays the payload VERBATIM (no re-encode) — the
// gateway does not interpret the AXTree/screenshot fields. Only the envelope
// type is checked so an error response is surfaced as a relayed FBError.
func (c *NodeClient) Execute(ctx context.Context, socketPath string, req *BrowserActionRequest) (*ResponseFrame, error) {
    payload, err := marshalPayload(req)
    if err != nil {
        return nil, err
    }
    frame := RequestFrame{Type: reqTypeExecute, Payload: payload}
    resp, err := c.roundTrip(ctx, socketPath, frame)
    if err != nil {
        return nil, err
    }
    if resp.Type == respTypeError {
        return nil, decodeNodeError(resp.Payload, socketPath)
    }
    return resp, nil
}

// Close forwards a close request and returns the closed confirmation. Close
// is idempotent — a node may already be gone (dead pin), so a dial error is
// returned distinctly and the proxy treats it as best-effort (evict the pin
// regardless). On a node FBError the proxy still evicts the pin (the session
// is gone either way); the error is surfaced for observability only.
func (c *NodeClient) Close(ctx context.Context, socketPath, sessionID string) error {
    frame := RequestFrame{Type: reqTypeClose, SessionID: sessionID}
    resp, err := c.roundTrip(ctx, socketPath, frame)
    if err != nil {
        return err
    }
    if resp.Type == respTypeError {
        return decodeNodeError(resp.Payload, socketPath)
    }
    if resp.Type != respTypeClosed {
        return fmt.Errorf("browser: unexpected close response type %q from %q", resp.Type, socketPath)
    }
    return nil
}

// Metrics forwards a metrics request and returns the raw metrics envelope.
// Admin-only at the proxy; the gateway relays the opaque counters/latency
// verbatim.
func (c *NodeClient) Metrics(ctx context.Context, socketPath string) (*ResponseFrame, error) {
    frame := RequestFrame{Type: reqTypeMetrics}
    resp, err := c.roundTrip(ctx, socketPath, frame)
    if err != nil {
        return nil, err
    }
    if resp.Type == respTypeError {
        return nil, decodeNodeError(resp.Payload, socketPath)
    }
    if resp.Type != respTypeMetrics {
        return nil, fmt.Errorf("browser: unexpected metrics response type %q from %q", resp.Type, socketPath)
    }
    return resp, nil
}

// marshalPayload encodes a payload struct to json.RawMessage for the envelope.
func marshalPayload(p any) (json.RawMessage, error) {
    b, err := json.Marshal(p)
    if err != nil {
        return nil, fmt.Errorf("browser: marshal payload: %w", err)
    }
    return b, nil
}

// decodeNodeError decodes an error-response payload into an FBError the caller
// can relay verbatim. A malformed error payload is itself an error (the node
// is misbehaving), surfaced distinctly so the proxy logs it.
func decodeNodeError(payload []byte, socketPath string) error {
    var fe FBError
    if err := json.Unmarshal(payload, &fe); err != nil {
        return fmt.Errorf("browser: decode node error from %q: %w", socketPath, err)
    }
    return &NodeError{FBError: fe, NodeSocket: socketPath}
}

// NodeError wraps a node's FBError with the socket it came from, so logs and
// the admin map can attribute the failure. Implements error; the proxy uses
// errors.As to extract the FBError and relay its code/retryable.
type NodeError struct {
    FBError
    NodeSocket string
}

func (e *NodeError) Error() string {
    return fmt.Sprintf("browser node %s error %s: %s", e.NodeSocket, e.Code, e.Message)
}

// AsNodeError extracts a *NodeError from an error chain, if present. Returns
// nil otherwise so the caller can branch on node-error vs gateway-error.
func AsNodeError(err error) *NodeError {
    var ne *NodeError
    if errors.As(err, &ne) {
        return ne
    }
    return nil
}
