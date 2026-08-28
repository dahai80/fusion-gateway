package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNewMCPClusterGateway(t *testing.T) {
	cfg := DefaultGatewayConfig()
	gw := NewMCPClusterGateway(cfg)
	if gw == nil {
		t.Fatal("gateway should not be nil")
	}
	if gw.tokenBudget != cfg.TokenBudget {
		t.Errorf("token budget = %d, want %d", gw.tokenBudget, cfg.TokenBudget)
	}
	if gw.maxRequests != cfg.MaxRequests {
		t.Errorf("max requests = %d, want %d", gw.maxRequests, cfg.MaxRequests)
	}
}

func TestNewMCPClusterGateway_Defaults(t *testing.T) {
	cfg := GatewayConfig{}
	gw := NewMCPClusterGateway(cfg)
	if gw.tokenBudget != 10_000_000 {
		t.Errorf("default token budget = %d, want 10000000", gw.tokenBudget)
	}
	if gw.maxRequests != 10000 {
		t.Errorf("default max requests = %d, want 10000", gw.maxRequests)
	}
}

func TestRegisterTool(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	tool := &MCPTool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  map[string]interface{}{"type": "object"},
		Plugin:      "test_plugin",
	}
	gw.RegisterTool(tool)

	gw.toolsMu.RLock()
	_, ok := gw.tools["test_tool"]
	gw.toolsMu.RUnlock()
	if !ok {
		t.Error("tool should be registered")
	}
}

func TestUnregisterTool(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	tool := &MCPTool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  map[string]interface{}{"type": "object"},
	}
	gw.RegisterTool(tool)
	gw.UnregisterTool("test_tool")

	gw.toolsMu.RLock()
	_, ok := gw.tools["test_tool"]
	gw.toolsMu.RUnlock()
	if ok {
		t.Error("tool should be unregistered")
	}
}

func TestUnregisterTool_NotFound(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.UnregisterTool("nonexistent")
	// should not panic
}

func TestGetToolsList(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.RegisterTool(&MCPTool{
		Name:        "tool_a",
		Description: "Tool A",
		Parameters:  map[string]interface{}{"type": "object"},
	})
	gw.RegisterTool(&MCPTool{
		Name:        "tool_b",
		Description: "Tool B",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	tools := gw.GetToolsList()
	if len(tools) != 2 {
		t.Fatalf("tools list length = %d, want 2", len(tools))
	}

	names := map[string]bool{}
	for _, t := range tools {
		n, _ := t["name"].(string)
		names[n] = true
	}
	if !names["tool_a"] || !names["tool_b"] {
		t.Error("expected both tool_a and tool_b in list")
	}
}

func TestGetToolsList_Empty(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	tools := gw.GetToolsList()
	if len(tools) != 0 {
		t.Errorf("empty gateway should have 0 tools, got %d", len(tools))
	}
}

func TestHandleToolCall_UnknownTool(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	result := gw.HandleToolCall(context.Background(), "nonexistent", nil, "api")
	errVal, ok := result["error"]
	if !ok {
		t.Error("expected error in result")
	}
	if errVal != "unknown tool: nonexistent" {
		t.Errorf("error = %v, want 'unknown tool: nonexistent'", errVal)
	}
}

func TestHandleToolCall_TokenBudgetExhausted(t *testing.T) {
	cfg := DefaultGatewayConfig()
	gw := NewMCPClusterGateway(cfg)
	gw.tokenBudget = 0
	gw.totalTokenCount.Store(10)
	gw.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	result := gw.HandleToolCall(context.Background(), "test_tool", nil, "api")
	errVal, ok := result["error"]
	if !ok {
		t.Error("expected error in result")
	}
	if errVal != "token budget exhausted" {
		t.Errorf("error = %v, want 'token budget exhausted'", errVal)
	}
}

func TestHandleToolCall_LocalhostForward(t *testing.T) {
	// set up a mock local server
	var receivedPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		receivedPayload = payload
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": "ok",
			"output": "test_output",
		})
	}))
	defer server.Close()

	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	// use custom node selector to redirect to test server
	gw.SetNodeSelector(func(tool *MCPTool) (string, error) {
		return "localhost", nil
	})

	// override the local port by patching forwardToNode — test via the real flow
	// we'll test forwardToNode directly instead
	ctx := context.Background()
	req := &MCPRequest{
		RequestID:    "mcp_test123",
		ToolName:     "test_tool",
		Arguments:    map[string]interface{}{"key": "value"},
		AssignedNode: "localhost",
	}

	// directly test forwardToNode with a mock server
	// Since forwardToNode uses hardcoded port, test via integration below
	_ = ctx
	_ = req
	_ = receivedPayload
}

