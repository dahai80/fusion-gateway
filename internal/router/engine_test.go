package router

import (
    "context"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

func TestDecide_LocalPriority(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:  "test-model",
        Stream: false,
    }

    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecide_CircuitBreakerTrip(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test trip")

    req := &RouteRequest{
        Model:  "test-model",
        Stream: false,
    }

    dec := e.Decide(context.Background(), req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud after trip, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecide_TokenBudgetExceeded(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.TokenThreshold = 100
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 200, TotalBudget: 300}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:  "test-model",
        Stream: false,
    }

    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when token budget exceeded, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestCircuitBreaker_Transitions(t *testing.T) {
    cb := NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold:    3,
        Timeout:             30 * 1e9, // 30s in nanoseconds
        HalfOpenMaxRequests: 1,
        SuccessThreshold:    2,
    })

    if cb.State() != StateClosed {
        t.Fatalf("expected closed, got %s", cb.State())
    }

    for i := 0; i < 3; i++ {
        cb.RecordFailure()
    }
    if cb.State() != StateOpen {
        t.Fatalf("expected open after failures, got %s", cb.State())
    }
}

func TestCircuitBreaker_Trip(t *testing.T) {
    cb := NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold: 10,
        Timeout:          30 * 1e9,
    })

    cb.Trip("hardware overload")
    if cb.State() != StateOpen {
        t.Fatalf("expected open after trip, got %s", cb.State())
    }
}

func defaultTestSnapshot() *config.ConfigSnapshot {
    cfg := config.DefaultConfig()
    return &config.ConfigSnapshot{
        Config:  cfg,
        Version: 1,
    }
}

func TestDecide_ConcurrentLimit(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.LocalPriority.MaxConcurrent = 4
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalInFlight(func() int64 { return 4 })

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud at concurrent limit, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "concurrent_limit" {
        t.Errorf("expected concurrent_limit reason, got %s", dec.Reason)
    }
}

func TestDecide_ModelNotAvailableLocally(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"local-model-a": true}
    })

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "cloud-only-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud for unavailable model, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "model_not_available_locally" {
        t.Errorf("expected model_not_available_locally, got %s", dec.Reason)
    }
}

func TestDecide_ModelAvailableLocally(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"local-model-a": true, "qwen3-0.6b": true}
    })

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "qwen3-0.6b", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for available model, got %s: %s", dec.Backend, dec.Reason)
    }
}

type mockClusterSelector struct {
    healthy int
    nodeID  string
    err     error
}

func (m *mockClusterSelector) HealthyNodes() int            { return m.healthy }
func (m *mockClusterSelector) SelectNode(string) (string, error) { return m.nodeID, m.err }

func TestDecide_ClusterFallback(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test trip")

    e.SetClusterSelector(&mockClusterSelector{healthy: 2, nodeID: "node-1"})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != ClusterBackend {
        t.Errorf("expected cluster fallback, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.NodeID != "node-1" {
        t.Errorf("expected node-1, got %s", dec.NodeID)
    }
}

func TestDecide_ClusterUnavailable_FallsToCloud(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test trip")

    e.SetClusterSelector(&mockClusterSelector{healthy: 0})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when no cluster nodes, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecide_ClusterBreakerOpen_FallsToCloud(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test trip")
    e.Trip("cluster", "cluster overload")

    e.SetClusterSelector(&mockClusterSelector{healthy: 2, nodeID: "node-1"})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when cluster breaker open, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDrainAndApply(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test")
    if e.CircuitBreakerState("local") != StateOpen {
        t.Fatal("expected open after trip")
    }

    newCfg := defaultTestSnapshot()
    newCfg.Version = 2
    newCfg.Config.HotReload.BreakerDrainTimeout = 1 * time.Second
    newCfg.Config.HotReload.BreakerWarmupSuccess = 2

    e.DrainAndApply(newCfg)

    if e.CircuitBreakerState("local") != StateHalfOpen {
        t.Fatalf("expected half_open after warmup, got %s", e.CircuitBreakerState("local"))
    }
}

func TestDecide_Embedding_LocalPriority(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "text-embedding-ada-002", Type: RequestTypeEmbedding}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for embedding when ready, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "embedding_local_priority" {
        t.Errorf("expected embedding_local_priority, got %s", dec.Reason)
    }
}

func TestDecide_Embedding_LocalUnavailable(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test")

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "text-embedding-ada-002", Type: RequestTypeEmbedding}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud for embedding when local unavailable, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecide_Embedding_ClusterFallback(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test")
    e.SetClusterSelector(&mockClusterSelector{healthy: 2, nodeID: "node-1"})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "text-embedding-ada-002", Type: RequestTypeEmbedding}
    dec := e.Decide(ctx, req)
    if dec.Backend != ClusterBackend {
        t.Errorf("expected cluster for embedding when local unavailable, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecide_Rerank_CloudDefault(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"qwen3-0.6b": true}
    })

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "bge-rerank-v2-m3", Type: RequestTypeRerank}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud for rerank without local model, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "rerank_cloud_default" {
        t.Errorf("expected rerank_cloud_default, got %s", dec.Reason)
    }
}

func TestDecide_Rerank_LocalAvailable(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"bge-rerank-v2-m3": true, "qwen3-0.6b": true}
    })

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "bge-rerank-v2-m3", Type: RequestTypeRerank}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for rerank with local model, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "rerank_local_available" {
        t.Errorf("expected rerank_local_available, got %s", dec.Reason)
    }
}

func TestDecide_Rerank_ClusterFallback(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool { return nil })
    e.SetClusterSelector(&mockClusterSelector{healthy: 2, nodeID: "node-2"})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "bge-rerank-v2-m3", Type: RequestTypeRerank}
    dec := e.Decide(ctx, req)
    if dec.Backend != ClusterBackend {
        t.Errorf("expected cluster for rerank, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestIsRerankModel(t *testing.T) {
    tests := []struct {
        model string
        want  bool
    }{
        {"bge-rerank-v2-m3", true},
        {"bge-reranker-large", true},
        {"cohere-rerank-v3", true},
        {"qwen3-0.6b", false},
        {"text-embedding-ada-002", false},
    }
    for _, tt := range tests {
        got := isRerankModel(tt.model)
        if got != tt.want {
            t.Errorf("isRerankModel(%q) = %v, want %v", tt.model, got, tt.want)
        }
    }
}
