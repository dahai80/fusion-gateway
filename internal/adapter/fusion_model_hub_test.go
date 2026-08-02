package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestFusionModelHubProvider_Creation(t *testing.T) {
	cfg := config.BackendConfig{
		Type:    "fusion-model-hub",
		BaseURL: "http://127.0.0.1:11435",
		APIKey:  "test-key",
		Timeout: 5e9,
		Enabled: true,
	}
	p := NewFusionModelHubProvider("model-hub", cfg)
	if p == nil {
		t.Fatal("provider should not be nil")
	}
	if p.Name() != "model-hub" {
		t.Errorf("expected name model-hub, got %s", p.Name())
	}
	if p.BaseURL() != "http://127.0.0.1:11435" {
		t.Errorf("expected base URL http://127.0.0.1:11435, got %s", p.BaseURL())
	}
	if p.ReverseProxy() == nil {
		t.Error("reverse proxy should not be nil")
	}
}

func TestFusionModelHubProvider_ChatNotSupported(t *testing.T) {
	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "http://127.0.0.1:11435", Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	_, err := p.Chat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Error("Chat should return error")
	}
}

func TestFusionModelHubProvider_StreamChatNotSupported(t *testing.T) {
	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "http://127.0.0.1:11435", Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	_, err := p.StreamChat(context.Background(), &ChatRequest{})
	if err == nil {
		t.Error("StreamChat should return error")
	}
}

func TestFusionModelHubProvider_EmbeddingNotSupported(t *testing.T) {
	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "http://127.0.0.1:11435", Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	_, err := p.Embedding(context.Background(), &EmbeddingRequest{})
	if err == nil {
		t.Error("Embedding should return error")
	}
}

func TestFusionModelHubProvider_ListModelsNotSupported(t *testing.T) {
	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "http://127.0.0.1:11435", Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	_, err := p.ListModels(context.Background())
	if err == nil {
		t.Error("ListModels should return error")
	}
}

func TestFusionModelHubProvider_HealthCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: ts.URL, Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	if err := p.HealthCheck(context.Background()); err != nil {
		t.Errorf("health check should succeed: %v", err)
	}
}

func TestFusionModelHubProvider_HealthCheckFail(t *testing.T) {
	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "http://127.0.0.1:1", Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	if err := p.HealthCheck(context.Background()); err == nil {
		t.Error("health check should fail on unreachable host")
	}
}

func TestFusionModelHubProvider_InvalidBaseURL(t *testing.T) {
	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "://invalid", Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)
	if p == nil {
		t.Fatal("provider should not be nil even with invalid URL")
	}
}

func TestPool_GetModelHub(t *testing.T) {
	pool := NewPool()
	hub := pool.GetModelHub()
	if hub != nil {
		t.Error("should be nil when no model-hub registered")
	}

	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: "http://127.0.0.1:11435", Enabled: true}
	provider := NewFusionModelHubProvider("model-hub", cfg)
	pool.Register("model-hub", provider, cfg)

	hub = pool.GetModelHub()
	if hub == nil {
		t.Error("should return registered model-hub provider")
	}
}

func TestFusionModelHubProvider_ReverseProxyForwards(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	cfg := config.BackendConfig{Type: "fusion-model-hub", BaseURL: ts.URL, Enabled: true}
	p := NewFusionModelHubProvider("model-hub", cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	w := httptest.NewRecorder()
	p.ReverseProxy().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}
