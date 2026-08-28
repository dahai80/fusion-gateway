package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestHandler() *Handler {
	cfg := DefaultGatewayConfig()
	gw := NewMCPClusterGateway(cfg)
	gw.Start()
	return NewHandler(gw)
}

func TestHandler_ToolsList(t *testing.T) {
	h := newTestHandler()
	h.gateway.RegisterTool(&MCPTool{
		Name:        "tool_a",
		Description: "Tool A",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp/v1/tools", nil)
	w := httptest.NewRecorder()
	h.handleToolsList(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	tools, ok := resp["tools"].([]interface{})
	if !ok {
		t.Fatal("tools field missing or wrong type")
	}
	if len(tools) != 1 {
		t.Errorf("tools count = %d, want 1", len(tools))
	}
}

func TestHandler_ToolsList_MethodNotAllowed(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/tools", nil)
	w := httptest.NewRecorder()
	h.handleToolsList(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_ToolRegister(t *testing.T) {
	h := newTestHandler()

	body := `{"name":"new_tool","description":"New tool","parameters":{"type":"object"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/tools/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleToolRegister(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "registered" {
		t.Errorf("status = %v, want registered", resp["status"])
	}
}

func TestHandler_ToolRegister_MissingName(t *testing.T) {
	h := newTestHandler()

	body := `{"description":"No name tool"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/tools/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleToolRegister(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_ToolRegister_InvalidJSON(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/tools/register", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	h.handleToolRegister(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_ToolUnregister(t *testing.T) {
	h := newTestHandler()
	h.gateway.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	body := `{"name":"test_tool"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/tools/unregister", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleToolUnregister(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "unregistered" {
		t.Errorf("status = %v, want unregistered", resp["status"])
	}
}

func TestHandler_ToolCall_UnknownTool(t *testing.T) {
	h := newTestHandler()

	body := `{"tool_name":"nonexistent","arguments":{},"source":"api"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleToolCall(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_ToolCall_MissingToolName(t *testing.T) {
	h := newTestHandler()

	body := `{"arguments":{},"source":"api"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleToolCall(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_ToolCall_InvalidSource(t *testing.T) {
	h := newTestHandler()
	h.gateway.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	body := `{"tool_name":"test_tool","arguments":{},"source":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleToolCall(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for invalid source", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_ToolCall_DefaultSource(t *testing.T) {
	h := newTestHandler()
	h.gateway.RegisterTool(&MCPTool{
		Name:        "test_tool",
		Description: "test",
		Parameters:  map[string]interface{}{},
	})

	// without source field — should default to "api" and fail forwarding
	body := `{"tool_name":"test_tool","arguments":{}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleToolCall(w, req)

	// will fail because there's no real server to forward to
	if w.Code == http.StatusBadRequest {
		t.Errorf("should not be bad request for missing source, got %d", w.Code)
	}
}

func TestHandler_Stats(t *testing.T) {
	h := newTestHandler()
	h.gateway.RegisterTool(&MCPTool{
		Name:        "tool_a",
		Description: "Tool A",
		Parameters:  map[string]interface{}{},
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp/v1/stats", nil)
	w := httptest.NewRecorder()
	h.handleStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["registered_tools"] != float64(1) {
		t.Errorf("registered_tools = %v, want 1", resp["registered_tools"])
	}
}

func TestHandler_Stats_MethodNotAllowed(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/stats", nil)
	w := httptest.NewRecorder()
	h.handleStats(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_Health(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/mcp/v1/health", nil)
	w := httptest.NewRecorder()
	h.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
	if resp["running"] != true {
		t.Errorf("running = %v, want true", resp["running"])
	}
}

func TestHandler_Health_Stopped(t *testing.T) {
	h := newTestHandler()
	h.gateway.Stop()

	req := httptest.NewRequest(http.MethodGet, "/mcp/v1/health", nil)
	w := httptest.NewRecorder()
	h.handleHealth(w, req)

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "stopped" {
		t.Errorf("status = %v, want stopped", resp["status"])
	}
}

func TestHandler_Health_TokenBudgetExhausted(t *testing.T) {
	cfg := DefaultGatewayConfig()
	gw := NewMCPClusterGateway(cfg)
	gw.tokenBudget = 0
	gw.totalTokenCount.Store(10)
	gw.Start()
	h := NewHandler(gw)

	req := httptest.NewRequest(http.MethodGet, "/mcp/v1/health", nil)
	w := httptest.NewRecorder()
	h.handleHealth(w, req)

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "token_budget_exhausted" {
		t.Errorf("status = %v, want token_budget_exhausted", resp["status"])
	}
}

func TestHandler_RegisterRoutes(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// test that routes are registered by making requests
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/mcp/v1/tools"},
		{http.MethodGet, "/mcp/v1/stats"},
		{http.MethodGet, "/mcp/v1/health"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusServiceUnavailable || w.Code == http.StatusBadGateway {
			t.Errorf("route %s not registered properly, got status %d", tt.path, w.Code)
		}
	}
}

func TestHealthCheck_RemoteSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp/v1/health" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	err := HealthCheck(context.Background(), server.URL)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealthCheck_RemoteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := HealthCheck(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for unhealthy remote")
	}
}

func TestHealthCheck_RemoteUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := HealthCheck(ctx, "http://127.0.0.1:1")
	if err == nil {
		t.Error("expected error for unreachable remote")
	}
}
