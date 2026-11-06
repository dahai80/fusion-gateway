package router

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestCloudStrategy_SelectEmptyBackends(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "round-robin"}, lt)

    result := cs.Select(nil)
    if result != "" {
        t.Errorf("expected empty string for nil backends, got %q", result)
    }

    result = cs.Select([]string{})
    if result != "" {
        t.Errorf("expected empty string for empty backends, got %q", result)
    }
}

func TestCloudStrategy_SelectSingleBackend(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "round-robin"}, lt)

    result := cs.Select([]string{"openai"})
    if result != "openai" {
        t.Errorf("expected %q, got %q", "openai", result)
    }
}

func TestCloudStrategy_RoundRobin(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "round-robin"}, lt)
    backends := []string{"openai", "anthropic", "deepseek"}

    seen := map[string]int{}
    for i := 0; i < 9; i++ {
        picked := cs.Select(backends)
        seen[picked]++
    }

    for _, b := range backends {
        if seen[b] != 3 {
            t.Errorf("round-robin expected 3 selections for %q, got %d", b, seen[b])
        }
    }
}

func TestCloudStrategy_LatencyPicksLowestP95(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "latency"}, lt)

    for i := 0; i < 100; i++ {
        lt.Record("openai", 500*time.Millisecond)
        lt.Record("deepseek", 100*time.Millisecond)
        lt.Record("anthropic", 300*time.Millisecond)
    }

    picked := cs.Select([]string{"openai", "deepseek", "anthropic"})
    if picked != "deepseek" {
        t.Errorf("latency strategy expected %q, got %q", "deepseek", picked)
    }
}

func TestCloudStrategy_CostPicksCheapest(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "cost"}, lt)

    picked := cs.Select([]string{"openai", "anthropic", "deepseek"})
    if picked != "deepseek" {
        t.Errorf("cost strategy expected %q, got %q", "deepseek", picked)
    }
}

func TestCloudStrategy_CostWithUnknownBackend(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "cost"}, lt)

    backends := []string{"unknown-cloud", "openai"}
    picked := cs.Select(backends)
    if picked != "openai" {
        t.Errorf("cost strategy with unknown backend expected %q, got %q", "openai", picked)
    }
}

func TestCloudStrategy_WeightUsesConfig(t *testing.T) {
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy: "weight",
        CloudWeights: map[string]int{
            "deepseek":  90,
            "openai":    10,
        },
    }, lt)

    counts := map[string]int{}
    for i := 0; i < 1000; i++ {
        picked := cs.Select([]string{"deepseek", "openai"})
        counts[picked]++
    }

    if counts["deepseek"] < 700 {
        t.Errorf("weight strategy expected deepseek selected heavily, got deepseek=%d openai=%d", counts["deepseek"], counts["openai"])
    }
}

func TestCloudStrategy_WeightFallsBackToRoundRobin(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:     "weight",
        CloudWeights: map[string]int{},
    }, lt)

    backends := []string{"openai", "anthropic"}
    picked := cs.Select(backends)
    if picked != "openai" && picked != "anthropic" {
        t.Errorf("weight fallback to round-robin expected one of backends, got %q", picked)
    }
}

func TestCloudStrategy_LeastBusyPicksFewestSamples(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "least-busy"}, lt)

    for i := 0; i < 50; i++ {
        lt.Record("openai", 100*time.Millisecond)
    }
    for i := 0; i < 10; i++ {
        lt.Record("deepseek", 100*time.Millisecond)
    }
    for i := 0; i < 30; i++ {
        lt.Record("anthropic", 100*time.Millisecond)
    }

    picked := cs.Select([]string{"openai", "deepseek", "anthropic"})
    if picked != "deepseek" {
        t.Errorf("least-busy expected %q (10 samples), got %q", "deepseek", picked)
    }
}

func TestCloudStrategy_UpdateConfigChangesStrategy(t *testing.T) {
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "round-robin"}, lt)

    for i := 0; i < 50; i++ {
        lt.Record("openai", 500*time.Millisecond)
    }
    for i := 0; i < 5; i++ {
        lt.Record("deepseek", 100*time.Millisecond)
    }

    first := cs.Select([]string{"openai", "deepseek"})
    second := cs.Select([]string{"openai", "deepseek"})
    if first == second {
        t.Errorf("round-robin should alternate, got same result %q twice", first)
    }

    cs.UpdateConfig(config.CloudRoutingConfig{Strategy: "cost"})
    picked := cs.Select([]string{"openai", "deepseek"})
    if picked != "deepseek" {
        t.Errorf("after UpdateConfig to cost, expected %q, got %q", "deepseek", picked)
    }
}

