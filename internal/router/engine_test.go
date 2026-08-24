package router

import (
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
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

// issue #83: a model present in the local set but absent from
// model_mapping is local-exclusive. Even when the output/input ratio
// exceeds threshold, it must NOT be cloud-diverted (cloud cannot serve
// it -> 400 "Invalid model name"). The P3.5 guard short-circuits to
// local before P4.5 ratio.
func TestDecide_LocalExclusiveModel_RatioNotCloudDiverted(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Fallback.Enabled = true
    cfg.Config.Routing.Fallback.ModelMapping = map[string]string{
        "claude-opus-4-7": "glm5.2",
    }
    cfg.Config.Routing.OutputInputRatioThreshold = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"Qwen3.5-9B-4bit": true}
    })

    // ratio = 2048/132 = 15.52 >> 0.6 — would route cloud without guard
    budget := tokenizer.TokenBudget{InputTokens: 132, PredictOutputTokens: 2048, TotalBudget: 2180}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "Qwen3.5-9B-4bit", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for local-exclusive model under high ratio, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "local_exclusive_model" {
        t.Errorf("expected local_exclusive_model, got %s", dec.Reason)
    }
}

// issue #83: same guard must hold against P4 token-budget cloud-divert.
func TestDecide_LocalExclusiveModel_TokenBudgetNotCloudDiverted(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Fallback.Enabled = true
    cfg.Config.Routing.Fallback.ModelMapping = map[string]string{
        "claude-opus-4-7": "glm5.2",
    }
    cfg.Config.Routing.TokenThreshold = 100
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"Qwen3.5-9B-4bit": true}
    })

    // input 500 > threshold 100 — would route cloud without guard
    budget := tokenizer.TokenBudget{InputTokens: 500, TotalBudget: 600}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "Qwen3.5-9B-4bit", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for local-exclusive model over token threshold, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "local_exclusive_model" {
        t.Errorf("expected local_exclusive_model, got %s", dec.Reason)
    }
}

// issue #83: a model present in local set AND in model_mapping is NOT
// local-exclusive (cloud can serve it via the mapping). The guard must
// no-op so the existing P4.5 ratio cloud-divert behavior is preserved.
func TestDecide_MappedModel_RatioStillCloudDiverts(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.Fallback.Enabled = true
    cfg.Config.Routing.Fallback.ModelMapping = map[string]string{
        "claude-opus-4-7": "glm5.2",
    }
    cfg.Config.Routing.OutputInputRatioThreshold = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"claude-opus-4-7": true}
    })

    budget := tokenizer.TokenBudget{InputTokens: 132, PredictOutputTokens: 2048, TotalBudget: 2180}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "claude-opus-4-7", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud for mapped model under high ratio, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "output_input_ratio_exceeded" {
        t.Errorf("expected output_input_ratio_exceeded, got %s", dec.Reason)
    }
}

type mockClusterSelector struct {
    healthy int
    nodeID  string
    err     error
    // platform-aware fields for D4 dispatch-by-platform tests
    platformNodes map[string]int
    platformNode  map[string]string
    // model-aware fields for #95 model-aware cluster routing tests
    modelNodes map[string]int
    modelNode  map[string]string
    modelErr   error
}

func (m *mockClusterSelector) HealthyNodes() int                 { return m.healthy }
func (m *mockClusterSelector) SelectNode(string) (string, error) { return m.nodeID, m.err }

func (m *mockClusterSelector) HealthyNodesByPlatform(platform string) int {
    if m.platformNodes != nil {
        if c, ok := m.platformNodes[platform]; ok {
            return c
        }
        return 0
    }
    return m.healthy
}

func (m *mockClusterSelector) SelectNodeByPlatform(_, platform string) (string, error) {
    if m.platformNode != nil {
        if id, ok := m.platformNode[platform]; ok {
            return id, m.err
        }
        return "", fmt.Errorf("no healthy node on platform %q", platform)
    }
    return m.nodeID, m.err
}

func (m *mockClusterSelector) HealthyNodesByModel(model string) int {
    if model == "" {
        return m.healthy
    }
    if m.modelNodes != nil {
        if c, ok := m.modelNodes[model]; ok {
            return c
        }
        return 0
    }
    return m.healthy
}

func (m *mockClusterSelector) SelectNodeByModel(_, model string) (string, error) {
    if model == "" {
        return m.nodeID, m.err
    }
    if m.modelNode != nil {
        if id, ok := m.modelNode[model]; ok {
            return id, m.modelErr
        }
        return "", fmt.Errorf("no healthy node serving model %q", model)
    }
    return m.nodeID, m.err
}

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

func TestDecide_ClusterModelAware(t *testing.T) {
    t.Log("testing cluster routes to node serving req.Model (#95)")
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test trip")

    e.SetClusterSelector(&mockClusterSelector{
        healthy:  2,
        nodeID:   "node-1",
        modelNode: map[string]string{"served-model": "node-serving"},
    })

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "served-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != ClusterBackend {
        t.Errorf("expected cluster, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.NodeID != "node-serving" {
        t.Errorf("expected node-serving (serves served-model), got %s", dec.NodeID)
    }
}

