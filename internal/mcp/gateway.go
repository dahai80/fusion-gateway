package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/httpx"
	"github.com/google/uuid"
)

type MCPTool struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	Parameters       map[string]interface{} `json:"parameters"`
	NodeID           string                 `json:"node_id,omitempty"`
	Plugin           string                 `json:"plugin,omitempty"`
	RequiredMemoryGB float64                `json:"required_memory_gb,omitempty"`
	RequiredGPU      bool                   `json:"required_gpu,omitempty"`
	Timeout          float64                `json:"timeout,omitempty"`
	// S2: callCount is incremented in HandleToolCall (after forwardToNode,
	// outside toolsMu) and read by ToolCallCount (under RLock). A bare int64
	// increment is a data race under -race / concurrent tool calls. atomic
	// makes the increment + read lock-free and race-free without widening the
	// toolsMu critical section.
	callCount        atomic.Int64
}

type MCPRequestStatus string

const (
	MCPRequestPending   MCPRequestStatus = "pending"
	MCPRequestCompleted MCPRequestStatus = "completed"
	MCPRequestFailed    MCPRequestStatus = "failed"
)

type MCPRequest struct {
	RequestID    string                 `json:"request_id"`
	ToolName     string                 `json:"tool_name"`
	Arguments    map[string]interface{} `json:"arguments"`
	Source       string                 `json:"source"`
	AssignedNode string                 `json:"assigned_node,omitempty"`
	Status       MCPRequestStatus       `json:"status"`
	CreatedAt    time.Time              `json:"created_at"`
	CompletedAt  time.Time              `json:"completed_at,omitempty"`
	TokenCount   int                    `json:"token_count,omitempty"`
	Error        string                 `json:"error,omitempty"`
}

type NodeSelectorFunc func(tool *MCPTool) (nodeID string, err error)

type MCPClusterGateway struct {
	host            string
	port            int
	// F1 fix: node/local ports come from config (GatewayConfig), not hardcoded
	// literals inside forwardToNode. Dead config is a footgun — the operator
	// sets NodePort/LocalPort expecting them to take effect; honoring them
	// keeps the dial target honest and auditable.
	nodePort        int
	localPort       int
	// F1 fix: allowlist of node IDs forwardToNode may dial. nil = localhost
	// only (the only wired deployment — SetNodeSelector is never called in
	// production). A non-localhost AssignedNode not in this set is rejected
	// before any dial, closing the SSRF-amplification vector: an unauthenticated
	// /mcp/v1/call can no longer coerce the gateway into an outbound request to
	// an attacker-chosen host even if a node selector were ever installed.
	allowedNodes    map[string]bool
	// N1: allowedNodes is written by SetAllowedNodes (wiring / hot-reload)
	// and read by forwardToNode (per request) with no synchronization — a
	// concurrent reload races the read. Dedicated mutex so the dial-allowlist
	// check stays honest under reload.
	allowedNodesMu  sync.RWMutex
	tools          map[string]*MCPTool
	toolsMu        sync.RWMutex
	requests       map[string]*MCPRequest
	requestsMu     sync.Mutex
	maxRequests    int
	nodeSelector   NodeSelectorFunc
	// S2: totalTokenCount is incremented in HandleToolCall and read in
	// HandleToolCall (budget gate) + GetStats, both outside any lock guarding
	// the counter itself. atomic.Int64 closes the data race.
	totalTokenCount atomic.Int64
	tokenBudget    int64
	httpClient     *http.Client
	running        bool
	runningMu      sync.Mutex
	// #129 Gap 3: managed-MCP per-node tool allowlist. nil/empty = unrestricted
	// (admit every registered tool). When non-empty, HandleToolCall rejects a
	// tool whose Name is not in the set before any forward. Guarded by its own
	// mutex so hot-reload (SetManagedToolAllowlist) does not race admission.
	managedAllowlist    map[string]bool
	managedAllowlistMu  sync.RWMutex
}

