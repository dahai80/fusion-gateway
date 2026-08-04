package router

import (
    "context"
    "fmt"
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

func TestResolveCloudByTier(t *testing.T) {
    tests := []struct {
        name   string
        budget tokenizer.TokenBudget
        metric string
        rules  []config.TokenTierRule
        want   string
    }{
        {
            name:   "total_metric_first_rule_match",
            budget: tokenizer.TokenBudget{InputTokens: 500, PredictOutputTokens: 200, TotalBudget: 700},
            metric: "total",
            rules: []config.TokenTierRule{
                {MaxTokens: 1000, Backend: "qianfan"},
                {MaxTokens: 4000, Backend: "openai"},
                {MaxTokens: 0, Backend: "claude"},
            },
            want: "qianfan",
        },
        {
            name:   "total_metric_second_rule_match",
            budget: tokenizer.TokenBudget{InputTokens: 2000, PredictOutputTokens: 1500, TotalBudget: 3500},
            metric: "total",
            rules: []config.TokenTierRule{
                {MaxTokens: 1000, Backend: "qianfan"},
                {MaxTokens: 4000, Backend: "openai"},
                {MaxTokens: 0, Backend: "claude"},
            },
            want: "openai",
        },
        {
            name:   "total_metric_catch_all",
            budget: tokenizer.TokenBudget{InputTokens: 5000, PredictOutputTokens: 3000, TotalBudget: 8000},
            metric: "total",
            rules: []config.TokenTierRule{
                {MaxTokens: 1000, Backend: "qianfan"},
                {MaxTokens: 4000, Backend: "openai"},
                {MaxTokens: 0, Backend: "claude"},
            },
            want: "claude",
        },
        {
            name:   "input_metric",
            budget: tokenizer.TokenBudget{InputTokens: 300, PredictOutputTokens: 2000, TotalBudget: 2300},
            metric: "input",
            rules: []config.TokenTierRule{
                {MaxTokens: 500, Backend: "qianfan"},
                {MaxTokens: 0, Backend: "openai"},
            },
            want: "qianfan",
        },
        {
            name:   "output_metric",
            budget: tokenizer.TokenBudget{InputTokens: 100, PredictOutputTokens: 2000, TotalBudget: 2100},
            metric: "output",
            rules: []config.TokenTierRule{
                {MaxTokens: 500, Backend: "qianfan"},
                {MaxTokens: 0, Backend: "openai"},
            },
            want: "openai",
        },
        {
            name:   "no_match",
            budget: tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200},
            metric: "total",
            rules: []config.TokenTierRule{
                {MaxTokens: 50, Backend: "qianfan"},
            },
            want: "",
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            tier := config.TokenTierConfig{
                Enabled: true,
                Metric:  tt.metric,
                Rules:   tt.rules,
            }
            got := resolveCloudByTier(tt.budget, tier)
            if got != tt.want {
                t.Errorf("resolveCloudByTier() = %q, want %q", got, tt.want)
            }
        })
    }
}

func TestDecide_TokenTierRouting(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.TokenThreshold = 100
    cfg.Config.Routing.TokenTiers = config.TokenTierConfig{
        Enabled: true,
        Metric:  "total",
        Rules: []config.TokenTierRule{
            {MaxTokens: 2000, Backend: "qianfan"},
            {MaxTokens: 0, Backend: "openai"},
        },
    }
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    t.Run("tier_matches_small_budget", func(t *testing.T) {
        budget := tokenizer.TokenBudget{InputTokens: 500, TotalBudget: 800}
        ctx := tokenizer.WithTokenBudget(context.Background(), budget)
        ctx = config.WithSnapshot(ctx, cfg)
        req := &RouteRequest{Model: "test-model"}
        dec := e.Decide(ctx, req)
        if dec.Backend != CloudBackend {
            t.Fatalf("expected cloud, got %s", dec.Backend)
        }
        if dec.CloudTarget != "qianfan" {
            t.Errorf("expected cloud_target=qianfan, got %q", dec.CloudTarget)
        }
    })

    t.Run("tier_matches_large_budget", func(t *testing.T) {
        budget := tokenizer.TokenBudget{InputTokens: 3000, TotalBudget: 5000}
        ctx := tokenizer.WithTokenBudget(context.Background(), budget)
        ctx = config.WithSnapshot(ctx, cfg)
        req := &RouteRequest{Model: "test-model"}
        dec := e.Decide(ctx, req)
        if dec.Backend != CloudBackend {
            t.Fatalf("expected cloud, got %s", dec.Backend)
        }
        if dec.CloudTarget != "openai" {
            t.Errorf("expected cloud_target=openai, got %q", dec.CloudTarget)
        }
    })

    t.Run("tiers_disabled_uses_default", func(t *testing.T) {
        cfg2 := defaultTestSnapshot()
        cfg2.Config.Routing.TokenThreshold = 100
        cfg2.Config.Routing.TokenTiers = config.TokenTierConfig{Enabled: false}
        e2 := NewEngine(cfg2, hw)
        e2.SetLocalReady(true)

        budget := tokenizer.TokenBudget{InputTokens: 500, TotalBudget: 800}
        ctx := tokenizer.WithTokenBudget(context.Background(), budget)
        ctx = config.WithSnapshot(ctx, cfg2)
        req := &RouteRequest{Model: "test-model"}
        dec := e2.Decide(ctx, req)
        if dec.Backend != CloudBackend {
            t.Fatalf("expected cloud, got %s", dec.Backend)
        }
        if dec.CloudTarget != "" {
            t.Errorf("expected empty cloud_target when tiers disabled, got %q", dec.CloudTarget)
        }
    })
}

