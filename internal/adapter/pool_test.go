package adapter

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestPool_RegisterAndGet(t *testing.T) {
    pool := NewPool()

    p := &mockProvider{name: "test-provider"}
    pool.Register("test", p, config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://localhost:11434"})

    got, ok := pool.Get("test")
    if !ok {
        t.Fatal("expected provider to be found")
    }
    if got.Name() != "test-provider" {
        t.Errorf("expected test-provider, got %s", got.Name())
    }
}

func TestPool_GetByBackend(t *testing.T) {
    pool := NewPool()

    p := &mockProvider{name: "mlx"}
    pool.Register("fusion-mlx", p, config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://localhost:11434"})

    got, err := pool.GetByBackend("fusion-mlx")
    if err != nil {
        t.Fatal(err)
    }
    if got.Name() != "mlx" {
        t.Errorf("expected mlx, got %s", got.Name())
    }
}

func TestPool_ListProviders(t *testing.T) {
    pool := NewPool()

    pool.Register("a", &mockProvider{name: "a"}, config.BackendConfig{Type: "test", BaseURL: "http://a"})
    pool.Register("b", &mockProvider{name: "b"}, config.BackendConfig{Type: "test", BaseURL: "http://b"})

    list := pool.ListProviders()
    if len(list) != 2 {
        t.Errorf("expected 2 providers, got %d", len(list))
    }
}

func TestPool_GetFusionMLX(t *testing.T) {
    pool := NewPool()
    if mlx := pool.GetFusionMLX(); mlx != nil {
        t.Error("expected nil when no fusion-mlx provider registered")
    }

    mlx := NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: "http://localhost:11434",
    }, config.RoutingConfig{})
    pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://localhost:11434"})

    got := pool.GetFusionMLX()
    if got == nil {
        t.Fatal("expected fusion-mlx provider, got nil")
    }
    if got.Name() != "fusion-mlx" {
        t.Errorf("expected fusion-mlx, got %s", got.Name())
    }
}

type mockProvider struct {
    name string
}

func (m *mockProvider) Name() string                                          { return m.name }
func (m *mockProvider) HealthCheck(_ context.Context) error                   { return nil }
func (m *mockProvider) Chat(_ context.Context, _ *ChatRequest) (*ChatResponse, error) {
    return nil, nil
}
func (m *mockProvider) StreamChat(_ context.Context, _ *ChatRequest) (<-chan StreamChunk, error) {
    return nil, nil
}
func (m *mockProvider) Embedding(_ context.Context, _ *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, nil
}
func (m *mockProvider) ListModels(_ context.Context) ([]ModelInfo, error) {
    return nil, nil
}
func (m *mockProvider) Rerank(_ context.Context, _ *RerankRequest) (*RerankResponse, error) {
    return nil, nil
}