type GatewayConfig struct {
	Host        string `mapstructure:"host"`
	Port        int    `mapstructure:"port"`
	TokenBudget int64  `mapstructure:"token_budget"`
	MaxRequests int    `mapstructure:"max_requests"`
	NodePort    int    `mapstructure:"node_port"`
	LocalPort   int    `mapstructure:"local_port"`
	// #129 Gap 3: enterprise tool names permitted on this node. Empty =
	// unrestricted. Set identically across nodes via deployment; converge on
	// next restart. No runtime fan-out (Rule 2: no pub/sub for one consumer).
	ManagedToolAllowlist []string `mapstructure:"managed_tool_allowlist"`
}

func DefaultGatewayConfig() GatewayConfig {
	return GatewayConfig{
		Host:        "127.0.0.1",
		Port:        11446,
		TokenBudget: 10_000_000,
		MaxRequests: 10000,
		NodePort:    11445,
		LocalPort:   9000,
	}
}

func NewMCPClusterGateway(cfg GatewayConfig) *MCPClusterGateway {
	if cfg.TokenBudget <= 0 {
		cfg.TokenBudget = 10_000_000
	}
	if cfg.MaxRequests <= 0 {
		cfg.MaxRequests = 10000
	}
	if cfg.NodePort <= 0 {
		cfg.NodePort = 11445
	}
	if cfg.LocalPort <= 0 {
		cfg.LocalPort = 9000
	}
	return &MCPClusterGateway{
		host:         cfg.Host,
		port:         cfg.Port,
		nodePort:     cfg.NodePort,
		localPort:    cfg.LocalPort,
		// F1 fix: localhost is always permitted; remote nodes must be
		// explicitly allowlisted via SetAllowedNodes before forwardToNode will
		// dial them. Default = localhost-only (fail-closed for SSRF).
		allowedNodes: map[string]bool{"localhost": true},
		tools:        make(map[string]*MCPTool),
		requests:     make(map[string]*MCPRequest),
		maxRequests:  cfg.MaxRequests,
		tokenBudget:  cfg.TokenBudget,
		// N2 (RR11): the shared MCP forward client MUST route through
		// httpx.TransportForBackend so per-host connections are capped
		// (MaxConnsPerHost) + bounded dial/header/TLS timeouts. A bare
		// &http.Client{Timeout} inherits DefaultTransport which has no
		// MaxConnsPerHost — a node-forward burst could exhaust the process FD
		// table. BaseURL is informational here (transport is shared across
		// nodes; per-node target URL is set on each request), so the cap is
		// the durable invariant.
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: httpx.TransportForBackend(config.BackendConfig{BaseURL: "mcp-cluster"}),
		},
	}
}

// SetManagedToolAllowlist installs the #129 Gap 3 managed-MCP per-node tool
// allowlist. Empty/nil = unrestricted (admit every registered tool). Used at
// wiring time from config and on hot-reload so toggling the allowlist takes
// effect without a restart. Concurrent-safe with HandleToolCall admission.
func (g *MCPClusterGateway) SetManagedToolAllowlist(tools []string) {
	set := make(map[string]bool, len(tools))
	for _, t := range tools {
		set[t] = true
	}
	g.managedAllowlistMu.Lock()
	g.managedAllowlist = set
	g.managedAllowlistMu.Unlock()
	slog.Info("MCP managed tool allowlist updated",
		"count", len(set),
		"restricted", len(set) > 0,
	)
}

// admitTool is the #129 Gap 3 admission gate: returns true when the tool is
// permitted under the managed allowlist. Empty allowlist = unrestricted (nil
// guard). Non-empty = tool name must be present. Called from HandleToolCall
// before token-budget + node selection so a rejected call never dials a node.
func (g *MCPClusterGateway) admitTool(toolName string) bool {
	g.managedAllowlistMu.RLock()
	allow := g.managedAllowlist
	g.managedAllowlistMu.RUnlock()
	if len(allow) == 0 {
		return true
	}
	return allow[toolName]
}

