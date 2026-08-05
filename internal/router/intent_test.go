package router

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// stubClassifier returns a fixed IntentResult for testing the P-1 semantic
// layer without the real fusion-router-light model (upstream: fusion-trainer
// issue-tr-3, not yet trained).
type stubClassifier struct {
    result *IntentResult
    err    error
}

func (s stubClassifier) Classify(_ context.Context, _ *RouteRequest) (*IntentResult, error) {
    if s.err != nil {
        return nil, s.err
    }
    if s.result == nil {
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
    }
    return s.result, nil
}

func newIntentEngine(t *testing.T, cfg *config.ConfigSnapshot, classifier IntentClassifier) *Engine {
    t.Helper()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    if classifier != nil {
        e.SetIntentClassifier(classifier)
    }
    return e
}

func intentCtx(cfg *config.ConfigSnapshot) context.Context {
    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 20}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    return config.WithSnapshot(ctx, cfg)
}

// NoopClassifier always returns unknown, so the semantic layer is a no-op and
// existing P0-P7 behavior must be preserved (defers to the rule chain).
func TestNoopClassifier_ReturnsUnknown(t *testing.T) {
    res, err := NoopClassifier{}.Classify(context.Background(), &RouteRequest{Model: "m"})
    if err != nil {
        t.Fatalf("noop classifier errored: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected unknown intent, got %s", res.Intent)
    }
}

func TestDecideIntent_DisabledDefersToRuleChain(t *testing.T) {
    cfg := defaultTestSnapshot()
    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentHeavyModel, Confidence: 0.99}})
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != LocalBackend {
        t.Fatalf("expected rule-chain local when intent disabled, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason == "intent:heavy_model:cluster_platform:windows-cuda" {
        t.Fatalf("intent layer fired despite being disabled")
    }
}