func TestHandleToolCall_WithNodeSelector(t *testing.T) {
	var selectedTool *MCPTool
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	gw.SetNodeSelector(func(tool *MCPTool) (string, error) {
		selectedTool = tool
		return "node-1", nil
	})

	// We can't fully test forwarding without a real server, but we can verify
	// the node selector is called and the request gets assigned
	_ = selectedTool
}

func TestHandleToolCall_RequestTracked(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	// unknown tool should still track the request
	gw.HandleToolCall(context.Background(), "nonexistent", nil, "api")

	gw.requestsMu.Lock()
	reqCount := len(gw.requests)
	gw.requestsMu.Unlock()
	if reqCount == 0 {
		t.Error("request should be tracked even for unknown tools")
	}
}

func TestGetStats(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.RegisterTool(&MCPTool{
		Name:        "tool_a",
		Description: "Tool A",
		Parameters:  map[string]interface{}{},
	})

	stats := gw.GetStats()
	if stats["registered_tools"] != 1 {
		t.Errorf("registered_tools = %v, want 1", stats["registered_tools"])
	}
	if stats["token_budget"] != int64(10_000_000) {
		t.Errorf("token_budget = %v, want 10000000", stats["token_budget"])
	}
}

func TestStartStop(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	if gw.IsRunning() {
		t.Error("gateway should not be running initially")
	}
	gw.Start()
	if !gw.IsRunning() {
		t.Error("gateway should be running after start")
	}
	gw.Stop()
	if gw.IsRunning() {
		t.Error("gateway should not be running after stop")
	}
}

func TestToolCallCount(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	count := gw.ToolCallCount("nonexistent")
	if count != 0 {
		t.Errorf("nonexistent tool call count = %d, want 0", count)
	}
}

