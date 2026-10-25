package router

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// supplyTestSnapshot builds a snapshot with a models.supply entry routing
// "shared-model" to [fusion-mlx, cloud-a] and both backends configured.
func supplyTestSnapshot() *config.ConfigSnapshot {
    base := config.DefaultConfig()
    cfg := &config.ConfigSnapshot{Config: base, Version: 1}
    cfg.Config.Backends = map[string]config.BackendConfig{
        "fusion-mlx": {Type: "openai", BaseURL: "http://127.0.0.1:11434", Enabled: true},
        "cloud-a":    {Type: "openai", BaseURL: "http://cloud-a.example", Enabled: true, APIKey: "sk-test"},
        "cloud-b":    {Type: "openai", BaseURL: "http://cloud-b.example", Enabled: false},
    }
    cfg.Config.Models.Supply = map[string][]string{
        "shared-model": {"fusion-mlx", "cloud-a"},
    }
    return cfg
}

func supplyTestContext(cfg *config.ConfigSnapshot) context.Context {
    budget := tokenizer.TokenBudget{InputTokens: 10, TotalBudget: 100}
    ctx := tokenizer.WithTokenBudget(context.Background(), budget)
    return config.WithSnapshot(ctx, cfg)
}

// P0.18: local-first supply entry wins when the model is in the local set.
func TestModelsSupply_LocalFirst(t *testing.T) {
    cfg := supplyTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"shared-model": true}
    })

    dec := e.Decide(supplyTestContext(cfg), &RouteRequest{Model: "shared-model", Stream: false})
    if dec.Backend != LocalBackend {
        t.Fatalf("expected local via supply, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "models_supply:fusion-mlx" {
        t.Errorf("expected models_supply:fusion-mlx reason, got %s", dec.Reason)
    }
}

// P0.18: local entry skipped (model not in local set) falls to the next
// supply entry — cloud-a — instead of the generic chain.
func TestModelsSupply_FallsToCloud(t *testing.T) {
    cfg := supplyTestSnapshot()
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool {
        return map[string]bool{"other-model": true}
    })

    dec := e.Decide(supplyTestContext(cfg), &RouteRequest{Model: "shared-model", Stream: false})
    if dec.Backend != CloudBackend {
        t.Fatalf("expected cloud via supply, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.CloudTarget != "cloud-a" {
        t.Errorf("expected CloudTarget cloud-a, got %q", dec.CloudTarget)
    }
    if dec.Reason != "models_supply:cloud-a" {
        t.Errorf("expected models_supply:cloud-a reason, got %s", dec.Reason)
    }
}

// P0.18: a disabled cloud supply entry is skipped.
func TestModelsSupply_DisabledEntrySkipped(t *testing.T) {
    cfg := supplyTestSnapshot()
    cfg.Config.Models.Supply["shared-model"] = []string{"cloud-b", "cloud-a"}
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)

    dec := e.Decide(supplyTestContext(cfg), &RouteRequest{Model: "shared-model", Stream: false})
    if dec.Backend != CloudBackend || dec.CloudTarget != "cloud-a" {
        t.Fatalf("expected cloud-a after disabled skip, got %s/%q: %s", dec.Backend, dec.CloudTarget, dec.Reason)
    }
}

// P0.18: with allow_cross_supply=false and no eligible entry, the decision
// is a rejection — an unlisted backend must not serve the name.
func TestModelsSupply_ExclusiveMiss(t *testing.T) {
    cfg := supplyTestSnapshot()
    // Only ineligible entries: local without the model, cloud-b disabled.
    cfg.Config.Models.Supply["shared-model"] = []string{"fusion-mlx", "cloud-b"}
    no := false
    cfg.Config.Models.AllowCrossSupply = &no
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool { return map[string]bool{} })

    dec := e.Decide(supplyTestContext(cfg), &RouteRequest{Model: "shared-model", Stream: false})
    if !dec.Rejected {
        t.Fatalf("expected rejection on exclusive supply miss, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "models_supply_miss" {
        t.Errorf("expected models_supply_miss, got %s", dec.Reason)
    }
}

// P0.18: default allow_cross_supply=true keeps the historical chain when
// the supply list is exhausted (no rejection).
func TestModelsSupply_ExhaustedFallsThrough(t *testing.T) {
    cfg := supplyTestSnapshot()
    cfg.Config.Models.Supply["shared-model"] = []string{"cloud-b"} // disabled
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool { return map[string]bool{} })

    dec := e.Decide(supplyTestContext(cfg), &RouteRequest{Model: "shared-model", Stream: false})
    if dec.Rejected {
        t.Fatalf("expected fall-through on exhausted supply, got rejection: %s", dec.Reason)
    }
    if dec.Reason == "models_supply:cloud-b" {
        t.Errorf("disabled entry must not be selected")
    }
}

// AC17: the cloud_models allowlist outranks supply — a listed cloud entry
// for an allowlist-pinned-local model must not be selected; the P0.15
// guard fires first (local decision).
func TestModelsSupply_CloudModelsGuardWins(t *testing.T) {
    cfg := supplyTestSnapshot()
    cfg.Config.Routing.CloudModels = []string{"other-*"}
    hw := hardware.NewCollector(&cfg.Config.Hardware)
    e := NewEngine(cfg, hw)
    e.SetLocalReady(true)
    e.SetLocalModels(func() map[string]bool { return map[string]bool{} })

    dec := e.Decide(supplyTestContext(cfg), &RouteRequest{Model: "shared-model", Stream: false})
    if dec.Backend != LocalBackend {
        t.Fatalf("expected local via allowlist guard, got %s: %s", dec.Backend, dec.Reason)
    }
    if dec.Reason != "cloud_models_allowlist_miss" {
        t.Errorf("expected cloud_models_allowlist_miss, got %s", dec.Reason)
    }
}

// ModelAllowedByCloudList export sanity (server layer uses it for
// qualified-lock guard, AC17).
func TestModelAllowedByCloudList_Export(t *testing.T) {
    if !ModelAllowedByCloudList("glm-5", nil) {
        t.Error("empty list must allow everything")
    }
    if ModelAllowedByCloudList("glm-5", []string{"qwen*"}) {
        t.Error("non-matching pattern must not allow")
    }
    if !ModelAllowedByCloudList("GLM-5", []string{"glm*"}) {
        t.Error("case-insensitive prefix match must allow")
    }
}
