package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Handler struct {
	gateway *MCPClusterGateway
}

func NewHandler(gateway *MCPClusterGateway) *Handler {
	return &Handler{gateway: gateway}
}

// SetManagedToolAllowlist forwards the #129 Gap 3 managed-MCP tool allowlist
// update to the underlying gateway. Called from server.RebuildMiddlewareChain
// on config hot-reload so toggling mcp.managed_tool_allowlist takes effect
// without a restart. No-op when MCP is disabled (gateway nil).
func (h *Handler) SetManagedToolAllowlist(tools []string) {
	if h == nil || h.gateway == nil {
		return
	}
	h.gateway.SetManagedToolAllowlist(tools)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.register(mux, nil)
}

// RegisterRoutesWithMiddleware registers the MCP routes through the supplied
// wrapper so they pass through the gateway middleware chain (auth, rate limit,
// budget). F1 fix: MCP routes were previously mounted on the bare mux, which
// bypassed withMiddleware entirely — an unauthenticated /mcp/v1/call could
// trigger forwardToNode's outbound dial (SSRF amplifier). health stays public
// only if the wrapper itself permits unauthenticated reads; the wrapper is the
// single auth gate, applied uniformly to every MCP route.
func (h *Handler) RegisterRoutesWithMiddleware(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	h.register(mux, wrap)
}

// RegisterRoutesWithGate registers MCP routes on the SHARED main mux, layered as:
// shared withMiddleware chain (rate limit, budget, observability) THEN the MCP
// auth gate. The MCP gate is applied AFTER the shared chain so the main-chain
// context (request id, principal) is present, but the gate's credential check
// is independent of auth.enabled — MCP stays locked even when the main chain is
// open (auth.enabled=false). #118 shared-listener path.
func (h *Handler) RegisterRoutesWithGate(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc, gate func(http.Handler) http.Handler) {
	h.register(mux, func(hf http.HandlerFunc) http.HandlerFunc {
		wrapped := hf
		if wrap != nil {
			wrapped = wrap(wrapped)
		}
		return func(w http.ResponseWriter, r *http.Request) {
			gate(http.Handler(wrapped)).ServeHTTP(w, r)
		}
	})
}

// RegisterRoutesMCPOnly registers MCP routes with the MCP auth gate as the SOLE
// middleware (no shared chain). Used by the dedicated MCP listener (#118) which
// is security-domain-isolated from the main :11432 mux — it does not need the
// main rate-limiter/budget/observability chain (those are inference concerns).
func (h *Handler) RegisterRoutesMCPOnly(mux *http.ServeMux, gate func(http.Handler) http.Handler) {
	h.register(mux, func(hf http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			gate(http.Handler(hf)).ServeHTTP(w, r)
		}
	})
}

func (h *Handler) register(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	regs := []struct {
		path string
		h    http.HandlerFunc
	}{
		{"/mcp/v1/tools", h.handleToolsList},
		{"/mcp/v1/tools/register", h.handleToolRegister},
		{"/mcp/v1/tools/unregister", h.handleToolUnregister},
		{"/mcp/v1/call", h.handleToolCall},
		{"/mcp/v1/stats", h.handleStats},
		{"/mcp/v1/health", h.handleHealth},
	}
	for _, r := range regs {
		hh := r.h
		if wrap != nil {
			hh = wrap(hh)
		}
		mux.HandleFunc(r.path, hh)
	}
	slog.Info("MCP gateway routes registered")
}

func (h *Handler) handleToolsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	tools := h.gateway.GetToolsList()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tools": tools,
	})
}

func (h *Handler) handleToolRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		slog.Error("MCP register: failed to read body", "error", err)
		http.Error(w, `{"error":"failed to read request"}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var tool MCPTool
	if err := json.Unmarshal(body, &tool); err != nil {
		slog.Error("MCP register: invalid JSON", "error", err)
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if tool.Name == "" {
		http.Error(w, `{"error":"tool name is required"}`, http.StatusBadRequest)
		return
	}

	h.gateway.RegisterTool(&tool)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "registered",
		"name":   tool.Name,
	})
}

func (h *Handler) handleToolUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// F1 fix: bound the read so an unbounded body cannot exhaust memory
	// (siblings handleToolRegister/handleToolCall already cap at 1MiB/5MiB).
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, `{"error":"tool name is required"}`, http.StatusBadRequest)
		return
	}

	h.gateway.UnregisterTool(req.Name)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "unregistered",
		"name":   req.Name,
	})
}

func (h *Handler) handleToolCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		slog.Error("MCP call: failed to read body", "error", err)
		http.Error(w, `{"error":"failed to read request"}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req struct {
		ToolName  string                 `json:"tool_name"`
		Arguments map[string]interface{} `json:"arguments"`
		Source    string                 `json:"source"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Error("MCP call: invalid JSON", "error", err)
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.ToolName == "" {
		http.Error(w, `{"error":"tool_name is required"}`, http.StatusBadRequest)
		return
	}

	if req.Source == "" {
		req.Source = "api"
	}

	// check source validity
	validSources := map[string]bool{"claude_desktop": true, "claude_code": true, "api": true}
	if !validSources[req.Source] {
		http.Error(w, fmt.Sprintf(`{"error":"invalid source: %s"}`, req.Source), http.StatusBadRequest)
		return
	}

	result := h.gateway.HandleToolCall(r.Context(), req.ToolName, req.Arguments, req.Source)

	// check if result contains error
	if errVal, ok := result["error"]; ok {
		errMsg := fmt.Sprintf("%v", errVal)
		if strings.Contains(errMsg, "unknown tool") {
			writeJSON(w, http.StatusNotFound, result)
			return
		}
		if strings.Contains(errMsg, "token budget") {
			writeJSON(w, http.StatusTooManyRequests, result)
			return
		}
		writeJSON(w, http.StatusInternalServerError, result)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	stats := h.gateway.GetStats()
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	stats := h.gateway.GetStats()
	status := "ok"
	tokenBudget, _ := stats["token_budget"].(int64)
	tokenRemaining, _ := stats["token_remaining"].(int64)
	if tokenBudget <= 0 || tokenRemaining <= 0 {
		status = "token_budget_exhausted"
	}

	running := h.gateway.IsRunning()
	if !running {
		status = "stopped"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          status,
		"running":         running,
		"registered_tools": stats["registered_tools"],
		"token_remaining": stats["token_remaining"],
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("MCP handler: failed to encode JSON response", "error", err)
	}
}

// HealthCheck probes a remote MCP node for health status
func HealthCheck(ctx context.Context, nodeAddr string) error {
	url := strings.TrimRight(nodeAddr, "/") + "/mcp/v1/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create MCP health check request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("MCP health check to %s failed: %w", nodeAddr, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MCP health check returned status %d", resp.StatusCode)
	}
	return nil
}