func TestCloudStrategy_DefaultStrategyFallsToFirst(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "unknown-strategy"}, lt)

    picked := cs.Select([]string{"openai", "deepseek"})
    if picked != "openai" {
        t.Errorf("unknown strategy expected first backend %q, got %q", "openai", picked)
    }
}

func TestCloudStrategy_UsagePicksLeastUsed(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "usage"}, lt)
    backends := []string{"openai", "deepseek"}

    first := cs.Select(backends)
    // No usage recorded yet: tie breaks to the first backend.
    if first != "openai" {
        t.Fatalf("usage tie expected first backend, got %q", first)
    }
    second := cs.Select(backends)
    if second != "deepseek" {
        t.Fatalf("usage should rotate to least used, got %q", second)
    }
    third := cs.Select(backends)
    if third != "openai" {
        t.Fatalf("usage counts even again, got %q", third)
    }
}

func TestCloudStrategy_CooldownSkipsFailedBackend(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:        "round-robin",
        CooldownSeconds: 60,
    }, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    for i := 0; i < 6; i++ {
        if got := cs.Select(backends); got != "deepseek" {
            t.Fatalf("cooled backend should be skipped, pick %d got %q", i, got)
        }
    }
}

func TestCloudStrategy_CooldownRequiresThreshold(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:         "round-robin",
        CooldownSeconds:  60,
        FailureThreshold: 3,
    }, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    cs.RecordOutcome("openai", false)
    if got := cs.Select(backends); got != "openai" {
        t.Fatalf("below threshold backend must stay eligible, got %q", got)
    }
    cs.RecordOutcome("openai", false)
    if got := cs.Select(backends); got != "deepseek" {
        t.Fatalf("threshold reached, expected cooldown, got %q", got)
    }
}

func TestCloudStrategy_CooldownSuccessResetsFailures(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:         "round-robin",
        CooldownSeconds:  60,
        FailureThreshold: 2,
    }, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    cs.RecordOutcome("openai", true)
    cs.RecordOutcome("openai", false)
    if got := cs.Select(backends); got != "openai" {
        t.Fatalf("success must reset the failure streak, got %q", got)
    }
}

func TestCloudStrategy_CooldownAllCooledFallsBackToFullList(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:        "round-robin",
        CooldownSeconds: 60,
    }, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    cs.RecordOutcome("deepseek", false)
    // Availability first: never 503 because of cooldown state alone.
    for i := 0; i < 4; i++ {
        got := cs.Select(backends)
        if got != "openai" && got != "deepseek" {
            t.Fatalf("all cooled must fall back to full list, got %q", got)
        }
    }
}

func TestCloudStrategy_CooldownExpires(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:        "round-robin",
        CooldownSeconds: 1,
    }, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    if got := cs.Select(backends); got != "deepseek" {
        t.Fatalf("expected cooled backend skipped, got %q", got)
    }
    time.Sleep(1100 * time.Millisecond)
    eligible := false
    for i := 0; i < 4; i++ {
        if cs.Select(backends) == "openai" {
            eligible = true
            break
        }
    }
    if !eligible {
        t.Fatal("cooldown must expire and re-include the backend")
    }
}

func TestCloudStrategy_CooldownDisabledIgnoresOutcomes(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{Strategy: "round-robin"}, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    cs.RecordOutcome("openai", false)
    cs.RecordOutcome("openai", false)
    seen := map[string]bool{}
    for i := 0; i < 4; i++ {
        seen[cs.Select(backends)] = true
    }
    if !seen["openai"] {
        t.Fatalf("cooldown off (default) must keep every backend eligible")
    }
}

func TestCloudStrategy_CooldownDisabledByReload(t *testing.T) {
    // #541 lesson: cooldown state must not outlive a config that disables it.
    t.Parallel()
    lt := NewLatencyTracker(1000)
    cs := NewCloudStrategy(config.CloudRoutingConfig{
        Strategy:        "round-robin",
        CooldownSeconds: 60,
    }, lt)
    backends := []string{"openai", "deepseek"}

    cs.RecordOutcome("openai", false)
    if got := cs.Select(backends); got != "deepseek" {
        t.Fatalf("precondition: openai should be cooled, got %q", got)
    }
    cs.UpdateConfig(config.CloudRoutingConfig{Strategy: "round-robin"})
    eligible := false
    for i := 0; i < 4; i++ {
        if cs.Select(backends) == "openai" {
            eligible = true
            break
        }
    }
    if !eligible {
        t.Fatal("reload that zeroes cooldown must re-enable cooled backends")
    }
}