// SetAllowedNodes replaces the forwardToNode dial allowlist. "localhost" is
// always retained. Call this once during wiring if remote MCP nodes are
// deployed; absent a call, forwardToNode rejects every non-localhost node.
func (g *MCPClusterGateway) SetAllowedNodes(nodes []string) {
	set := map[string]bool{"localhost": true}
	for _, n := range nodes {
		set[sanitizeNodeID(n)] = true
	}
	// N1: guard the write so a concurrent forwardToNode read (RLock) never
	// races the map replacement. Log the change for auditability.
	g.allowedNodesMu.Lock()
	g.allowedNodes = set
	g.allowedNodesMu.Unlock()
	slog.Info("MCP allowed nodes updated",
		"count", len(set),
		"localhost_only", len(set) == 1,
	)
}

func (g *MCPClusterGateway) RegisterTool(tool *MCPTool) {
	g.toolsMu.Lock()
	defer g.toolsMu.Unlock()
	g.tools[tool.Name] = tool
	slog.Info("MCP tool registered", "name", tool.Name, "plugin", tool.Plugin)
}

func (g *MCPClusterGateway) UnregisterTool(name string) {
	g.toolsMu.Lock()
	defer g.toolsMu.Unlock()
	delete(g.tools, name)
	slog.Info("MCP tool unregistered", "name", name)
}

func (g *MCPClusterGateway) GetToolsList() []map[string]interface{} {
	g.toolsMu.RLock()
	defer g.toolsMu.RUnlock()

	result := make([]map[string]interface{}, 0, len(g.tools))
	for _, t := range g.tools {
		result = append(result, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"input_schema": t.Parameters,
		})
	}
	return result
}

func (g *MCPClusterGateway) SetNodeSelector(selector NodeSelectorFunc) {
	g.nodeSelector = selector
}