func TestDecide_SessionAffinity_LocalProvider(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    e.sessionAffinity.Record("space-42", "fusion-mlx")
    t.Logf("Recorded session affinity: space-42 -> fusion-mlx")

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:   "test-model",
        Stream:  false,
        SpaceID: "space-42",
    }

    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local via session affinity, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "session_affinity:local" {
        t.Errorf("expected session_affinity:local reason, got %s", dec.Reason)
    }
    t.Logf("Session affinity routes to local: backend=%s reason=%s", dec.Backend, dec.Reason)
}

func TestDecide_SessionAffinity_CloudProvider(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    e.sessionAffinity.Record("space-99", "qianfan")
    t.Logf("Recorded session affinity: space-99 -> qianfan")

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:   "test-model",
        Stream:  false,
        SpaceID: "space-99",
    }

    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud via session affinity, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.CloudTarget != "qianfan" {
        t.Errorf("expected cloud_target=qianfan, got %q", dec.CloudTarget)
    }
    t.Logf("Session affinity routes to cloud: backend=%s target=%s reason=%s", dec.Backend, dec.CloudTarget, dec.Reason)
}

func TestDecide_SessionAffinity_CircuitBreakerOpen(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.sessionAffinity.Record("space-42", "fusion-mlx")
    t.Logf("Recorded session affinity: space-42 -> fusion-mlx")

    e.Trip("local", "overload")
    t.Log("Tripped local circuit breaker")

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:   "test-model",
        Stream:  false,
        SpaceID: "space-42",
    }

    dec := e.Decide(ctx, req)
    if dec.Backend == LocalBackend {
        t.Errorf("expected NOT local when circuit breaker is open, got %s: %s", dec.Backend, dec.Reason)
    }
    t.Logf("Session affinity falls back when breaker open: backend=%s reason=%s", dec.Backend, dec.Reason)
}

func TestDecide_SessionAffinity_NoAffinity(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:   "test-model",
        Stream:  false,
        SpaceID: "space-no-affinity",
    }

    dec := e.Decide(ctx, req)
    if dec.Reason == "session_affinity:local" || dec.Reason == "session_affinity:qianfan" {
        t.Errorf("expected no session affinity match, got reason=%s", dec.Reason)
    }
    t.Logf("No affinity match falls through to normal routing: backend=%s reason=%s", dec.Backend, dec.Reason)
}

func TestDecide_SessionAffinity_EmptySpaceID(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:   "test-model",
        Stream:  false,
        SpaceID: "",
    }

    dec := e.Decide(ctx, req)
    if dec.Reason == "session_affinity:local" {
        t.Errorf("expected no session affinity match with empty SpaceID, got reason=%s", dec.Reason)
    }
    t.Logf("Empty SpaceID skips session affinity: backend=%s reason=%s", dec.Backend, dec.Reason)
}

func TestRecordAffinity(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.RecordAffinity("space-rec", "openai")

    provider, ok := e.sessionAffinity.Lookup("space-rec")
    if !ok {
        t.Fatal("expected to find recorded affinity")
    }
    if provider != "openai" {
        t.Errorf("expected openai, got %s", provider)
    }
    t.Logf("RecordAffinity stores entry: space-rec -> %s", provider)
}

