package tokenizer

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestCountTokens_EmptyString(t *testing.T) {
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count, err := e.CountTokens(context.TODO(), "")
    if err != nil {
        t.Fatal(err)
    }
    if count != 0 {
        t.Fatalf("expected 0 for empty, got %d", count)
    }
}

func TestCountTokens_SingleWord(t *testing.T) {
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count, _ := e.CountTokens(context.TODO(), "hello")
    if count < 1 {
        t.Fatalf("expected >=1 for single word, got %d", count)
    }
}

func TestCountTokens_Multiline(t *testing.T) {
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count, _ := e.CountTokens(context.TODO(), "hello\nworld\nfoo")
    if count < 3 {
        t.Fatalf("expected >=3 for multiline, got %d", count)
    }
    t.Logf("multiline count: %d", count)
}

func TestCountTokens_CalibrationDisabled(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{Enabled: false},
    }
    e := NewEngine(cfg, "")
    count, err := e.CountTokens(context.TODO(), "test text")
    if err != nil {
        t.Fatal(err)
    }
    if count < 1 {
        t.Fatal("expected positive count")
    }
}

func TestCountTokens_CalibrationEnabled(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 1,
            SampleSize:     5,
        },
    }
    e := NewEngine(cfg, "")
    count, err := e.CountTokens(context.TODO(), "calibration test")
    if err != nil {
        t.Fatal(err)
    }
    if count < 1 {
        t.Fatal("expected positive count")
    }
}

func TestEstimateBudget_ToolCallScene(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        ScenePresets: config.ScenePresetsConfig{
            ToolCall: 512,
        },
    }
    e := NewEngine(cfg, "")
    budget := e.EstimateBudget(100, nil, "model", true, false)
    if budget.PredictOutputTokens != 512 {
        t.Fatalf("expected ToolCall preset 512, got %d", budget.PredictOutputTokens)
    }
}

func TestEstimateBudget_MaxTokensZero(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider:     "whitespace",
        MinMaxTokens: 256,
    }
    e := NewEngine(cfg, "")
    zero := 0
    budget := e.EstimateBudget(50, &zero, "model", false, false)
    if budget.PredictOutputTokens != 256 {
        t.Fatalf("expected MinMaxTokens 256 for zero maxTokens, got %d", budget.PredictOutputTokens)
    }
}

func TestEstimateBudget_LowConfidence(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider:     "whitespace",
        MinMaxTokens: 256,
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            AutoSwitchThreshold: 0.01,
        },
    }
    e := NewEngine(cfg, "")

    e.calibMu.Lock()
    e.calibState.lastDeviation = 0.5
    e.calibMu.Unlock()

    budget := e.EstimateBudget(50, nil, "model", false, false)
    if !budget.LowConfidence {
        t.Fatal("expected LowConfidence=true when deviation > threshold")
    }
}

func TestEstimateBudget_NoLowConfidence(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider:     "whitespace",
        MinMaxTokens: 256,
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            AutoSwitchThreshold: 0.1,
        },
    }
    e := NewEngine(cfg, "")

    e.calibMu.Lock()
    e.calibState.lastDeviation = 0.01
    e.calibMu.Unlock()

    budget := e.EstimateBudget(50, nil, "model", false, false)
    if budget.LowConfidence {
        t.Fatal("expected LowConfidence=false when deviation < threshold")
    }
}

func TestWithTokenBudget_BudgetFromContext(t *testing.T) {
    budget := TokenBudget{
        InputTokens:         100,
        PredictOutputTokens: 50,
        TotalBudget:         150,
        LowConfidence:       false,
    }
    ctx := WithTokenBudget(context.Background(), budget)
    got, ok := BudgetFromContext(ctx)
    if !ok {
        t.Fatal("expected budget in context")
    }
    if got.InputTokens != 100 || got.PredictOutputTokens != 50 {
        t.Fatalf("unexpected budget: %+v", got)
    }
}

func TestBudgetFromContext_Missing(t *testing.T) {
    _, ok := BudgetFromContext(context.Background())
    if ok {
        t.Fatal("expected no budget in empty context")
    }
}

func TestCountViaMLX_ReturnsZero(t *testing.T) {
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "http://localhost:11434")
    count, err := e.countViaMLX(context.TODO(), "test")
    if err != nil {
        t.Fatal(err)
    }
    if count != 0 {
        t.Fatalf("expected 0 for MVP stub, got %d", count)
    }
}

func TestDefaultMaxTokens_ChatStream(t *testing.T) {
    cfg := &config.TokenizerConfig{
        ScenePresets: config.ScenePresetsConfig{Chat: 1024},
    }
    e := NewEngine(cfg, "")
    result := e.defaultMaxTokens("model", false, true)
    if result != 1024 {
        t.Fatalf("expected Chat=1024, got %d", result)
    }
}

func TestDefaultMaxTokens_CodeDefault(t *testing.T) {
    cfg := &config.TokenizerConfig{
        MinMaxTokens: 256,
    }
    e := NewEngine(cfg, "")
    result := e.defaultMaxTokens("model", false, false)
    if result != 256 {
        t.Fatalf("expected MinMaxTokens=256, got %d", result)
    }
}