func (g *MCPClusterGateway) HandleToolCall(ctx context.Context, toolName string, arguments map[string]interface{}, source string) map[string]interface{} {
	requestID := fmt.Sprintf("mcp_%s", uuid.New().String()[:12])
	req := &MCPRequest{
		RequestID: requestID,
		ToolName:  toolName,
		Arguments: arguments,
		Source:    source,
		Status:    MCPRequestPending,
		CreatedAt: time.Now(),
	}

	g.requestsMu.Lock()
	g.requests[requestID] = req
	if len(g.requests) > g.maxRequests {
		// N3: evict the OLDEST request by CreatedAt, matching the "evict
		// oldest" intent. The prior loop `for k := range ...; break` deleted a
		// RANDOM map entry (Go map iteration order is randomized) — so a
		// fresh request could be evicted while a stale one stayed, and the
		// comment lied. Track the min-CreatedAt key and drop that one.
		var oldestKey string
		var oldestTime time.Time
		for k, r := range g.requests {
			if k == requestID {
				continue // never evict the entry we just inserted
			}
			if oldestKey == "" || r.CreatedAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = r.CreatedAt
			}
		}
		if oldestKey != "" {
			delete(g.requests, oldestKey)
			slog.Info("MCP request map evicted oldest entry on cap",
				"evicted_id", oldestKey,
				"created_at", oldestTime,
				"size", len(g.requests),
				"max", g.maxRequests,
			)
		}
	}
	g.requestsMu.Unlock()

	// #129 Gap 3: managed-MCP per-node tool allowlist admission. Empty list =
	// unrestricted (current behavior). Non-empty = reject unlisted tool BEFORE
	// token-budget + node selection so a denied call never dials a node.
	if !g.admitTool(toolName) {
		req.Status = MCPRequestFailed
		req.Error = fmt.Sprintf("tool %s not in managed allowlist", toolName)
		slog.Warn("MCP call rejected: tool not in managed allowlist",
			"tool", toolName,
			"source", source,
			"request_id", requestID,
		)
		return map[string]interface{}{"error": fmt.Sprintf("tool %s is not permitted on this node", toolName)}
	}

	// check token budget
	used := g.totalTokenCount.Load()
	if g.tokenBudget <= 0 || used >= g.tokenBudget {
		req.Status = MCPRequestFailed
		req.Error = "token budget exhausted"
		slog.Warn("MCP call rejected: token budget exhausted",
			"tool", toolName,
			"total_tokens", used,
			"budget", g.tokenBudget,
		)
		return map[string]interface{}{"error": "token budget exhausted"}
	}

	// find tool
	g.toolsMu.RLock()
	tool, ok := g.tools[toolName]
	g.toolsMu.RUnlock()

	if !ok {
		req.Status = MCPRequestFailed
		req.Error = fmt.Sprintf("unknown tool: %s", toolName)
		slog.Warn("MCP call rejected: unknown tool", "tool", toolName)
		return map[string]interface{}{"error": fmt.Sprintf("unknown tool: %s", toolName)}
	}

	// select node
	var nodeID string
	if g.nodeSelector != nil {
		var err error
		nodeID, err = g.nodeSelector(tool)
		if err != nil {
			req.Status = MCPRequestFailed
			req.Error = fmt.Sprintf("node selection failed: %s", err)
			slog.Error("MCP node selection failed", "tool", toolName, "error", err)
			return map[string]interface{}{"error": "node selection failed"}
		}
	} else {
		nodeID = "localhost"
	}
	req.AssignedNode = nodeID

	slog.Info("MCP tool call",
		"tool", toolName,
		"node", nodeID,
		"source", source,
		"request_id", requestID,
	)

	// forward to node
	result, err := g.forwardToNode(ctx, req, tool)
	if err != nil {
		req.Status = MCPRequestFailed
		req.Error = err.Error()
		slog.Error("MCP tool call failed",
			"tool", toolName,
			"node", nodeID,
			"error", err,
		)
		return map[string]interface{}{"error": "internal error"}
	}

	req.Status = MCPRequestCompleted
	req.CompletedAt = time.Now()
	// S2: atomic increment — callCount is read by ToolCallCount under RLock;
	// the increment here runs outside toolsMu, so a bare ++ is a data race.
	tool.callCount.Add(1)

	// estimate token consumption
	estimatedTokens := estimateTokens(arguments)
	req.TokenCount = estimatedTokens
	// S2: atomic add — totalTokenCount is read by the budget gate above and
	// GetStats, both lock-free; the add here races them under bare int64.
	g.totalTokenCount.Add(int64(estimatedTokens))

	slog.Info("MCP tool call completed",
		"tool", toolName,
		"node", nodeID,
		"tokens_estimated", estimatedTokens,
		"request_id", requestID,
	)

	return result
}