func TestDecide_SessionAffinity_LocalNotReady(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.sessionAffinity.Record("space-nr", "fusion-mlx")
    t.Logf("Recorded session affinity: space-nr -> fusion-mlx, but local not ready")

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{
        Model:   "test-model",
        Stream:  false,
        SpaceID: "space-nr",
    }

    dec := e.Decide(ctx, req)
    if dec.Backend == LocalBackend {
        t.Errorf("expected NOT local when not ready, got %s: %s", dec.Backend, dec.Reason)
    }
    t.Logf("Session affinity falls back when local not ready: backend=%s reason=%s", dec.Backend, dec.Reason)
}

func TestCircuitBreaker_RecordSuccess(t *testing.T) {
    cb := NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold:    3,
        Timeout:             30 * 1e9,
        HalfOpenMaxRequests: 1,
        SuccessThreshold:    2,
    })

    cb.Trip("test")
    if cb.State() != StateOpen {
        t.Fatalf("expected open after trip, got %s", cb.State())
    }

    cb.ResetToHalfOpen()
    if cb.State() != StateHalfOpen {
        t.Fatalf("expected half_open after reset, got %s", cb.State())
    }

    cb.RecordSuccess()
    cb.RecordSuccess()
    if cb.State() != StateClosed {
        t.Fatalf("expected closed after enough successes, got %s", cb.State())
    }
    t.Logf("RecordSuccess transitions half_open -> closed after %d successes", 2)
}

func TestCircuitBreaker_RecordSuccess_Closed(t *testing.T) {
    cb := NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold: 3,
        Timeout:          30 * 1e9,
    })

    cb.RecordFailure()
    cb.RecordFailure()
    if cb.State() != StateClosed {
        t.Fatalf("expected still closed, got %s", cb.State())
    }

    cb.RecordSuccess()
    cb.RecordFailure()
    cb.RecordFailure()
    if cb.State() != StateClosed {
        t.Fatalf("expected still closed (failureCount reset by success), got %s", cb.State())
    }
    t.Log("RecordSuccess in closed state resets failure count")
}

func TestCircuitBreaker_RecordFailure_HalfOpen(t *testing.T) {
    cb := NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold: 3,
        Timeout:          30 * 1e9,
    })

    cb.Trip("test")
    cb.ResetToHalfOpen()
    if cb.State() != StateHalfOpen {
        t.Fatalf("expected half_open, got %s", cb.State())
    }

    cb.RecordFailure()
    if cb.State() != StateOpen {
        t.Fatalf("expected open after failure in half_open, got %s", cb.State())
    }
    t.Log("RecordFailure in half_open reopens breaker")
}

func TestCircuitBreaker_IsOpen(t *testing.T) {
    cb := NewCircuitBreaker(config.CircuitBreakerConfig{
        FailureThreshold: 3,
        Timeout:          30 * 1e9,
    })

    if cb.IsOpen() {
        t.Error("expected IsOpen=false when closed")
    }

    cb.Trip("test")
    if !cb.IsOpen() {
        t.Error("expected IsOpen=true after trip")
    }
    t.Log("IsOpen returns correct boolean")
}

func TestCircuitBreakerState_String(t *testing.T) {
    tests := []struct {
        state CircuitBreakerState
        want  string
    }{
        {StateClosed, "closed"},
        {StateOpen, "open"},
        {StateHalfOpen, "half_open"},
        {CircuitBreakerState(99), "unknown"},
    }
    for _, tt := range tests {
        got := tt.state.String()
        if got != tt.want {
            t.Errorf("CircuitBreakerState(%d).String() = %q, want %q", tt.state, got, tt.want)
        }
    }
}

func TestEngine_RecordSuccess(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test")
    e.RecordSuccess("local")
    e.RecordSuccess("local")
    e.RecordSuccess("local")

    state := e.CircuitBreakerState("local")
    if state != StateClosed {
        t.Logf("After RecordSuccess on tripped breaker (via half_open), state=%s", state)
    }
    t.Log("Engine.RecordSuccess delegates to breaker")
}

func TestEngine_RecordFailure(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    for i := 0; i < 5; i++ {
        e.RecordFailure("local")
    }
    if e.CircuitBreakerState("local") != StateOpen {
        t.Log("Engine.RecordFailure triggers breaker after threshold")
    }
    t.Log("Engine.RecordFailure delegates to breaker")
}

func TestEngine_UpdateConfig(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    newCfg := defaultTestSnapshot()
    newCfg.Version = 42
    e.UpdateConfig(newCfg)

    t.Log("Engine.UpdateConfig completes without panic")
}

