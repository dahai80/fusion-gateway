package insights

import "testing"

// TestPricing_LongestPrefixWins pins the match rule: longest model-name
// prefix wins regardless of map order ("glm-4.5" beats "glm" for
// "glm-4.5-air"). No entry → ok=false, stored cost fallback applies.
func TestPricing_LongestPrefixWins(t *testing.T) {
    p := map[string]ModelPrice{
        "glm":     {InputPerMTok: 1, OutputPerMTok: 2},
        "glm-4.5": {InputPerMTok: 3, OutputPerMTok: 4},
        "kimi":    {InputPerMTok: 5},
    }
    if got, ok := priceFor(p, "glm-4.5-air"); !ok || got.InputPerMTok != 3 || got.OutputPerMTok != 4 {
        t.Fatalf("glm-4.5-air = %v ok=%v, want {3 4} true", got, ok)
    }
    if got, ok := priceFor(p, "glm-5"); !ok || got.InputPerMTok != 1 {
        t.Fatalf("glm-5 = %v ok=%v, want glm entry", got, ok)
    }
    if got, ok := priceFor(p, "kimi-k2"); !ok || got.OutputPerMTok != 0 {
        t.Fatalf("kimi-k2 = %v ok=%v, want kimi entry with zero output", got, ok)
    }
    if _, ok := priceFor(p, "qwen3"); ok {
        t.Fatal("unmatched model must report ok=false")
    }
    if _, ok := priceFor(nil, "glm-5"); ok {
        t.Fatal("empty pricing table must report ok=false")
    }
}

// TestPricing_CostComputation pins the read-time cost formula and the
// stored-cost fallback: priced model = in/1M*pin + out/1M*pout; unpriced
// model = stored sum.
func TestPricing_CostComputation(t *testing.T) {
    p := map[string]ModelPrice{"glm-5": {InputPerMTok: 1, OutputPerMTok: 3}}
    if c := costFor(p, "glm-5", 2_000_000, 1_000_000, 99); c != 5 {
        t.Fatalf("priced cost = %v, want 5", c)
    }
    if c := costFor(p, "qwen3", 1_000_000, 1_000_000, 7.5); c != 7.5 {
        t.Fatalf("fallback cost = %v, want 7.5 (stored)", c)
    }
}