func TestDecide_ClusterNoModelMatch_CloudFallback(t *testing.T) {
    t.Log("testing cluster falls through to cloud when no node serves req.Model (#95)")
    cfg := defaultTestSnapshot()
    cfg.Config.Cluster.Enabled = true
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "test trip")

    e.SetClusterSelector(&mockClusterSelector{
        healthy:  2,
        nodeID:   "node-1",
        modelNode: map[string]string{"served-model": "node-1"},
    })

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{Model: "unserved-model", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != CloudBackend {
        t.Errorf("expected cloud when no cluster node serves model, got %s: %s", dec.Backend, dec.Reason)
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

// TestDecide_OutputInputRatioSkippedForTinyInput reproduces issue #48: a tiny
// request (4 input tokens, 5 predicted output → ratio 1.25 > 0.6) must NOT be
// misrouted to cloud by the output/input ratio check. The ratio is statistically
// meaningless at very low input counts, so the engine skips the check below the
// input-token floor and falls through to local (model available locally).
func TestDecide_OutputInputRatioSkippedForTinyInput(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.TokenThreshold = 1000
    cfg.Config.Routing.OutputInputRatioThreshold = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"Qwen3.5-9B-4bit": true}
    })

    budget := tokenizer.TokenBudget{InputTokens: 4, PredictOutputTokens: 5, TotalBudget: 9}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "Qwen3.5-9B-4bit", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local for tiny request with model available locally, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason == "output_input_ratio_exceeded" {
        t.Errorf("tiny request must not trigger output_input_ratio_exceeded, got reason=%s", dec.Reason)
    }
    t.Logf("Tiny-request ratio skip works: backend=%s reason=%s", dec.Backend, dec.Reason)
}

// TestDecide_OutputInputRatioSkippedForTinyInput_ExplicitFloor verifies the
// config-driven floor (output_input_ratio_min_input_tokens) overrides the
// default: with an explicit floor of 64, a 50-token request (above default 32
// but below 64) skips the ratio check even though ratio 200/50=4 > 0.6.
func TestDecide_OutputInputRatioSkippedForTinyInput_ExplicitFloor(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.TokenThreshold = 1000
    cfg.Config.Routing.OutputInputRatioThreshold = 0.6
    cfg.Config.Routing.OutputInputRatioMinInputTokens = 64
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"Qwen3.5-9B-4bit": true}
    })

    budget := tokenizer.TokenBudget{InputTokens: 50, PredictOutputTokens: 200, TotalBudget: 250}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    ctx = config.WithSnapshot(ctx, cfg)

    req := &RouteRequest{Model: "Qwen3.5-9B-4bit", Stream: false}
    dec := e.Decide(ctx, req)
    if dec.Backend != LocalBackend {
        t.Errorf("expected local when input below explicit floor, got %s: %s", dec.Backend, dec.Reason)
    }
    t.Logf("Explicit floor works: backend=%s reason=%s", dec.Backend, dec.Reason)
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

// stubAdapterLookup is a test AdapterLookup with a fixed name->path map, used
// to verify the best-effort code_adapter validation and name->path resolution
// in decideIntentLocked.
type stubAdapterLookup struct {
    paths map[string]string
}

func (s stubAdapterLookup) Has(name string) bool {
    _, ok := s.paths[name]
    return ok
}

func (s stubAdapterLookup) Path(name string) (string, bool) {
    p, ok := s.paths[name]
    return p, ok
}

// TestDecide_CodeIntentDispatch asserts a code-intent request routes to
// LocalBackend with the configured LoRA adapter when the heuristic classifier
// is enabled and recognizes the request.
func TestDecide_CodeIntentDispatch(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.HeuristicClassifier.Enabled = true
    cfg.Config.Routing.HeuristicClassifier.MinConfidence = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)

    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetHeuristicClassifier(NewHeuristicClassifier(cfg.Config.Routing.HeuristicClassifier))

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement a fibonacci function in go",
    }

    dec := e.Decide(ctx, req)
    if dec == nil {
        t.Fatal("expected a route decision for code intent, got nil")
    }
    if dec.Backend != LocalBackend {
        t.Errorf("expected LocalBackend, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Adapter != "lora-code" {
        t.Errorf("expected Adapter=lora-code, got %q", dec.Adapter)
    }
    if dec.Reason != "intent:code:lora:lora-code" {
        t.Errorf("expected reason intent:code:lora:lora-code, got %s", dec.Reason)
    }
}