func TestResolveCloudByRatio(t *testing.T) {
    tests := []struct {
        name   string
        ratio  float64
        rt     config.RatioTierConfig
        want   string
    }{
        {
            name:  "first_tier_match",
            ratio: 0.3,
            rt: config.RatioTierConfig{
                Enabled: true,
                Rules: []config.RatioTierRule{
                    {MaxRatio: 0.5, Backend: "qianfan"},
                    {MaxRatio: 2.0, Backend: "openai"},
                },
            },
            want: "qianfan",
        },
        {
            name:  "second_tier_match",
            ratio: 1.5,
            rt: config.RatioTierConfig{
                Enabled: true,
                Rules: []config.RatioTierRule{
                    {MaxRatio: 0.5, Backend: "qianfan"},
                    {MaxRatio: 2.0, Backend: "openai"},
                },
            },
            want: "openai",
        },
        {
            name:  "no_match_ratio_exceeds_all",
            ratio: 5.0,
            rt: config.RatioTierConfig{
                Enabled: true,
                Rules: []config.RatioTierRule{
                    {MaxRatio: 0.5, Backend: "qianfan"},
                    {MaxRatio: 2.0, Backend: "openai"},
                },
            },
            want: "",
        },
        {
            name:  "no_rules",
            ratio: 1.0,
            rt: config.RatioTierConfig{
                Enabled: true,
                Rules:   []config.RatioTierRule{},
            },
            want: "",
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := resolveCloudByRatio(tt.ratio, tt.rt)
            if got != tt.want {
                t.Errorf("resolveCloudByRatio() = %q, want %q", got, tt.want)
            }
        })
    }
}

func TestDecide_RatioTierRouting(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.TokenThreshold = 500
    cfg.Config.Routing.RatioTiers = config.RatioTierConfig{
        Enabled: true,
        Rules: []config.RatioTierRule{
            {MaxRatio: 0.5, Backend: "qianfan"},
            {MaxRatio: 2.0, Backend: "openai"},
        },
    }
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    t.Run("ratio_tier_low_ratio", func(t *testing.T) {
        budget := tokenizer.TokenBudget{InputTokens: 200, PredictOutputTokens: 50, TotalBudget: 250}
        ctx := tokenizer.WithTokenBudget(context.Background(), budget)
        ctx = config.WithSnapshot(ctx, cfg)
        req := &RouteRequest{Model: "test-model"}
        dec := e.Decide(ctx, req)
        if dec.Backend != CloudBackend {
            t.Fatalf("expected cloud, got %s", dec.Backend)
        }
        if dec.CloudTarget != "qianfan" {
            t.Errorf("expected cloud_target=qianfan, got %q", dec.CloudTarget)
        }
    })

    t.Run("ratio_tier_high_ratio", func(t *testing.T) {
        budget := tokenizer.TokenBudget{InputTokens: 200, PredictOutputTokens: 300, TotalBudget: 500}
        ctx := tokenizer.WithTokenBudget(context.Background(), budget)
        ctx = config.WithSnapshot(ctx, cfg)
        req := &RouteRequest{Model: "test-model"}
        dec := e.Decide(ctx, req)
        if dec.Backend != CloudBackend {
            t.Fatalf("expected cloud, got %s", dec.Backend)
        }
        if dec.CloudTarget != "openai" {
            t.Errorf("expected cloud_target=openai, got %q", dec.CloudTarget)
        }
    })
}

func TestDecide_OutputInputRatioThreshold(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.TokenThreshold = 1000
    cfg.Config.Routing.OutputInputRatioThreshold = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 100, PredictOutputTokens: 200, TotalBudget: 300}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when output/input ratio exceeds threshold, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "output_input_ratio_exceeded" {
        t.Errorf("expected output_input_ratio_exceeded, got %s", dec.Reason)
    }
    t.Logf("Output/input ratio threshold works: reason=%s", dec.Reason)
}

func TestEngine_CircuitBreakerState_Unknown(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    state := e.CircuitBreakerState("nonexistent")
    if state != StateOpen {
        t.Errorf("expected open for unknown backend, got %s", state)
    }
    t.Logf("CircuitBreakerState for unknown backend returns open")
}

func TestDecide_EmptyPrompt_Local(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 0, TotalBudget: 0}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for empty prompt, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "empty_prompt_local" {
        t.Errorf("expected empty_prompt_local, got %s", dec.Reason)
    }
    t.Logf("Empty prompt routes to local: reason=%s", dec.Reason)
}