func TestDecideIntent_UnknownDefersToRuleChain(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentUnknown, Confidence: 0.9}})
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != LocalBackend {
        t.Fatalf("expected rule-chain local for unknown intent, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecideIntent_LowConfidenceDefersToRuleChain(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    cfg.Config.Routing.IntentClassifier.MinConfidence = 0.8
    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentHeavyModel, Confidence: 0.5}})
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != LocalBackend {
        t.Fatalf("expected rule-chain local for low-confidence intent, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestDecideIntent_LightweightDefersToRuleChain(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentLightweight, Confidence: 0.99}})
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != LocalBackend {
        t.Fatalf("expected rule-chain local for lightweight intent, got %s: %s", dec.Backend, dec.Reason)
    }
}

// Heavy-model intent with a healthy Windows-CUDA cluster node must dispatch to
// that node by platform (issue #23/#25 scaffolding).
func TestDecideIntent_HeavyModelDispatchesToPlatformCluster(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    cfg.Config.Routing.IntentClassifier.MinConfidence = 0.5
    cfg.Config.Cluster.Enabled = true
    cfg.Config.Cluster.PlatformRouting.Enabled = true
    cfg.Config.Cluster.PlatformRouting.HeavyModelPlatform = "windows-cuda"

    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentHeavyModel, Confidence: 0.9}})
    e.SetClusterSelector(&mockClusterSelector{
        healthy:       1,
        nodeID:        "win-node-1",
        platformNodes: map[string]int{"windows-cuda": 1},
        platformNode:  map[string]string{"windows-cuda": "win-node-1"},
    })

    req := &RouteRequest{Model: "big-llm", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != ClusterBackend {
        t.Fatalf("expected cluster dispatch for heavy intent, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.NodeID != "win-node-1" {
        t.Fatalf("expected win-node-1, got %s", dec.NodeID)
    }
    if dec.Reason != "intent:heavy_model:cluster_platform:windows-cuda" {
        t.Fatalf("unexpected reason: %s", dec.Reason)
    }
}

// Diffusion intent dispatches to its configured platform node.
func TestDecideIntent_DiffusionDispatchesToPlatformCluster(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    cfg.Config.Routing.IntentClassifier.MinConfidence = 0.5
    cfg.Config.Cluster.Enabled = true
    cfg.Config.Cluster.PlatformRouting.Enabled = true
    cfg.Config.Cluster.PlatformRouting.DiffusionPlatform = "windows-cuda"

    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentDiffusion, Confidence: 0.9}})
    e.SetClusterSelector(&mockClusterSelector{
        healthy:       1,
        nodeID:        "win-diff-1",
        platformNodes: map[string]int{"windows-cuda": 1},
        platformNode:  map[string]string{"windows-cuda": "win-diff-1"},
    })

    req := &RouteRequest{Model: "sdxl", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != ClusterBackend {
        t.Fatalf("expected cluster dispatch for diffusion intent, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.NodeID != "win-diff-1" {
        t.Fatalf("expected win-diff-1, got %s", dec.NodeID)
    }
    if dec.Reason != "intent:diffusion:cluster_platform:windows-cuda" {
        t.Fatalf("unexpected reason: %s", dec.Reason)
    }
}

// When the target platform has no healthy node, the semantic layer falls back
// to cloud (not the rule chain) so the intent is still honored observably.
func TestDecideIntent_NoPlatformNodeFallsBackToCloud(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    cfg.Config.Routing.IntentClassifier.MinConfidence = 0.5
    cfg.Config.Cluster.Enabled = true
    cfg.Config.Cluster.PlatformRouting.Enabled = true
    cfg.Config.Cluster.PlatformRouting.HeavyModelPlatform = "windows-cuda"

    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentHeavyModel, Confidence: 0.9}})
    e.SetClusterSelector(&mockClusterSelector{
        healthy:       0,
        platformNodes: map[string]int{"windows-cuda": 0},
        platformNode:  map[string]string{},
    })

    req := &RouteRequest{Model: "big-llm", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != CloudBackend {
        t.Fatalf("expected cloud fallback when no platform node, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "intent:heavy_model:no_platform_node" {
        t.Fatalf("unexpected reason: %s", dec.Reason)
    }
}

// Cluster disabled -> no platform dispatch possible -> cloud fallback.
func TestDecideIntent_ClusterDisabledFallsBackToCloud(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    cfg.Config.Routing.IntentClassifier.MinConfidence = 0.5
    cfg.Config.Cluster.Enabled = false
    cfg.Config.Cluster.PlatformRouting.Enabled = true
    cfg.Config.Cluster.PlatformRouting.HeavyModelPlatform = "windows-cuda"

    e := newIntentEngine(t, cfg, stubClassifier{result: &IntentResult{Intent: IntentHeavyModel, Confidence: 0.9}})

    req := &RouteRequest{Model: "big-llm", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != CloudBackend {
        t.Fatalf("expected cloud fallback when cluster disabled, got %s: %s", dec.Backend, dec.Reason)
    }
}

// Classifier error must not break routing — fall back to the rule chain.
func TestDecideIntent_ClassifierErrorDefersToRuleChain(t *testing.T) {
    cfg := defaultTestSnapshot()
    cfg.Config.Routing.IntentClassifier.Enabled = true
    e := newIntentEngine(t, cfg, stubClassifier{err: context.DeadlineExceeded})
    req := &RouteRequest{Model: "test-model", Stream: false}
    dec := e.Decide(intentCtx(cfg), req)
    if dec.Backend != LocalBackend {
        t.Fatalf("expected rule-chain local when classifier errors, got %s: %s", dec.Backend, dec.Reason)
    }
}

func TestPlatformForIntent(t *testing.T) {
    if got := PlatformForIntent(IntentHeavyModel, "", ""); got != "windows-cuda" {
        t.Fatalf("heavy default platform = %q, want windows-cuda", got)
    }
    if got := PlatformForIntent(IntentDiffusion, "", ""); got != "windows-cuda" {
        t.Fatalf("diffusion default platform = %q, want windows-cuda", got)
    }
    if got := PlatformForIntent(IntentHeavyModel, "rocm", ""); got != "rocm" {
        t.Fatalf("heavy override platform = %q, want rocm", got)
    }
    if got := PlatformForIntent(IntentLightweight, "windows-cuda", "windows-cuda"); got != "" {
        t.Fatalf("lightweight platform = %q, want empty", got)
    }
    if got := PlatformForIntent(IntentUnknown, "x", "y"); got != "" {
        t.Fatalf("unknown platform = %q, want empty", got)
    }
}
