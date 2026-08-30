package browser

import (
    "context"
    "testing"
)

// TestNodeClientAuthHandshakeSucceeds is the happy path of the FR-10/H-5
// handshake: a client presenting the node's exact token gets an auth_ack and
// its op is served. Guards against a regression where the handshake was
// dropped (the #132 root cause — every op silently skipped auth, so CI stayed
// green against a fake node that also did not enforce auth).
func TestNodeClientAuthHandshakeSucceeds(t *testing.T) {
    fn := newFakeNodeWithToken(t, "node-secret", map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity: func(req RequestFrame) ResponseFrame {
            return capResp(FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 0, FreeMemoryMB: 8000})
        },
    })
    ctx, cancel := ctxShort()
    defer cancel()
    cap, err := dialClient().Capacity(ctx, fn.socket, "node-secret")
    if err != nil {
        t.Fatalf("Capacity with correct token: %v", err)
    }
    if cap.NodeID != "n1" {
        t.Fatalf("capacity not decoded: %+v", cap)
    }
    if fn.count(reqTypeCapacity) != 1 {
        t.Fatalf("expected 1 capacity served, got %d", fn.count(reqTypeCapacity))
    }
    if fn.authDeniedCount() != 0 {
        t.Fatalf("correct token should not be denied, got %d denies", fn.authDeniedCount())
    }
}

// TestNodeClientWrongTokenRejectedAuthDenied is the #132 regression guard: a
// client dialing with a token that does not match the node's authToken is
// rejected at the auth gate with auth_denied, the op is NEVER served, and the
// node records the denial. Before the fix the client sent no auth frame at all
// and the (non-auth-enforcing) fake served the op — a live node would have
// closed the connection, so this test models the real contract.
func TestNodeClientWrongTokenRejectedAuthDenied(t *testing.T) {
    fn := newFakeNodeWithToken(t, "node-secret", map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity: func(req RequestFrame) ResponseFrame {
            return capResp(FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 0, FreeMemoryMB: 8000})
        },
    })
    ctx, cancel := ctxShort()
    defer cancel()
    _, err := dialClient().Capacity(ctx, fn.socket, "WRONG-token")
    if err == nil {
        t.Fatal("expected auth_denied error for wrong token, got nil")
    }
    // The client surfaces the node's own auth_denied code verbatim.
    ne := AsNodeError(err)
    if ne == nil || ne.Code != ErrCodeAuthDenied {
        t.Fatalf("expected *NodeError code %q, got %T %v", ErrCodeAuthDenied, err, err)
    }
    // The op frame was never served — only the auth round trip happened.
    if fn.count(reqTypeCapacity) != 0 {
        t.Fatalf("op must NOT be served on auth_denied, got %d capacity calls", fn.count(reqTypeCapacity))
    }
    // The fake node recorded the gate rejection (regression assertion).
    if fn.authDeniedCount() != 1 {
        t.Fatalf("expected 1 auth_denied at the node, got %d", fn.authDeniedCount())
    }
}

// TestNodeClientEmptyTokenFailsClosed asserts the fail-closed guard: a missing
// token is rejected client-side BEFORE any dial (the node is deny-all without a
// token, so there is no point dialing). This is the static-config-misconfig
// path; config.Validate catches it for seeds, but the client guards runtime
// dial-in paths too. No connection is opened, so the fake is unused here.
func TestNodeClientEmptyTokenFailsClosed(t *testing.T) {
    fn := newFakeNodeWithToken(t, "node-secret", map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity: func(req RequestFrame) ResponseFrame {
            return capResp(FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 0, FreeMemoryMB: 8000})
        },
    })
    ctx, cancel := ctxShort()
    defer cancel()
    _, err := dialClient().Capacity(ctx, fn.socket, "")
    if err == nil {
        t.Fatal("expected error for empty token, got nil")
    }
    if !errIs(err, "missing auth token") {
        t.Fatalf("expected missing-auth-token error, got %v", err)
    }
    // No dial happened — node never saw a connection, no deny, no op.
    if fn.authDeniedCount() != 0 || fn.count(reqTypeCapacity) != 0 {
        t.Fatalf("empty token must not reach the node: denies=%d capacity=%d",
            fn.authDeniedCount(), fn.count(reqTypeCapacity))
    }
}

// TestNodeClientAuthAckRequiredForEveryOp verifies the handshake runs on every
// dial (per-call dial, no pooled authenticated session). A node that requires
// auth on each connection rejects an op-only dial. This models the real
// fusion-browser per-client lifecycle, which closes after one client, so a
// reused pooled conn would race the close.
func TestNodeClientAuthAckRequiredForEveryOp(t *testing.T) {
    fn := newFakeNodeWithToken(t, "node-secret", map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCapacity: func(req RequestFrame) ResponseFrame {
            return capResp(FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 0, FreeMemoryMB: 8000})
        },
    })
    ctx, cancel := ctxShort()
    defer cancel()
    client := dialClient()
    // Two sequential calls = two fresh dials = two auth handshakes. Both must
    // re-auth and succeed (a pooled-conn regression would close after the first).
    for i := 0; i < 2; i++ {
        if _, err := client.Capacity(ctx, fn.socket, "node-secret"); err != nil {
            t.Fatalf("call %d: %v", i, err)
        }
    }
    if fn.count(reqTypeCapacity) != 2 {
        t.Fatalf("expected 2 served capacity ops, got %d", fn.count(reqTypeCapacity))
    }
    if fn.authDeniedCount() != 0 {
        t.Fatalf("correct tokens should not be denied, got %d", fn.authDeniedCount())
    }
}

var _ = context.Background
