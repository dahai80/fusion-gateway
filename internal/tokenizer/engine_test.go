package tokenizer

import (
    "context"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestCountTokens_Whitespace(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
    }
    e := NewEngine(cfg, "")

    count, err := e.CountTokens(context.TODO(), "hello world foo bar")
    if err != nil {
        t.Fatal(err)
    }
    // whitespace heuristic: 4 words * 1.5 ratio = 6
    if count != 6 {
        t.Errorf("expected 6 tokens, got %d", count)
    }
}

func TestEstimateBudget_WithMaxTokens(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
    }
    e := NewEngine(cfg, "")

    maxTok := 100
    budget := e.EstimateBudget(50, &maxTok, "test-model", false, false)
    if budget.InputTokens != 50 {
        t.Errorf("expected input=50, got %d", budget.InputTokens)
    }
    if budget.PredictOutputTokens != 100 {
        t.Errorf("expected output=100, got %d", budget.PredictOutputTokens)
    }
    if budget.TotalBudget != 150 {
        t.Errorf("expected total=150, got %d", budget.TotalBudget)
    }
}

func TestEstimateBudget_DefaultMaxTokens(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider:    "whitespace",
        MinMaxTokens: 256,
        ScenePresets: config.ScenePresetsConfig{
            Chat: 1024,
        },
    }
    e := NewEngine(cfg, "")

    // hasTools=false, isStream=false -> falls to default: MinMaxTokens
    budget := e.EstimateBudget(50, nil, "test-model", false, false)
    if budget.PredictOutputTokens != 256 {
        t.Errorf("expected MinMaxTokens 256, got %d", budget.PredictOutputTokens)
    }
}

func TestEstimateBudget_ChatScene(t *testing.T) {
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        ScenePresets: config.ScenePresetsConfig{
            Chat: 1024,
        },
    }
    e := NewEngine(cfg, "")

    // isStream=true, hasTools=false -> Chat preset
    budget := e.EstimateBudget(50, nil, "test-model", false, true)
    if budget.PredictOutputTokens != 1024 {
        t.Errorf("expected Chat preset 1024, got %d", budget.PredictOutputTokens)
    }
}
