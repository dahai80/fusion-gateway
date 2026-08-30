package browser

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net"
    "os"
    "sync"
    "sync/atomic"
    "testing"
    "time"
)

// fakeNode is a mock fusion-browser node listening on a UDS socket. It models
// the FR-10/H-5 auth contract: the FIRST frame on a connection MUST be an
// {type:"auth", token} frame matching the node's token, else it replies
// auth_denied and closes (mirrors the real UDSServer first-frame-must-be-auth
// gate). After auth_ack, it reads the op frame, dispatches to a per-type
// handler, and writes one response frame. Connections are closed after one
// auth+op round trip (matches the gateway's per-call dial). The listener is
// accepted in a goroutine; tests call Stop() to close + drain.
type fakeNode struct {
    socket   string
    token    string
    listener net.Listener
    handlers map[string]func(req RequestFrame) ResponseFrame

    mu       sync.Mutex
    stopped  bool
    accepted map[string]int // request type -> count, for assertion
    authDeny int            // count of auth_denied replies (regression assertion)
}

// fakeNodeCounter mints unique short socket names. macOS limits a UDS path to
// ~104 bytes (sun_len); t.TempDir() paths are ~95 bytes and overflow once the
// test name + /fb.sock is appended. So we bind sockets directly under /tmp
// with a short numeric suffix — well under the limit, and unique across the
// parallel test run.
var fakeNodeCounter atomic.Uint64

// newFakeNode starts a mock node on a short UDS socket under /tmp with a fixed
// test token. The handlers map keys are request Type strings (reqTypeCapacity,
// reqTypeCreateSession, ...). A missing handler returns a generic error frame
// so the test sees a clear failure rather than a hang. The auth gate is
// enforced: a non-auth first frame, or a token mismatch, is rejected with
// auth_denied — this is what let the #132 bug ship (the prior fake did not
// model auth, so CI stayed green while the live path was broken).
func newFakeNode(t *testing.T, handlers map[string]func(req RequestFrame) ResponseFrame) *fakeNode {
    t.Helper()
    return newFakeNodeWithToken(t, "test-token", handlers)
}

// newFakeNodeWithToken starts a mock node with an explicit token (for the
// token-mismatch / empty-token regression assertions).
func newFakeNodeWithToken(t *testing.T, token string, handlers map[string]func(req RequestFrame) ResponseFrame) *fakeNode {
    t.Helper()
    n := fakeNodeCounter.Add(1)
    socket := fmt.Sprintf("/tmp/fbtest-%d.sock", n)
    os.Remove(socket) // clear any stale socket from a prior crashed run
    ln, err := net.Listen("unix", socket)
    if err != nil {
        t.Fatalf("fake node listen: %v", err)
    }
    fn := &fakeNode{
        socket:   socket,
        token:    token,
        listener: ln,
        handlers: handlers,
        accepted: make(map[string]int),
    }
    go fn.acceptLoop()
    t.Cleanup(fn.Stop)
    return fn
}

func (f *fakeNode) acceptLoop() {
    for {
        conn, err := f.listener.Accept()
        if err != nil {
            f.mu.Lock()
            stopped := f.stopped
            f.mu.Unlock()
            if stopped {
                return
            }
            continue
        }
        go f.serve(conn)
    }
}