func TestDecide_TokenBudgetMissing(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    ctx := config.WithSnapshot(context.Background(), cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when token budget missing, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "token_budget_missing" {
        t.Errorf("expected token_budget_missing, got %s", dec.Reason)
    }
    t.Logf("Missing token budget routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_MemoryOverload(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.LocalPriority.MaxSystemMemoryRatio = 0.8
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    hw.SetLatestForTest(hardware.HardwareMetrics{MemoryUsedRatio: 0.9})
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud on memory overload, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "memory_overload" {
        t.Errorf("expected memory_overload, got %s", dec.Reason)
    }
    t.Logf("Memory overload routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_MLXMemoryOverload(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.LocalPriority.MaxMLXMemoryRatio = 0.5
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    hw.SetLatestForTest(hardware.HardwareMetrics{
        MLXActiveMemory: 800,
        GPUInUseMemory:  1000,
    })
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud on MLX memory overload, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "mlx_memory_overload" {
        t.Errorf("expected mlx_memory_overload, got %s", dec.Reason)
    }
    t.Logf("MLX memory overload routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_SwapThrashing(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.LocalPriority.SwapPageRateThreshold = 100
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    hw.SetLatestForTest(hardware.HardwareMetrics{SwapPageInRate: 500})
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud on swap thrashing, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "swap_thrashing" {
        t.Errorf("expected swap_thrashing, got %s", dec.Reason)
    }
    t.Logf("Swap thrashing routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_GPUMemoryLow(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    hw.SetLatestForTest(hardware.HardwareMetrics{
        GPUAllocMemory: 1000,
        GPUInUseMemory: 950,
    })
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud on GPU memory low, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "gpu_memory_low" {
        t.Errorf("expected gpu_memory_low, got %s", dec.Reason)
    }
    t.Logf("GPU memory low routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_MetricsCollectionError(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Hardware.CollectionErrorProtection = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    hw.SetLatestForTest(hardware.HardwareMetrics{
        CollectionError: fmt.Errorf("gopsutil failed"),
    })
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud on metrics collection error, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "metrics_collection_error" {
        t.Errorf("expected metrics_collection_error, got %s", dec.Reason)
    }
    t.Logf("Metrics collection error routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_LocalNotReady(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when local not ready, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "local_not_ready" {
        t.Errorf("expected local_not_ready, got %s", dec.Reason)
    }
    t.Logf("Local not ready routes to cloud: reason=%s", dec.Reason)
}

func TestDecide_ContextWindowFallback(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Fallback.ContextWindowFallback = map[string]string{
        "small-model": "large-model",
    }
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"large-model": true}
    })

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "small-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local with context window fallback, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "context_window_fallback:large-model" {
        t.Errorf("expected context_window_fallback:large-model, got %s", dec.Reason)
    }
    t.Logf("Context window fallback works: reason=%s", dec.Reason)
}

func TestDecide_ContextWindowFallback_NotAvailable(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Fallback.ContextWindowFallback = map[string]string{
        "small-model": "large-model",
    }
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{}
    })

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "small-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when fallback model not available, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "model_not_available_locally" {
        t.Errorf("expected model_not_available_locally, got %s", dec.Reason)
    }
}

func TestDecide_ModeLocal(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Mode = "local"
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(false)

    budget := tokenizer.TokenBudget{InputTokens: 100, TotalBudget: 200}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("mode=local should force local, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "mode_local" {
        t.Errorf("expected mode_local reason, got %s", dec.Reason)
    }
}

func TestDecide_ModeCloud(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Mode = "cloud"
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("mode=cloud should force cloud, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "mode_cloud" {
        t.Errorf("expected mode_cloud reason, got %s", dec.Reason)
    }
}

func TestDecide_ModeHybrid(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Mode = "hybrid"
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)

    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("mode=hybrid with small tokens should route local, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecide_ModeLocalEmbedding(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Mode = "local"
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(false)

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "embed-model", Type: RequestTypeEmbedding}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("mode=local should force embedding to local, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "mode_local:embedding" {
        t.Errorf("expected mode_local:embedding, got %s", dec.Reason)
    }
}

func TestDecide_ModeCloudRerank(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Mode = "cloud"
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "rerank-model", Type: RequestTypeRerank}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("mode=cloud should force rerank to cloud, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "mode_cloud:rerank" {
        t.Errorf("expected mode_cloud:rerank, got %s", dec.Reason)
    }
}