func (g *MCPClusterGateway) forwardToNode(ctx context.Context, req *MCPRequest, tool *MCPTool) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"tool":       tool.Name,
		"plugin":     tool.Plugin,
		"arguments":  req.Arguments,
		"request_id": req.RequestID,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal forward payload: %w", err)
	}

	// F1 fix: validate the assigned node against the dial allowlist BEFORE
	// building any URL. Non-localhost nodes require an explicit SetAllowedNodes
	// entry; the default allowlist is localhost-only, so an attacker-influenced
	// AssignedNode (e.g. via a future node selector) cannot coerce an outbound
	// dial to an arbitrary host — SSRF amplification closed.
	node := sanitizeNodeID(req.AssignedNode)
	// N1: read the allowlist under RLock so a concurrent SetAllowedNodes
	// reload cannot race this map read.
	g.allowedNodesMu.RLock()
	allowed := g.allowedNodes[node]
	g.allowedNodesMu.RUnlock()
	if !allowed {
		slog.Warn("MCP forward rejected: node not allowlisted",
			"node", req.AssignedNode,
			"sanitized", node,
			"request_id", req.RequestID,
		)
		return nil, fmt.Errorf("node %s is not an allowed MCP target", req.AssignedNode)
	}

	var targetURL string
	if node == "localhost" {
		targetURL = fmt.Sprintf("http://localhost:%d/api/mcp/tools/%s", g.localPort, tool.Name)
	} else {
		targetURL = fmt.Sprintf("http://%s:%d/api/mcp/execute", node, g.nodePort)
	}

	timeout := time.Duration(tool.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	// N2 (RR11): per-request client reuses the shared capped transport
	// (MaxConnsPerHost + R5 dial/header/TLS timeouts from the constructor's
	// httpx.TransportForBackend) but carries this tool's timeout. A bare
	// &http.Client{Timeout} would inherit DefaultTransport (no MaxConnsPerHost)
	// — a concurrent burst to one node could exhaust FDs.
	client := &http.Client{Timeout: timeout, Transport: g.httpClient.Transport}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytesReader(body))
	if err != nil {
		return nil, fmt.Errorf("create forward request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("forward to node %s: %w", req.AssignedNode, err)
	}
	defer resp.Body.Close()

	// F1 fix: cap the response body so a malicious/buggy node cannot exhaust
	// gateway memory with an unbounded stream. Mirrors the SSE hardening cap.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response from node %s: %w", req.AssignedNode, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node %s returned status %d: %s", req.AssignedNode, resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response from node %s: %w", req.AssignedNode, err)
	}

	return result, nil
}

func (g *MCPClusterGateway) GetStats() map[string]interface{} {
	g.toolsMu.RLock()
	toolCount := len(g.tools)
	g.toolsMu.RUnlock()

	g.requestsMu.Lock()
	totalReqs := len(g.requests)
	completed := 0
	failed := 0
	for _, r := range g.requests {
		switch r.Status {
		case MCPRequestCompleted:
			completed++
		case MCPRequestFailed:
			failed++
		}
	}
	g.requestsMu.Unlock()

	return map[string]interface{}{
		"registered_tools": toolCount,
		"total_requests":   totalReqs,
		"completed":        completed,
		"failed":           failed,
		"total_token_count": g.totalTokenCount.Load(),
		"token_budget":     g.tokenBudget,
		"token_remaining":  g.tokenBudget - g.totalTokenCount.Load(),
	}
}

func (g *MCPClusterGateway) Start() {
	g.runningMu.Lock()
	defer g.runningMu.Unlock()
	g.running = true
	slog.Info("MCP cluster gateway started", "host", g.host, "port", g.port)
}

func (g *MCPClusterGateway) Stop() {
	g.runningMu.Lock()
	defer g.runningMu.Unlock()
	g.running = false
	slog.Info("MCP cluster gateway stopped")
}

func (g *MCPClusterGateway) IsRunning() bool {
	g.runningMu.Lock()
	defer g.runningMu.Unlock()
	return g.running
}

func (g *MCPClusterGateway) ToolCallCount(name string) int64 {
	g.toolsMu.RLock()
	defer g.toolsMu.RUnlock()
	if t, ok := g.tools[name]; ok {
		// S2: load the atomic counter (race-free under concurrent increments).
		return t.callCount.Load()
	}
	return 0
}

func sanitizeNodeID(nodeID string) string {
	result := make([]byte, 0, len(nodeID))
	for i := 0; i < len(nodeID); i++ {
		c := nodeID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result = append(result, c)
		}
	}
	if len(result) == 0 {
		return "unknown"
	}
	return string(result)
}

func estimateTokens(arguments map[string]interface{}) int {
	data, _ := json.Marshal(arguments)
	return len(data) / 4
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func bytesReader(data []byte) *bytesReaderImpl {
	return &bytesReaderImpl{data: data}
}

func (r *bytesReaderImpl) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