// TestDecide_CodeIntentAdapterMissingStillDispatches asserts that when an
// AdapterLookup is wired and does NOT contain the configured code_adapter, the
// engine still dispatches to LocalBackend (best-effort: the index may be stale;
// fusion-mlx will surface a hot-swap error if the adapter is truly absent).
func TestDecide_CodeIntentAdapterMissingStillDispatches(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.HeuristicClassifier.Enabled = true
    cfg.Config.Routing.HeuristicClassifier.MinConfidence = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)

    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetHeuristicClassifier(NewHeuristicClassifier(cfg.Config.Routing.HeuristicClassifier))
    // Index present but does NOT list "lora-code" — validation should warn, not block.
    e.SetAdapterLookup(stubAdapterLookup{paths: map[string]string{"lora-sql": "/adapters/lora-sql"}})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement a fibonacci function in go",
    }

    dec := e.Decide(ctx, req)
    if dec == nil {
        t.Fatal("expected dispatch despite missing adapter in index, got nil")
    }
    if dec.Backend != LocalBackend {
        t.Errorf("expected LocalBackend despite index miss, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Adapter != "lora-code" {
        t.Errorf("expected Adapter=lora-code preserved, got %q", dec.Adapter)
    }
}

// TestDecide_CodeIntentAdapterPresentDispatches asserts that when the
// AdapterLookup contains the configured code_adapter, dispatch proceeds
// normally (validation passes silently).
func TestDecide_CodeIntentAdapterPresentDispatches(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.HeuristicClassifier.Enabled = true
    cfg.Config.Routing.HeuristicClassifier.MinConfidence = 0.6
    hw := hardware.NewCollector(&cfg.Config.Hardware)

    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetHeuristicClassifier(NewHeuristicClassifier(cfg.Config.Routing.HeuristicClassifier))
    e.SetAdapterLookup(stubAdapterLookup{paths: map[string]string{"lora-code": "/adapters/qwen2.5-coder-7b/lora-code"}})

    ctx := config.WithSnapshot(context.Background(), cfg)
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement a fibonacci function in go",
    }

    dec := e.Decide(ctx, req)
    // When the index lists the adapter, the engine resolves the bare name to
    // the absolute adapter directory path that fusion-mlx's "adapters" field
    // requires; the Reason still carries the bare name for X-Route-Decision.
    if dec == nil || dec.Backend != LocalBackend || dec.Adapter != "/adapters/qwen2.5-coder-7b/lora-code" {
        t.Errorf("expected local + resolved adapter path, got %+v", dec)
    }
    if dec.Reason != "intent:code:lora:lora-code" {
        t.Errorf("expected reason intent:code:lora:lora-code, got %s", dec.Reason)
    }
}

func TestTrip_PublishesCircuitBreakerMetrics(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("local", "memory_overload")

    body := scrapeMetrics(t)
    if !strings.Contains(body, "fusion_gateway_circuit_breaker_trips_total") {
        t.Fatal("expected circuit_breaker_trips_total metric after Trip")
    }
    if !strings.Contains(body, "fusion_gateway_circuit_breaker_state") {
        t.Fatal("expected circuit_breaker_state metric after Trip")
    }
    // open = 1; the gauge line for backend=local should carry value 1.
    if !strings.Contains(body, `fusion_gateway_circuit_breaker_state{backend="local"} 1`) {
        t.Fatalf("expected circuit_breaker_state{backend=\"local\"} 1 (open) after Trip, got:\n%s", body)
    }
}

func TestRecordFailure_OpensBreaker_PublishesStateAndTrip(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.CircuitBreaker.FailureThreshold = 2
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.RecordFailure("local")
    // Not yet open (threshold=2): gauge should still be 0 (closed).
    body := scrapeMetrics(t)
    if !strings.Contains(body, `fusion_gateway_circuit_breaker_state{backend="local"} 0`) {
        t.Fatalf("expected state 0 (closed) after 1 failure, got:\n%s", body)
    }
    e.RecordFailure("local")
    // Now open: gauge 1 + a trip counted (failure_threshold reason).
    body = scrapeMetrics(t)
    if !strings.Contains(body, `fusion_gateway_circuit_breaker_state{backend="local"} 1`) {
        t.Fatalf("expected state 1 (open) after 2 failures, got:\n%s", body)
    }
}

func TestPublishBreakerStates_ReflectsCurrentState(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    e.Trip("cloud", "timeout")
    e.PublishBreakerStates()

    body := scrapeMetrics(t)
    if !strings.Contains(body, `fusion_gateway_circuit_breaker_state{backend="cloud"} 1`) {
        t.Fatalf("expected cloud state 1 after PublishBreakerStates, got:\n%s", body)
    }
}

func TestLocalInFlight_WithoutWiring(t *testing.T) {
    cfg := defaultTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    if got := e.LocalInFlight(); got != 0 {
        t.Fatalf("expected 0 local in-flight before wiring, got %d", got)
    }
}

func scrapeMetrics(t *testing.T) string {
    t.Helper()
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec, req)
    return rec.Body.String()
}
