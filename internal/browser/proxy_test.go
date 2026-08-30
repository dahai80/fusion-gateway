package browser

import (
    "errors"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestProxyCreatePinsSession(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity:    func(req RequestFrame) ResponseFrame { return capResp(FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 0, FreeMemoryMB: 8000}) },
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-42", true) },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    sched := NewScheduler(reg, 0, 200)
    proxy := NewProxy(reg, dialClient(), sched)

    ctx, cancel := ctxShort()
    defer cancel()
    res, err := proxy.Create(ctx, &CreateSessionRequest{Mode: WebModeHeadless})
    if err != nil {
        t.Fatalf("Create: %v", err)
    }
    if res.SessionID != "sess-42" || res.NodeID != "n1" || !res.CredentialInjected {
        t.Fatalf("CreateResult wrong: %+v", res)
    }
    // Pin recorded.
    node, ok := proxy.lookupPin("sess-42")
    if !ok || node != "n1" {
        t.Fatalf("pin not recorded: node=%q ok=%v", node, ok)
    }
}

func TestProxyCreateQuotaExceeded(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    seedNode(t, reg, "n1", "/tmp/n1.sock")
    setCap(t, reg, "n1", &FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 3, FreeMemoryMB: 8000})
    sched := NewScheduler(reg, 3, 200) // ceiling 3, live 3 → quota
    proxy := NewProxy(reg, dialClient(), sched)
    ctx, cancel := ctxShort()
    defer cancel()
    _, err := proxy.Create(ctx, &CreateSessionRequest{Mode: WebModeHeadless})
    if !errors.Is(err, ErrGlobalQuotaExceeded) {
        t.Fatalf("expected ErrGlobalQuotaExceeded, got %v", err)
    }
}

func TestProxyExecutePinMiss(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    _, err := proxy.Execute(ctx, &BrowserActionRequest{SessionID: "ghost", Action: ActionClick})
    if !errIs(err, "session_not_found") {
        t.Fatalf("expected session_not_found, got %v", err)
    }
    var oe *OpError
    if !errors.As(err, &oe) || oe.HTTPStatus != 404 {
        t.Fatalf("expected OpError 404, got %T %v", err, err)
    }
}

func TestProxyExecuteForwardsVerbatim(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-1", false) },
        reqTypeExecute: func(req RequestFrame) ResponseFrame {
            return stateResp(map[string]any{"session_id": "sess-1", "url": "https://example.com", "title": "Example", "ax_tree_markdown": "# root"})
        },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    if _, err := proxy.Create(ctx, &CreateSessionRequest{Mode: WebModeHeadless}); err != nil {
        t.Fatalf("Create: %v", err)
    }
    res, err := proxy.Execute(ctx, &BrowserActionRequest{SessionID: "sess-1", Action: ActionClick})
    if err != nil {
        t.Fatalf("Execute: %v", err)
    }
    // Verbatim payload: fields preserved untouched.
    if !errIs(&stringError{s: string(res.Payload)}, `"ax_tree_markdown":"# root"`) {
        t.Fatalf("state payload not relayed verbatim: %s", res.Payload)
    }
}

func TestProxyExecuteDeadPinEvictsAndErrors(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-1", false) },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    if _, err := proxy.Create(ctx, &CreateSessionRequest{Mode: WebModeHeadless}); err != nil {
        t.Fatalf("Create: %v", err)
    }
    killNode(t, reg, "n1")
    _, err = proxy.Execute(ctx, &BrowserActionRequest{SessionID: "sess-1", Action: ActionClick})
    if !errIs(err, "session_lost") {
        t.Fatalf("expected session_lost, got %v", err)
    }
    // Pin evicted.
    if _, ok := proxy.lookupPin("sess-1"); ok {
        t.Fatal("dead pin should be evicted after execute error")
    }
}

func TestProxyCloseIdempotentNoPin(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    // Close of an unknown session is a no-op success (204 path).
    if err := proxy.Close(ctx, "ghost"); err != nil {
        t.Fatalf("Close of unknown session should be nil, got %v", err)
    }
}

func TestProxyCloseEvictsPin(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-1", false) },
        reqTypeClose:         func(req RequestFrame) ResponseFrame { return ResponseFrame{Type: respTypeClosed} },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    if _, err := proxy.Create(ctx, &CreateSessionRequest{Mode: WebModeHeadless}); err != nil {
        t.Fatalf("Create: %v", err)
    }
    if err := proxy.Close(ctx, "sess-1"); err != nil {
        t.Fatalf("Close: %v", err)
    }
    if _, ok := proxy.lookupPin("sess-1"); ok {
        t.Fatal("pin should be evicted after close")
    }
    if fn.count(reqTypeClose) != 1 {
        t.Fatalf("expected 1 close forwarded, got %d", fn.count(reqTypeClose))
    }
}

func TestProxyMetricsForwardsFirstLive(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeMetrics: func(req RequestFrame) ResponseFrame { return metricsResp(map[string]any{"counters": []map[string]any{{"name": "sessions", "value": 5}}}) },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    res, err := proxy.Metrics(ctx, "") // empty → first live node
    if err != nil {
        t.Fatalf("Metrics: %v", err)
    }
    if !errIs(&stringError{s: string(res.Payload)}, `"value":5`) {
        t.Fatalf("metrics payload not relayed: %s", res.Payload)
    }
}

func TestProxyMetricsNoLiveNode(t *testing.T) {
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    _, err := proxy.Metrics(ctx, "")
    if !errIs(err, "no_live_node") {
        t.Fatalf("expected no_live_node, got %v", err)
    }
}

func TestProxyCreateRelaysNodeError(t *testing.T) {
    // Node returns a quota_exceeded FBError on create → proxy relays as a
    // *NodeError carrying the node's own code/retryable (NOT coerced to 502).
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return errResp(ErrCodeQuotaExceeded, "node full", false) },
    })
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    ctx, cancel := ctxShort()
    defer cancel()
    _, err = proxy.Create(ctx, &CreateSessionRequest{Mode: WebModeHeadless})
    ne := AsNodeError(err)
    if ne == nil {
        t.Fatalf("expected *NodeError, got %T %v", err, err)
    }
    if ne.Code != ErrCodeQuotaExceeded || ne.Message != "node full" {
        t.Fatalf("node error not relayed verbatim: %+v", ne)
    }
}

// stringError is a test-only error wrapper so errIs can match against a raw
// payload string without importing fmt.Errorf (keeps the test dep-free).
type stringError struct{ s string }

func (e *stringError) Error() string { return e.s }