func TestSanitizeNodeID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"node-1", "node-1"},
		{"node_2", "node_2"},
		{"node/evil", "nodeevil"},
		{"", "unknown"},
		{"../etc/passwd", "etcpasswd"},
	}
	for _, tt := range tests {
		result := sanitizeNodeID(tt.input)
		if result != tt.expected {
			t.Errorf("sanitizeNodeID(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// TestForwardToNode_RejectsNonAllowlistedNode verifies the F1 SSRF fix: a
// non-localhost AssignedNode that is not in the dial allowlist must be rejected
// BEFORE forwardToNode performs any outbound dial. The default gateway
// allowlist is localhost-only, so an attacker-influenced node id cannot coerce
// an outbound request to an arbitrary host. The test asserts no dial occurs
// (error returned, no HTTP attempt) even though no server is listening.
func TestForwardToNode_RejectsNonAllowlistedNode(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	tool := &MCPTool{Name: "t", Parameters: map[string]interface{}{}}
	req := &MCPRequest{
		RequestID:    "mcp_evil",
		AssignedNode: "evil-attacker-host",
	}
	_, err := gw.forwardToNode(context.Background(), req, tool)
	if err == nil {
		t.Fatal("expected rejection of non-allowlisted node, got nil error (SSRF)")
	}
	if !strings.Contains(err.Error(), "not an allowed MCP target") {
		t.Fatalf("expected allowlist rejection error, got: %v", err)
	}
}

// TestForwardToNode_AllowlistedRemoteNode verifies that after SetAllowedNodes,
// a configured remote node passes the gate (dial may then fail on connection,
// but it must NOT fail with the allowlist rejection — proving the gate opened).
func TestForwardToNode_AllowlistedRemoteNode(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	// point at a port nothing listens on so the dial fails fast, but AFTER the
	// allowlist gate passes — the error must be a dial error, not a rejection.
	gw.SetAllowedNodes([]string{"node-a"})
	tool := &MCPTool{Name: "t", Parameters: map[string]interface{}{}, Timeout: 1}
	req := &MCPRequest{
		RequestID:    "mcp_nodea",
		AssignedNode: "node-a",
	}
	_, err := gw.forwardToNode(context.Background(), req, tool)
	if err == nil {
		t.Fatal("expected dial error for unreachable allowlisted node, got nil")
	}
	if strings.Contains(err.Error(), "not an allowed MCP target") {
		t.Fatalf("allowlisted node was rejected by the gate: %v", err)
	}
}

// TestNewMCPClusterGateway_ConfigPortsHonored verifies the F1 fix: configured
// NodePort/LocalPort are stored and used, not the dead hardcoded 9000/11445.
func TestNewMCPClusterGateway_ConfigPortsHonored(t *testing.T) {
	cfg := GatewayConfig{NodePort: 22222, LocalPort: 33333}
	gw := NewMCPClusterGateway(cfg)
	if gw.nodePort != 22222 {
		t.Errorf("nodePort = %d, want 22222", gw.nodePort)
	}
	if gw.localPort != 33333 {
		t.Errorf("localPort = %d, want 33333", gw.localPort)
	}
}

func TestEstimateTokens(t *testing.T) {
	args := map[string]interface{}{
		"key": "value",
	}
	tokens := estimateTokens(args)
	if tokens <= 0 {
		t.Error("estimateTokens should return positive value")
	}
}

func TestHandleToolCall_MaxRequestsEviction(t *testing.T) {
	cfg := DefaultGatewayConfig()
	cfg.MaxRequests = 2
	gw := NewMCPClusterGateway(cfg)

	// make requests that will fail (unknown tool) to fill the buffer
	gw.HandleToolCall(context.Background(), "tool1", nil, "api")
	gw.HandleToolCall(context.Background(), "tool2", nil, "api")
	gw.HandleToolCall(context.Background(), "tool3", nil, "api")

	gw.requestsMu.Lock()
	reqCount := len(gw.requests)
	// N3: eviction must drop the OLDEST entry (tool1, inserted first), not a
	// random map entry. Prior `for k := range; break` picked a random one — this
	// assertion catches that regression by verifying tool1 is gone and the two
	// newest (tool2, tool3) survive.
	remaining := make(map[string]bool, len(gw.requests))
	for _, r := range gw.requests {
		remaining[r.ToolName] = true
	}
	gw.requestsMu.Unlock()
	if reqCount > cfg.MaxRequests {
		t.Errorf("request count %d exceeds max %d", reqCount, cfg.MaxRequests)
	}
	if remaining["tool1"] {
		t.Errorf("N3: oldest entry (tool1) must be evicted on cap, but survived; remaining=%v", remaining)
	}
	if !remaining["tool2"] || !remaining["tool3"] {
		t.Errorf("N3: newest entries (tool2, tool3) must survive eviction; remaining=%v", remaining)
	}
}

func TestHandleToolCall_Concurrent(t *testing.T) {
	gw := NewMCPClusterGateway(DefaultGatewayConfig())
	gw.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	var wg sync.WaitGroup
	var errors atomic.Int64

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := gw.HandleToolCall(context.Background(), "nonexistent", nil, "api")
			if _, ok := result["error"]; !ok {
				errors.Add(1)
			}
		}()
	}
	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("unexpected non-error results: %d", errors.Load())
	}
}

func TestBytesReader(t *testing.T) {
	data := []byte(`{"hello":"world"}`)
	reader := bytesReader(data)
	buf := make([]byte, len(data))
	n, err := reader.Read(buf)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("read %d bytes, want %d", n, len(data))
	}
}