// serve enforces the auth gate then dispatches the op. The first frame must be
// {type:"auth", token}; a wrong type or token mismatch → auth_denied + close
// (matching the real node). On auth_ack it reads the op frame and dispatches.
func (f *fakeNode) serve(conn net.Conn) {
    defer conn.Close()
    body, err := ReadFrame(conn, defaultFrameMaxBytes, 5*time.Second)
    if err != nil {
        return
    }
    // Decode the first frame as both the generic envelope (for the type) and
    // the auth message (for the token). AuthMessage uses {type, token}, so the
    // token is read from the dedicated JSON key, not SessionID.
    var first RequestFrame
    if err := json.Unmarshal(body, &first); err != nil {
        return
    }
    var am AuthMessage
    _ = json.Unmarshal(body, &am)
    // FR-10/H-5: first frame MUST be auth with the matching token. A non-auth
    // first frame (e.g. a bare capacity request — the #132 bug) or a token
    // mismatch is rejected exactly as the real node does: auth_denied + close.
    if first.Type != reqTypeAuth || am.Token != f.token {
        f.mu.Lock()
        f.authDeny++
        f.mu.Unlock()
        _ = WriteFrame(conn, errResp(ErrCodeAuthDenied, "authentication token invalid or missing", false))
        return
    }
    // auth_ack — the connection is now authorized for one op.
    if err := WriteFrame(conn, ResponseFrame{Type: respTypeAuthAck}); err != nil {
        return
    }
    // Read the op frame + dispatch.
    opBody, err := ReadFrame(conn, defaultFrameMaxBytes, 5*time.Second)
    if err != nil {
        return
    }
    var req RequestFrame
    if err := json.Unmarshal(opBody, &req); err != nil {
        return
    }
    f.mu.Lock()
    f.accepted[req.Type]++
    handler := f.handlers[req.Type]
    f.mu.Unlock()
    var resp ResponseFrame
    if handler != nil {
        resp = handler(req)
    } else {
        resp = ResponseFrame{Type: respTypeError, Payload: mustMarshal(FBError{Code: "no_handler", Message: "fake node has no handler for " + req.Type, Retryable: false})}
    }
    if err := WriteFrame(conn, resp); err != nil {
        return
    }
}

// authDeniedCount returns how many connections were rejected at the auth gate.
func (f *fakeNode) authDeniedCount() int {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.authDeny
}

// count returns how many requests of a given type the fake node served.
func (f *fakeNode) count(reqType string) int {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.accepted[reqType]
}

// Stop closes the listener and removes the socket file. Safe to call once.
func (f *fakeNode) Stop() {
    f.mu.Lock()
    f.stopped = true
    f.mu.Unlock()
    if f.listener != nil {
        f.listener.Close()
    }
    if f.socket != "" {
        os.Remove(f.socket)
    }
}

// mustMarshal is a test helper that panics on marshal failure (test setup only).
func mustMarshal(v any) json.RawMessage {
    b, err := json.Marshal(v)
    if err != nil {
        panic(err)
    }
    return b
}

// errResp builds an error ResponseFrame for a fake-node handler.
func errResp(code, msg string, retryable bool) ResponseFrame {
    return ResponseFrame{Type: respTypeError, Payload: mustMarshal(FBError{Code: code, Message: msg, Retryable: retryable})}
}

// capResp builds a capacity ResponseFrame.
func capResp(cap FBNodeCapacity) ResponseFrame {
    return ResponseFrame{Type: respTypeCapacity, Payload: mustMarshal(cap)}
}

// createResp builds a create_session ResponseFrame.
func createResp(sessionID string, injected bool) ResponseFrame {
    return ResponseFrame{Type: respTypeCreateSession, Payload: mustMarshal(CreateSessionResponse{SessionID: sessionID, CredentialInjected: injected})}
}

// stateResp builds a state ResponseFrame with a verbatim payload the proxy
// should forward untouched.
func stateResp(payload map[string]any) ResponseFrame {
    return ResponseFrame{Type: respTypeState, Payload: mustMarshal(payload)}
}

// metricsResp builds a metrics ResponseFrame with opaque counters.
func metricsResp(payload map[string]any) ResponseFrame {
    return ResponseFrame{Type: respTypeMetrics, Payload: mustMarshal(payload)}
}

// dialClient builds a NodeClient tuned for tests (short dial timeout so a
// missing socket fails fast rather than hanging the test).
func dialClient() *NodeClient {
    return NewNodeClient(500*time.Millisecond, defaultFrameMaxBytes, 2*time.Second)
}

// ctxShort returns a context with a short timeout for test round trips.
func ctxShort() (context.Context, context.CancelFunc) {
    return context.WithTimeout(context.Background(), 3*time.Second)
}

// errIs reports whether err's message contains substr (loose check for test
// assertions over wrapped errors).
func errIs(err error, substr string) bool {
    return err != nil && contains(err.Error(), substr)
}

func contains(s, substr string) bool {
    return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
    for i := 0; i+len(substr) <= len(s); i++ {
        if s[i:i+len(substr)] == substr {
            return i
        }
    }
    return -1
}

var _ = errors.New
