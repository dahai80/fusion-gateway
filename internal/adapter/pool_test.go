package adapter

import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

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
    // Local-ness must ALSO hold for a fusion-mlx instance registered as
    // openai-compatible (the 2026-10-12 Images-passthrough switch): loopback
    // base_url or UDS socket_path mark the SAME local engine. Type-only
    // detection emptied /v1/models under routing.mode=local.
    p.Register("loopback-oc", &dummyProvider{name: "loopback-oc"}, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://127.0.0.1:11434"})
    p.Register("localhost-oc", &dummyProvider{name: "localhost-oc"}, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://localhost:8080"})
    p.Register("uds-oc", &dummyProvider{name: "uds-oc"}, config.BackendConfig{Type: "openai-compatible", BaseURL: "http://unix/", SocketPath: "/tmp/s.sock"})
    p.Register("remote-oc", &dummyProvider{name: "remote-oc"}, config.BackendConfig{Type: "openai-compatible", BaseURL: "https://api.openai.com/v1"})

    cases := []struct {
        name string
        want bool
    }{
        {"mlx", true},
        {"kb", true},
        {"hub", true},
        {"cloud", false},
        {"loopback-oc", true},
        {"localhost-oc", true},
        {"uds-oc", true},
        {"remote-oc", false},
        {"nonexistent", false},
    }
    for _, c := range cases {
        if got := p.IsLocalProvider(c.name); got != c.want {
            t.Errorf("IsLocalProvider(%q) = %v, want %v", c.name, got, c.want)
        }
    }
}

// Regression: BuildProviders must NOT wrap a loopback openai-compatible
// backend (a local fusion-mlx engine registered under that type) in the
// cloud in-flight tracking decorator — that counted LOCAL requests under
// the {backend="cloud"} Prometheus series. A remote openai-compatible
// backend keeps the wrap.
func TestPool_BuildProviders_LoopbackNotCloudWrapped(t *testing.T) {
    slog.Info("test Pool_BuildProviders_LoopbackNotCloudWrapped")
    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "local-oc": {
                    Enabled: true,
                    Type:    "openai-compatible",
                    BaseURL: "http://127.0.0.1:11434",
                },
                "remote-oc": {
                    Enabled: true,
                    Type:    "openai-compatible",
                    BaseURL: "https://api.upstream.example/v1",
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    for name, wantWrapped := range map[string]bool{"local-oc": false, "remote-oc": true} {
        prov, ok := p.Get(name)
        if !ok {
            t.Fatalf("provider %q not built", name)
        }
        // Assert the concrete decorator: localSlotProvider (the #210 wrap for
        // local backends) also exposes Unwrap, so the anonymous-interface
        // check can no longer distinguish cloud-tracking from local-slot.
        _, wrapped := prov.(*cloudTrackingProvider)
        if wrapped != wantWrapped {
            t.Errorf("provider %q: cloud-tracking wrap = %v, want %v", name, wrapped, wantWrapped)
        }
    }
}

// #210: a local-classified openai-compatible backend (the Images-passthrough
// registration of the fusion-mlx engine) must yield a working local-engine
// role: GetFusionMLX returns a non-nil FusionMLXProvider (shadow), the name
// matches the backend key, and the SERVING provider is wrapped with RR4 slot
// accounting whose Unwrap chain still exposes the Images passthrough.
func TestPool_BuildProviders_ShadowLocalEngineRole(t *testing.T) {
    slog.Info("test Pool_BuildProviders_ShadowLocalEngineRole")
    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "fusion-mlx": {
                    Enabled: true,
                    Type:    "openai-compatible",
                    BaseURL: "http://127.0.0.1:11434",
                    APIKey:  "fg-test-key",
                },
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    mlx := p.GetFusionMLX()
    if mlx == nil {
        t.Fatal("#210: GetFusionMLX() = nil for local-classified openai-compatible backend, want shadow engine role")
    }
    if name, _ := p.FusionMLXName(); name != "fusion-mlx" {
        t.Errorf("FusionMLXName() = %q, want fusion-mlx", name)
    }
    prov, ok := p.Get("fusion-mlx")
    if !ok {
        t.Fatal("serving provider missing")
    }
    slot, ok := prov.(*localSlotProvider)
    if !ok {
        t.Fatalf("serving provider not wrapped with local slot accounting, got %T", prov)
    }
    if slot.mlx != mlx {
        t.Fatal("slot decorator must share the shadow engine role's counter")
    }
    // H2 lesson: the Unwrap chain must keep the Images passthrough visible —
    // the type switch to openai-compatible exists FOR Images.
    type imager interface {
        Images(ctx context.Context, req *ImageRequest) (*ImageResponse, error)
    }
    if _, ok := prov.(imager); !ok {
        t.Fatal("Images passthrough lost through the localSlotProvider wrap")
    }
}

// #210/RR4: the shadow engine role's max_concurrent cap must be enforced on
// the SERVING provider's chat path. With max=1 and a first request parked
// inside the upstream handler, a second concurrent Chat must fail fast with
// ErrLocalSlotFull (diverts to the A4 cloud fallback) instead of overflowing.
func TestPool_ShadowLocalEngine_RR4SlotCap(t *testing.T) {
    slog.Info("test Pool_ShadowLocalEngine_RR4SlotCap")
    releaseFirst := make(chan struct{})
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/v1/chat/completions" {
            <-releaseFirst
        }
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
    }))
    defer srv.Close()
    // Close exactly once: explicitly before the release assertion, and via
    // the flag guard if a t.Fatal aborts earlier (srv.Close would otherwise
    // block forever on a handler parked on the channel).
    releaseClosed := false
    defer func() {
        if !releaseClosed {
            close(releaseFirst)
        }
    }()

    p := NewPool()
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            Backends: map[string]config.BackendConfig{
                "fusion-mlx": {
                    Enabled: true,
                    Type:    "openai-compatible",
                    BaseURL: srv.URL,
                },
            },
            Routing: config.RoutingConfig{
                LocalPriority: config.LocalPriorityConfig{MaxConcurrent: 1},
            },
        },
    }
    if err := p.BuildProviders(cfg); err != nil {
        t.Fatal(err)
    }
    prov, _ := p.Get("fusion-mlx")

    firstDone := make(chan error, 1)
    go func() {
        _, err := prov.Chat(context.Background(), &ChatRequest{Model: "m"})
        firstDone <- err
    }()
    // Wait until the first request is parked inside the upstream (slot held).
    deadline := time.Now().Add(5 * time.Second)
    for p.GetFusionMLX().InFlight() == 0 && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    if p.GetFusionMLX().InFlight() == 0 {
        t.Fatal("first chat never acquired the local slot")
    }
    _, err := prov.Chat(context.Background(), &ChatRequest{Model: "m"})
    if !errors.Is(err, ErrLocalSlotFull) {
        t.Fatalf("second chat should fail with ErrLocalSlotFull, got %v", err)
    }
    releaseClosed = true
    close(releaseFirst)
    if err := <-firstDone; err != nil {
        t.Fatalf("first chat failed: %v", err)
    }
    if got := p.GetFusionMLX().InFlight(); got != 0 {
        t.Fatalf("slot not released after chat completion: in_flight=%d", got)
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

// TestEI6_BuildProviders_MixedLocalCloudNoDeadlock guards the cloud-tracking
// wrap loop in BuildProviders against the RLock-inside-Lock deadlock. The loop
// runs while BuildProviders holds p.mu.Lock; an earlier version called
// p.IsLocalProvider() (which takes p.mu.RLock) on the same goroutine and hung
// forever (Go RWMutex: RLock inside Lock blocks). On the BUG this test never
// returns (hits the timeout). On the FIX BuildProviders completes and the local
// provider is NOT wrapped while the cloud provider IS.
func TestEI6_BuildProviders_MixedLocalCloudNoDeadlock(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    done := make(chan struct{})
    var p *Pool
    go func() {
        defer close(done)
        p = NewPool()
        cfg := &config.ConfigSnapshot{
            Config: config.Config{
                Backends: map[string]config.BackendConfig{
                    "local": {Enabled: true, Type: "fusion-mlx", BaseURL: srv.URL},
                    // Remote URL: an openai-compatible backend on a loopback
                    // URL is a LOCAL engine under the isLocalBackend predicate
                    // (2026-10-14 fix) and would not be wrapped; the cloud
                    // side of this test needs a genuinely remote base_url.
                    "cloud": {Enabled: true, Type: "openai-compatible", BaseURL: "https://api.upstream.example/v1"},
                },
            },
        }
        if err := p.BuildProviders(cfg); err != nil {
            t.Errorf("BuildProviders error: %v", err)
            return
        }
    }()
    select {
    case <-done:
    case <-time.After(5 * time.Second):
        t.Fatal("EI6: BuildProviders deadlocked — cloud wrap loop re-acquired RLock inside Lock (RLock-inside-Lock on same goroutine)")
    }

    localProv, ok := p.Get("local")
    if !ok {
        t.Fatal("local provider missing")
    }
    if _, wrapped := localProv.(*cloudTrackingProvider); wrapped {
        t.Error("EI6: local provider must NOT be wrapped with cloud tracking")
    }
    cloudProv, ok := p.Get("cloud")
    if !ok {
        t.Fatal("cloud provider missing")
    }
    if _, wrapped := cloudProv.(*cloudTrackingProvider); !wrapped {
        t.Error("EI6: cloud provider must be wrapped with cloud tracking (in_flight_requests cloud label)")
    }
}
