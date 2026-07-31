package cost

import (
    "math"
    "testing"
)

func TestCalculateCost_KnownModel(t *testing.T) {
    t.Parallel()
    // gpt-4: prompt=$30/1M, completion=$60/1M
    // 1000 prompt tokens: 1000/1_000_000 * 30 = 0.03
    // 500 completion tokens: 500/1_000_000 * 60 = 0.03
    // total: 0.06
    cost := CalculateCost("gpt-4", 1000, 500)
    if math.Abs(cost-0.06) > 1e-9 {
        t.Errorf("expected 0.06, got %.10f", cost)
    }
}

func TestCalculateCost_UnknownModel(t *testing.T) {
    t.Parallel()
    cost := CalculateCost("nonexistent-model-xyz", 1000, 500)
    if cost != 0.0 {
        t.Errorf("expected 0.0 for unknown model, got %.10f", cost)
    }
}

func TestCalculateCost_PrefixMatch(t *testing.T) {
    t.Parallel()
    // "gpt-4o-2024-05-13" should prefix-match "gpt-4o"
    // gpt-4o: prompt=$2.5/1M, completion=$10/1M
    // 2000 prompt: 2000/1_000_000 * 2.5 = 0.005
    // 1000 completion: 1000/1_000_000 * 10 = 0.01
    // total: 0.015
    cost := CalculateCost("gpt-4o-2024-05-13", 2000, 1000)
    if math.Abs(cost-0.015) > 1e-9 {
        t.Errorf("expected 0.015, got %.10f", cost)
    }
}

func TestCalculateCost_ZeroTokens(t *testing.T) {
    t.Parallel()
    cost := CalculateCost("gpt-4", 0, 0)
    if cost != 0.0 {
        t.Errorf("expected 0.0 for zero tokens, got %.10f", cost)
    }
}

func TestLookupPricing_ExactMatch(t *testing.T) {
    t.Parallel()
    p, ok := lookupPricing("claude-3-haiku")
    if !ok {
        t.Fatal("expected exact match for claude-3-haiku")
    }
    if p.PromptPricePer1M != 0.25 || p.CompletionPricePer1M != 1.25 {
        t.Errorf("unexpected pricing: prompt=%.2f completion=%.2f", p.PromptPricePer1M, p.CompletionPricePer1M)
    }
}

func TestLookupPricing_NoMatch(t *testing.T) {
    t.Parallel()
    _, ok := lookupPricing("totally-unknown")
    if ok {
        t.Error("expected no match for unknown model")
    }
}
