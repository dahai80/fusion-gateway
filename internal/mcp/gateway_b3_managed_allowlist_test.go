package mcp

// #129 Gap 3 test: managed-MCP per-node tool allowlist admission. Empty
// allowlist = unrestricted (admit every registered tool). Non-empty = a tool
// whose Name is NOT in the list is rejected at HandleToolCall before any
// forward, never dialing a node. SetManagedToolAllowlist is concurrent-safe
// with admission (hot-reload toggle).

import (
    "context"
    "strings"
    "testing"
)

func registerTestTool(gw *MCPClusterGateway, name string) {
    gw.RegisterTool(&MCPTool{
        Name:        name,
        Description: "test",
        Parameters:  map[string]interface{}{},
    })
}

// TestB3_EmptyAllowlistAdmitsAll: no managed allowlist installed → a registered
// tool passes the admission gate (reaches "unknown tool" only because no node
// selector + no live node, but the gate itself did NOT reject). Asserts the
// unrestricted default is preserved.
func TestB3_EmptyAllowlistAdmitsAll(t *testing.T) {
    gw := NewMCPClusterGateway(DefaultGatewayConfig())
    registerTestTool(gw, "enterprise_tool")
    // No SetManagedToolAllowlist call → nil allowlist → unrestricted.
    result := gw.HandleToolCall(context.Background(), "enterprise_tool", nil, "api")
    // Should NOT be the managed-allowlist rejection.
    if err, ok := result["error"]; ok {
        if strings.Contains(err.(string), "not permitted on this node") {
            t.Fatalf("B3: empty allowlist rejected a tool (must be unrestricted), got: %v", err)
        }
    }
}

// TestB3_NonEmptyAllowlistAdmitsListed: allowlist = ["enterprise_tool"]; calling
// the listed tool passes the gate (admission OK; downstream may still fail on
// no-live-node, but the allowlist did not reject).
func TestB3_NonEmptyAllowlistAdmitsListed(t *testing.T) {
    gw := NewMCPClusterGateway(DefaultGatewayConfig())
    registerTestTool(gw, "enterprise_tool")
    gw.SetManagedToolAllowlist([]string{"enterprise_tool"})
    result := gw.HandleToolCall(context.Background(), "enterprise_tool", nil, "api")
    if err, ok := result["error"]; ok {
        if strings.Contains(err.(string), "not permitted on this node") {
            t.Fatalf("B3: listed tool rejected by allowlist (should pass), got: %v", err)
        }
    }
}

// TestB3_NonEmptyAllowlistRejectsUnlisted: allowlist = ["enterprise_tool"];
// calling a DIFFERENT registered tool is rejected at admission — never dials a
// node. Asserts the rejection error string + that the gate fires before
// token-budget / node-selection.
func TestB3_NonEmptyAllowlistRejectsUnlisted(t *testing.T) {
    gw := NewMCPClusterGateway(DefaultGatewayConfig())
    registerTestTool(gw, "enterprise_tool")
    registerTestTool(gw, "rogue_tool")
    gw.SetManagedToolAllowlist([]string{"enterprise_tool"})

    result := gw.HandleToolCall(context.Background(), "rogue_tool", nil, "api")
    errVal, ok := result["error"]
    if !ok {
        t.Fatal("B3: unlisted tool was NOT rejected — allowlist gate missing")
    }
    got := errVal.(string)
    if !strings.Contains(got, "not permitted on this node") {
        t.Fatalf("B3: rejection message wrong, got %q want substring 'not permitted on this node'", got)
    }
}

// TestB3_SetManagedToolAllowlist_HotReloadToggle: installing an allowlist
// restricts; clearing it (empty slice) re-opens admission. Asserts the setter
// is concurrent-safe with admission (hot-reload path used by RebuildMiddlewareChain).
func TestB3_SetManagedToolAllowlist_HotReloadToggle(t *testing.T) {
    gw := NewMCPClusterGateway(DefaultGatewayConfig())
    registerTestTool(gw, "enterprise_tool")

    // Restrict → enterprise_tool still admitted.
    gw.SetManagedToolAllowlist([]string{"enterprise_tool"})
    r1 := gw.HandleToolCall(context.Background(), "enterprise_tool", nil, "api")
    if err, ok := r1["error"]; ok && strings.Contains(err.(string), "not permitted on this node") {
        t.Fatalf("B3: listed tool rejected after restrict, got: %v", err)
    }

    // Toggle back to unrestricted (empty) → still admitted, no allowlist error.
    gw.SetManagedToolAllowlist([]string{})
    r2 := gw.HandleToolCall(context.Background(), "enterprise_tool", nil, "api")
    if err, ok := r2["error"]; ok && strings.Contains(err.(string), "not permitted on this node") {
        t.Fatalf("B3: tool rejected after clearing allowlist (must be unrestricted), got: %v", err)
    }
}

// TestB3_admitTool_Direct: unit-test the gate itself for both branches without
// the HandleToolCall machinery.
func TestB3_admitTool_Direct(t *testing.T) {
    gw := NewMCPClusterGateway(DefaultGatewayConfig())
    if !gw.admitTool("anything") {
        t.Fatal("B3: nil allowlist must admit everything")
    }
    gw.SetManagedToolAllowlist([]string{"a", "b"})
    if !gw.admitTool("a") || !gw.admitTool("b") {
        t.Fatal("B3: listed tools must be admitted")
    }
    if gw.admitTool("c") {
        t.Fatal("B3: unlisted tool must be rejected")
    }
    gw.SetManagedToolAllowlist(nil)
    if !gw.admitTool("anything") {
        t.Fatal("B3: nil allowlist (after nil set) must admit everything")
    }
}
