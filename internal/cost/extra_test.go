package cost

import (
    "math"
    "os"
    "path/filepath"
    "testing"

    "gopkg.in/yaml.v3"
)

func TestCalculateCost_EmbeddingModel(t *testing.T) {
    cost := CalculateCost("text-embedding-3-small", 1000, 0)
    expected := 1000.0 / 1_000_000 * 0.02
    if math.Abs(cost-expected) > 1e-9 {
        t.Fatalf("expected %f, got %f", expected, cost)
    }
}

func TestCalculateCost_ChineseModel(t *testing.T) {
    cost := CalculateCost("qwen-turbo", 1000, 500)
    expected := 1000.0/1_000_000*0.30 + 500.0/1_000_000*0.60
    if math.Abs(cost-expected) > 1e-9 {
        t.Fatalf("expected %f, got %f", expected, cost)
    }
}

func TestLookupPricing_PrefixMatch_BestMatch(t *testing.T) {
    p, ok := lookupPricing("gpt-4-turbo-preview")
    if !ok {
        t.Fatal("expected prefix match for gpt-4-turbo-preview")
    }
    if p.PromptPricePer1M != 10.0 {
        t.Fatalf("expected gpt-4-turbo pricing, got prompt=%f", p.PromptPricePer1M)
    }
}

func TestTracker_SetGlobalMarkup(t *testing.T) {
    tr := NewTracker(100)
    tr.SetGlobalMarkup(0.1)
    tr.Record("key1", "cloud", "gpt-4", 1000, 0)
    baseCost := 1000.0 / 1_000_000 * 30.0
    markedUp := baseCost * 1.1
    tr.mu.RLock()
    actual := tr.records[0].CostUSD
    tr.mu.RUnlock()
    if math.Abs(actual-markedUp) > 1e-9 {
        t.Fatalf("expected %f with 10%% markup, got %f", markedUp, actual)
    }
}

func TestTracker_SetKeyMarkup(t *testing.T) {
    tr := NewTracker(100)
    tr.SetGlobalMarkup(0.2)
    tr.SetKeyMarkup("key1", 0.5)
    tr.Record("key1", "cloud", "gpt-4", 1000, 0)
    baseCost := 1000.0 / 1_000_000 * 30.0
    markedUp := baseCost * 1.5
    tr.mu.RLock()
    actual := tr.records[0].CostUSD
    tr.mu.RUnlock()
    if math.Abs(actual-markedUp) > 1e-9 {
        t.Fatalf("expected %f with key markup, got %f", markedUp, actual)
    }
}

func TestTracker_GetMarkup_Global(t *testing.T) {
    tr := NewTracker(100)
    tr.SetGlobalMarkup(0.3)
    got := tr.GetMarkup("unknown-key")
    if math.Abs(got-0.3) > 1e-9 {
        t.Fatalf("expected 0.3, got %f", got)
    }
}

func TestTracker_GetMarkup_KeySpecific(t *testing.T) {
    tr := NewTracker(100)
    tr.SetKeyMarkup("k1", 0.5)
    got := tr.GetMarkup("k1")
    if math.Abs(got-0.5) > 1e-9 {
        t.Fatalf("expected 0.5, got %f", got)
    }
}

func TestTracker_ExportJSON_InvalidPath(t *testing.T) {
    tr := NewTracker(100)
    tr.Record("k1", "cloud", "gpt-4", 100, 0)
    err := tr.ExportJSON("/nonexistent/dir/file.json")
    if err == nil {
        t.Fatal("expected error for invalid path")
    }
}

func TestCustomPricingManager_NoFile(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    m := NewCustomPricingManager("")
    if m == nil {
        t.Fatal("expected non-nil manager")
    }
    _, ok := m.Lookup("any")
    if ok {
        t.Fatal("expected no pricing without file")
    }
}

func TestCustomPricingManager_NonexistentFile(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    m := NewCustomPricingManager("/nonexistent/pricing.yaml")
    if m == nil {
        t.Fatal("expected non-nil manager even with bad file")
    }
}

func TestCustomPricingManager_LoadAndLookup(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")
    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "my-model": {PromptPricePer1M: 5.0, CompletionPricePer1M: 10.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    _ = os.WriteFile(f, data, 0644)

    m := NewCustomPricingManager(f)
    p, ok := m.Lookup("my-model")
    if !ok {
        t.Fatal("expected custom pricing to be found")
    }
    if p.PromptPricePer1M != 5.0 {
        t.Fatalf("expected 5.0, got %f", p.PromptPricePer1M)
    }
}

func TestCustomPricingManager_AllPricing(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")
    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "m1": {PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0},
            "m2": {PromptPricePer1M: 3.0, CompletionPricePer1M: 4.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    _ = os.WriteFile(f, data, 0644)

    m := NewCustomPricingManager(f)
    all := m.AllPricing()
    if len(all) != 2 {
        t.Fatalf("expected 2, got %d", len(all))
    }
}

func TestCustomPricingManager_InvalidYAML(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")
    _ = os.WriteFile(f, []byte("{{invalid"), 0644)
    m := NewCustomPricingManager(f)
    _, ok := m.Lookup("any")
    if ok {
        t.Fatal("expected no pricing from invalid YAML")
    }
}

func TestCustomPricingManager_Stop(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    m := NewCustomPricingManager("")
    m.Stop()
}

func TestCustomPricingManager_StartWatch_NoFile(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    m := NewCustomPricingManager("")
    m.StartWatch()
    m.Stop()
}

func TestLookupPricing_CustomPricingPriority(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()
    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")
    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "gpt-4": {PromptPricePer1M: 999.0, CompletionPricePer1M: 999.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    _ = os.WriteFile(f, data, 0644)
    NewCustomPricingManager(f)

    p, ok := lookupPricing("gpt-4")
    if !ok {
        t.Fatal("expected pricing for gpt-4")
    }
    if p.PromptPricePer1M != 999.0 {
        t.Fatalf("expected custom pricing 999, got %f", p.PromptPricePer1M)
    }
}

func TestCalculateCost_EdgeCases(t *testing.T) {
    cost := CalculateCost("gpt-4", 0, 0)
    if cost != 0.0 {
        t.Fatalf("expected 0 for zero tokens, got %f", cost)
    }
    cost = CalculateCost("gpt-4", 1, 0)
    if cost <= 0 {
        t.Fatal("expected positive cost for 1 prompt token")
    }
    cost = CalculateCost("gpt-4", 0, 1)
    if cost <= 0 {
        t.Fatal("expected positive cost for 1 completion token")
    }
}
