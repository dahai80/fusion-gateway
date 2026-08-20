package router

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func testHeuristicCfg() config.HeuristicClassifierConfig {
    return config.HeuristicClassifierConfig{
        Enabled:       true,
        CodeAdapter:   "lora-code",
        CacheSize:     4096,
        CacheTTL:      0, // no expiry for deterministic tests
        MinConfidence: 0.6,
        TextScanBytes: 4096,
    }
}

func TestClassify_CodeModelName(t *testing.T) {
    c := NewHeuristicClassifier(testHeuristicCfg())
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement fibonacci",
    }
    res, err := c.Classify(context.Background(), req)
    if err != nil {
        t.Fatalf("classify error: %v", err)
    }
    if res.Intent != IntentCode {
        t.Errorf("expected IntentCode, got %s (score %s)", res.Intent, res.Params["score"])
    }
    if res.Params["code_adapter"] != "lora-code" {
        t.Errorf("expected code_adapter=lora-code, got %q", res.Params["code_adapter"])
    }
}

func TestClassify_FencedCodeBlock(t *testing.T) {
    c := NewHeuristicClassifier(testHeuristicCfg())
    req := &RouteRequest{
        Model: "llama-3-8b",
        Text:  "fix this:\n```go\nfunc main() {\n  fmt.Println(\"hi\")\n}\n```",
        Tools: []string{"code_editor"},
    }
    res, _ := c.Classify(context.Background(), req)
    if res.Intent != IntentCode {
        t.Errorf("expected IntentCode for fenced block + tools, got %s (score %s)", res.Intent, res.Params["score"])
    }
}

func TestClassify_PlainChat(t *testing.T) {
    c := NewHeuristicClassifier(testHeuristicCfg())
    req := &RouteRequest{
        Model: "llama-3-8b",
        Text:  "hello, how are you doing today?",
    }
    res, _ := c.Classify(context.Background(), req)
    if res.Intent == IntentCode {
        t.Errorf("plain chat must not classify as code, score %s", res.Params["score"])
    }
}

func TestClassify_Disabled(t *testing.T) {
    cfg := testHeuristicCfg()
    cfg.Enabled = false
    c := NewHeuristicClassifier(cfg)
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement fibonacci",
    }
    res, _ := c.Classify(context.Background(), req)
    if res.Intent != IntentUnknown {
        t.Errorf("disabled classifier must return IntentUnknown, got %s", res.Intent)
    }
}

func TestClassify_CacheHit(t *testing.T) {
    c := NewHeuristicClassifier(testHeuristicCfg())
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement fibonacci in go",
    }
    first, _ := c.Classify(context.Background(), req)
    second, _ := c.Classify(context.Background(), req)
    if first.Intent != second.Intent || first.Confidence != second.Confidence {
        t.Errorf("cache hit should return identical result: first=%v second=%v", first, second)
    }
    if first.Intent != IntentCode {
        t.Errorf("expected IntentCode, got %s", first.Intent)
    }
}

func TestClassify_ToolsFlag(t *testing.T) {
    c := NewHeuristicClassifier(testHeuristicCfg())
    // Without tools, a code-action verb alone (0.3) is below 0.6 threshold.
    noTools := &RouteRequest{Model: "llama-3-8b", Text: "refactor this method"}
    r1, _ := c.Classify(context.Background(), noTools)
    if r1.Intent == IntentCode {
        t.Errorf("refactor alone (no tools) should not cross threshold, score %s", r1.Params["score"])
    }
    // With tools (+0.2), 0.3+0.2=0.5 still below 0.6 — but add a keyword to push over.
    withTools := &RouteRequest{Model: "llama-3-8b", Text: "refactor this method to use import", Tools: []string{"x"}}
    r2, _ := c.Classify(context.Background(), withTools)
    if r2.Intent != IntentCode {
        t.Errorf("refactor + import keyword + tools should cross threshold, score %s", r2.Params["score"])
    }
}

func BenchmarkClassify_SubMillisecond(b *testing.B) {
    c := NewHeuristicClassifier(testHeuristicCfg())
    req := &RouteRequest{
        Model: "qwen2.5-coder-7b",
        Text:  "implement fibonacci in go using dynamic programming",
    }
    ctx := context.Background()
    // Warm the cache so the bench measures steady-state (cached) cost.
    _, _ = c.Classify(ctx, req)
    b.ResetTimer()
    b.ReportAllocs()
    for i := 0; i < b.N; i++ {
        _, _ = c.Classify(ctx, req)
    }
}
