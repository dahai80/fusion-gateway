package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fusion-gateway/fusion-gateway/internal/adapter"
	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestInferModuleFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/api/v1/inference/completions", "chat"},
		{"/api/v1/chat/completions", "chat"},
		{"/api/v1/code/generate", "code"},
		{"/api/v1/rag/query", "rag"},
		{"/api/v1/agent/run", "agent"},
		{"/api/v1/design/render", "design"},
		{"/api/v1/models", ""},
		{"/api/v1/system/health", ""},
		{"/api/v1/", ""},
	}
	for _, tt := range tests {
		got := inferModuleFromPath(tt.path)
		if got != tt.expected {
			t.Errorf("inferModuleFromPath(%q) = %q, want %q", tt.path, got, tt.expected)
		}
	}
}

// TestHandleMLXStats_ProxiesToBackend verifies that /stats is transparently
// proxied to the fusion-mlx backend (#34), including the backend's response
// body and status code.
func TestHandleMLXStats_ProxiesToBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats" {
			t.Errorf("backend received path %q, want /stats", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"memory":"ok"}`))
	}))
	defer backend.Close()

	s := newTestServer()
	mlxProvider := adapter.NewFusionMLXProvider(config.BackendConfig{
		Type:    "fusion-mlx",
		BaseURL: backend.URL,
		APIKey:  "test-backend-key",
		Enabled: true,
	}, config.RoutingConfig{})
	s.pool.Register("fusion-mlx", mlxProvider, config.BackendConfig{
		Type:    "fusion-mlx",
		BaseURL: backend.URL,
		APIKey:  "test-backend-key",
		Enabled: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()
	s.handleMLXStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"memory":"ok"}` {
		t.Errorf("proxied body = %q, want {\"memory\":\"ok\"}", got)
	}
}

// TestHandleMLXStats_NoBackend returns 503 when fusion-mlx is not configured.
func TestHandleMLXStats_NoBackend(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()
	s.handleMLXStats(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
