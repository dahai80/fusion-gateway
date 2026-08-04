package adapter

import (
    "context"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type dummyProvider struct {
    name    string
    healthy error
}

func (d *dummyProvider) Name() string    { return d.name }
func (d *dummyProvider) HealthCheck(ctx context.Context) error { return d.healthy }
func (d *dummyProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return nil, nil
}
func (d *dummyProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return nil, nil
}
func (d *dummyProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, nil
}
func (d *dummyProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, nil
}
func (d *dummyProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return nil, nil
}

func TestNewPool(t *testing.T) {
    slog.Info("test NewPool")
    p := NewPool()
    if p == nil {
        t.Fatal("expected pool")
    }
}

func TestPool_RegisterAndGet(t *testing.T) {
    slog.Info("test Pool_RegisterAndGet")
    p := NewPool()
    dp := &dummyProvider{name: "test"}
    p.Register("test", dp, config.BackendConfig{Type: "dummy", BaseURL: "http://localhost"})
    got, ok := p.Get("test")
    if !ok {
        t.Fatal("provider not found")
    }
    if got.Name() != "test" {
        t.Errorf("expected test, got %s", got.Name())
    }
}

func TestPool_Get_NotFound(t *testing.T) {
    slog.Info("test Pool_Get_NotFound")
    p := NewPool()
    _, ok := p.Get("nonexistent")
    if ok {
        t.Error("should not find nonexistent provider")
    }
}

func TestPool_GetByBackend(t *testing.T) {
    slog.Info("test Pool_GetByBackend")
    p := NewPool()
    dp := &dummyProvider{name: "backend1"}
    p.Register("backend1", dp, config.BackendConfig{Type: "dummy"})
    got, err := p.GetByBackend("backend1")
    if err != nil {
        t.Fatal(err)
    }
    if got.Name() != "backend1" {
        t.Errorf("expected backend1, got %s", got.Name())
    }
}

func TestPool_GetByBackend_NotFound(t *testing.T) {
    slog.Info("test Pool_GetByBackend_NotFound")
    p := NewPool()
    _, err := p.GetByBackend("nonexistent")
    if err == nil {
        t.Error("expected error for nonexistent backend")
    }
}

func TestPool_ListProviders(t *testing.T) {
    slog.Info("test Pool_ListProviders")
    p := NewPool()
    p.Register("a", &dummyProvider{name: "a"}, config.BackendConfig{Type: "dummy"})
    p.Register("b", &dummyProvider{name: "b"}, config.BackendConfig{Type: "dummy"})
    names := p.ListProviders()
    if len(names) != 2 {
        t.Fatalf("expected 2, got %d", len(names))
    }
}

func TestPool_IsLocalProvider(t *testing.T) {
    slog.Info("test Pool_IsLocalProvider")
    p := NewPool()
    p.Register("mlx", &dummyProvider{name: "mlx"}, config.BackendConfig{Type: "fusion-mlx"})
    p.Register("kb", &dummyProvider{name: "kb"}, config.BackendConfig{Type: "fusion-kb"})
    p.Register("hub", &dummyProvider{name: "hub"}, config.BackendConfig{Type: "fusion-model-hub"})
    p.Register("cloud", &dummyProvider{name: "cloud"}, config.BackendConfig{Type: "openai-compatible"})

    cases := []struct {
        name string
        want bool
    }{
        {"mlx", true},
        {"kb", true},
        {"hub", true},
        {"cloud", false},
        {"nonexistent", false},
    }
    for _, c := range cases {
        if got := p.IsLocalProvider(c.name); got != c.want {
            t.Errorf("IsLocalProvider(%q) = %v, want %v", c.name, got, c.want)
        }
    }
}

func TestPool_GetFusionMLX(t *testing.T) {
    slog.Info("test Pool_GetFusionMLX")
    p := NewPool()
    p.Register("mlx", NewFusionMLXProvider(config.BackendConfig{BaseURL: "http://localhost:11434"}, config.RoutingConfig{}), config.BackendConfig{Type: "fusion-mlx"})
    mlx := p.GetFusionMLX()
    if mlx == nil {
        t.Error("expected FusionMLX provider")
    }
}

func TestPool_GetFusionMLX_None(t *testing.T) {
    slog.Info("test Pool_GetFusionMLX_None")
    p := NewPool()
    p.Register("other", &dummyProvider{name: "other"}, config.BackendConfig{Type: "dummy"})
    mlx := p.GetFusionMLX()
    if mlx != nil {
        t.Error("expected nil for no FusionMLX provider")
    }
}

func TestPool_HealthCheckAll(t *testing.T) {
    slog.Info("test Pool_HealthCheckAll")
    p := NewPool()
    p.Register("healthy", &dummyProvider{name: "healthy", healthy: nil}, config.BackendConfig{Type: "dummy"})
    p.Register("sick", &dummyProvider{name: "sick", healthy: context.DeadlineExceeded}, config.BackendConfig{Type: "dummy"})
    results := p.HealthCheckAll(context.Background())
    if results["healthy"] != nil {
        t.Error("healthy should be nil")
    }
    if results["sick"] == nil {
        t.Error("sick should have error")
    }
}

func TestPool_HealthCheckAll_Empty(t *testing.T) {
    slog.Info("test Pool_HealthCheckAll_Empty")
    p := NewPool()
    results := p.HealthCheckAll(context.Background())
    if len(results) != 0 {
        t.Errorf("expected 0 results, got %d", len(results))
    }
}

func TestPool_BuildProviders_FusionMLX(t *testing.T) {
    slog.Info("test Pool_BuildProviders_FusionMLX")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "local": {
                    Enabled: true,
                    Type:    "fusion-mlx",
                    BaseURL: srv.URL,
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    got, ok := p.Get("local")
    if !ok {
        t.Fatal("fusion-mlx provider not built")
    }
    if got.Name() != "fusion-mlx" {
        t.Errorf("expected fusion-mlx, got %s", got.Name())
    }
}

func TestPool_BuildProviders_OpenAICompatible(t *testing.T) {
    slog.Info("test Pool_BuildProviders_OpenAICompatible")
    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "cloud": {
                    Enabled: true,
                    Type:    "openai-compatible",
                    BaseURL: "http://localhost:8080",
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    got, ok := p.Get("cloud")
    if !ok {
        t.Fatal("openai-compatible provider not built")
    }
    if got.Name() != "cloud" {
        t.Errorf("expected cloud, got %s", got.Name())
    }
}

func TestPool_BuildProviders_Anthropic(t *testing.T) {
    slog.Info("test Pool_BuildProviders_Anthropic")
    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "anthro": {
                    Enabled: true,
                    Type:    "anthropic",
                    BaseURL: "http://localhost:8081",
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    _, ok := p.Get("anthro")
    if !ok {
        t.Fatal("anthropic provider not built")
    }
}

func TestPool_BuildProviders_AllChineseProviders(t *testing.T) {
    slog.Info("test Pool_BuildProviders_AllChineseProviders")
    types := []string{"volcengine", "qianfan", "deepseek", "openrouter",
        "dashscope", "moonshot", "zhipu", "minimax", "baichuan",
        "hunyuan", "stepfun", "yi", "fusion-kb"}
    for _, typ := range types {
        p := NewPool()
        cfg := &config.ConfigSnapshot{
            Config: config.Config{
                Backends: map[string]config.BackendConfig{
                    typ: {
                        Enabled: true,
                        Type:    typ,
                        BaseURL: "http://localhost:8080",
                    },
                },
            },
        }
        if err := p.BuildProviders(cfg); err != nil {
            t.Errorf("BuildProviders failed for type %s: %v", typ, err)
        }
        got, ok := p.Get(typ)
        if !ok {
            t.Errorf("provider %s not built", typ)
            continue
        }
        if got.Name() != typ {
            t.Errorf("expected %s, got %s", typ, got.Name())
        }
    }
}

func TestPool_BuildProviders_UnknownType(t *testing.T) {
    slog.Info("test Pool_BuildProviders_UnknownType")
    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "bad": {
                    Enabled: true,
                    Type:    "unknown-type",
                    BaseURL: "http://localhost:8080",
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err == nil {
        t.Error("expected error for unknown backend type")
    }
}

func TestPool_BuildProviders_Disabled(t *testing.T) {
    slog.Info("test Pool_BuildProviders_Disabled")
    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "disabled": {
                    Enabled: false,
                    Type:    "fusion-mlx",
                    BaseURL: "http://localhost:11434",
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    _, ok := p.Get("disabled")
    if ok {
        t.Error("disabled provider should not be registered")
    }
}

func TestPool_BuildProviders_RemoveStale(t *testing.T) {
    slog.Info("test Pool_BuildProviders_RemoveStale")
    p := NewPool()
    p.Register("stale", &dummyProvider{name: "stale"}, config.BackendConfig{Type: "dummy"})
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{},
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    _, ok := p.Get("stale")
    if ok {
        t.Error("stale provider should be removed")
    }
}
