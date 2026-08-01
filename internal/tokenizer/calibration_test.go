package tokenizer

import (
    "context"
    "errors"
    "sync"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestMaybeCalibrate_SampleIntervalEarlyReturn(t *testing.T) {
    t.Log("samplesSinceLastCalib < SampleInterval should return early")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 5,
        },
    }
    e := NewEngine(cfg, "")

    for i := 0; i < 4; i++ {
        count, err := e.CountTokens(context.TODO(), "test text")
        if err != nil {
            t.Fatal(err)
        }
        if count < 1 {
            t.Fatalf("expected positive count, got %d", count)
        }
    }

    e.calibMu.Lock()
    samples := e.calibState.samplesSinceLastCalib
    e.calibMu.Unlock()
    if samples != 4 {
        t.Fatalf("expected 4 samples since last calib, got %d", samples)
    }
}

func TestMaybeCalibrate_SampleIntervalReached(t *testing.T) {
    t.Log("when samplesSinceLastCalib >= SampleInterval, calibration runs")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 1,
            DeviationThreshold:  0.1,
            AutoSwitchThreshold: 0.5,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 0, nil
    }

    count, err := e.CountTokens(context.TODO(), "hello world")
    if err != nil {
        t.Fatal(err)
    }
    if count < 1 {
        t.Fatal("expected positive count")
    }

    e.calibMu.Lock()
    samples := e.calibState.samplesSinceLastCalib
    e.calibMu.Unlock()
    if samples != 0 {
        t.Fatalf("expected samples reset to 0 after calibration, got %d", samples)
    }
}

func TestMaybeCalibrate_MLXError(t *testing.T) {
    t.Log("countViaMLX error should log warning and return")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 1,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 0, errors.New("mlx unavailable")
    }

    count, err := e.CountTokens(context.TODO(), "hello world")
    if err != nil {
        t.Fatal(err)
    }
    if count < 1 {
        t.Fatal("expected positive count from local counting")
    }
}

func TestMaybeCalibrate_MLXReturnsZero(t *testing.T) {
    t.Log("mlxCount == 0 should return without updating deviation")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            AutoSwitchThreshold: 0.5,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 0, nil
    }

    _, _ = e.CountTokens(context.TODO(), "test")

    e.calibMu.Lock()
    dev := e.calibState.lastDeviation
    e.calibMu.Unlock()
    if dev != 0 {
        t.Fatalf("expected deviation 0 when mlxCount=0, got %f", dev)
    }
}

func TestMaybeCalibrate_PositiveDeviation(t *testing.T) {
    t.Log("positive deviation: localCount > mlxCount")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            DeviationThreshold:  0.1,
            AutoSwitchThreshold: 0.5,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 100, nil
    }

    _, _ = e.CountTokens(context.TODO(), "a b c d e f g h i j k l m n o p q r s t u v w x y z a b c d e f g h i j k l m n o p q r s t u v w x y z")

    e.calibMu.Lock()
    dev := e.calibState.lastDeviation
    e.calibMu.Unlock()
    if dev <= 0 {
        t.Fatalf("expected positive deviation, got %f", dev)
    }
}

func TestMaybeCalibrate_NegativeDeviation(t *testing.T) {
    t.Log("negative deviation: localCount < mlxCount, absolute value stored")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            DeviationThreshold:  0.1,
            AutoSwitchThreshold: 0.5,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 1000, nil
    }

    _, _ = e.CountTokens(context.TODO(), "short text")

    e.calibMu.Lock()
    dev := e.calibState.lastDeviation
    e.calibMu.Unlock()
    if dev < 0 {
        t.Fatalf("expected non-negative deviation (abs), got %f", dev)
    }
    t.Logf("deviation for local<mlx: %f", dev)
}

func TestMaybeCalibrate_DeviationExceedsThreshold(t *testing.T) {
    t.Log("deviation > DeviationThreshold should log warning")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            DeviationThreshold:  0.01,
            AutoSwitchThreshold: 2.0,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 1000, nil
    }

    _, _ = e.CountTokens(context.TODO(), "tiny")

    e.calibMu.Lock()
    dev := e.calibState.lastDeviation
    e.calibMu.Unlock()
    if dev <= 0.01 {
        t.Fatalf("expected deviation > 0.01, got %f", dev)
    }
}

func TestMaybeCalibrate_DeviationExceedsAutoSwitchThreshold(t *testing.T) {
    t.Log("deviation > AutoSwitchThreshold should log error and mark low confidence")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            DeviationThreshold:  0.01,
            AutoSwitchThreshold: 0.05,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 1000, nil
    }

    _, _ = e.CountTokens(context.TODO(), "tiny")

    budget := e.EstimateBudget(10, nil, "model", false, false)
    if !budget.LowConfidence {
        t.Fatal("expected LowConfidence=true when deviation > AutoSwitchThreshold")
    }
}

func TestMaybeCalibrate_ZeroDeviation(t *testing.T) {
    t.Log("when localCount == mlxCount, deviation is 0")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            DeviationThreshold:  0.1,
            AutoSwitchThreshold: 0.5,
        },
    }
    e := NewEngine(cfg, "")

    text := "a b"
    localCount := e.countLocal(text)

    e.countViaMLXFunc = func(ctx context.Context, txt string) (int, error) {
        return localCount, nil
    }

    _, _ = e.CountTokens(context.TODO(), text)

    e.calibMu.Lock()
    dev := e.calibState.lastDeviation
    e.calibMu.Unlock()
    if dev != 0 {
        t.Fatalf("expected deviation 0 for exact match, got %f", dev)
    }
}

func TestMaybeCalibrate_ConcurrentSafety(t *testing.T) {
    t.Log("concurrent CountTokens calls should not race")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 1,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 50, nil
    }

    var wg sync.WaitGroup
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            _, _ = e.CountTokens(context.TODO(), "concurrent test text")
        }()
    }
    wg.Wait()
}

func TestMaybeCalibrate_SamplesResetAfterCalibration(t *testing.T) {
    t.Log("samplesSinceLastCalib resets to 0 after reaching SampleInterval")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 3,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 0, nil
    }

    for i := 0; i < 3; i++ {
        _, _ = e.CountTokens(context.TODO(), "test")
    }

    e.calibMu.Lock()
    samples := e.calibState.samplesSinceLastCalib
    e.calibMu.Unlock()
    if samples != 0 {
        t.Fatalf("expected samples reset to 0, got %d", samples)
    }
}

func TestMaybeCalibrate_CalibrationDisabled(t *testing.T) {
    t.Log("when calibration disabled, maybeCalibrate should not be called")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled: false,
        },
    }
    e := NewEngine(cfg, "")
    called := false
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        called = true
        return 0, nil
    }

    _, _ = e.CountTokens(context.TODO(), "test text")
    if called {
        t.Fatal("countViaMLXFunc should not be called when calibration is disabled")
    }
}

func TestCountLocal_TabOnly(t *testing.T) {
    t.Log("countLocal with tab-only text")
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count := e.countLocal("\t\t\t")
    if count < 1 {
        t.Fatalf("expected >=1 for tab-only text, got %d", count)
    }
    t.Logf("tab-only count: %d", count)
}

func TestCountLocal_CarriageReturn(t *testing.T) {
    t.Log("countLocal with carriage return text")
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count := e.countLocal("\r\n\r\n")
    if count < 1 {
        t.Fatalf("expected >=1 for crlf text, got %d", count)
    }
    t.Logf("crlf count: %d", count)
}

func TestCountLocal_MixedWhitespace(t *testing.T) {
    t.Log("countLocal with mixed whitespace chars")
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count := e.countLocal("a \t b \n c \r d")
    t.Logf("mixed whitespace count: %d", count)
    if count < 5 {
        t.Fatalf("expected >=5 for mixed whitespace, got %d", count)
    }
}

func TestCountLocal_SingleRune(t *testing.T) {
    t.Log("countLocal with single non-whitespace rune")
    cfg := &config.TokenizerConfig{Provider: "whitespace"}
    e := NewEngine(cfg, "")
    count := e.countLocal("x")
    if count != 1 {
        t.Fatalf("expected 1 for single rune, got %d", count)
    }
}

func TestEstimateBudget_NegativeMaxTokens(t *testing.T) {
    t.Log("negative maxTokens should fall through to defaultMaxTokens")
    cfg := &config.TokenizerConfig{
        Provider:     "whitespace",
        MinMaxTokens: 256,
    }
    e := NewEngine(cfg, "")
    neg := -1
    budget := e.EstimateBudget(50, &neg, "model", false, false)
    if budget.PredictOutputTokens != 256 {
        t.Fatalf("expected MinMaxTokens 256 for negative maxTokens, got %d", budget.PredictOutputTokens)
    }
}

func TestBudgetFromContext_WrongType(t *testing.T) {
    t.Log("BudgetFromContext with wrong type should return false")
    ctx := context.WithValue(context.Background(), TokenBudgetKey, "not a budget")
    _, ok := BudgetFromContext(ctx)
    if ok {
        t.Fatal("expected false for wrong type assertion")
    }
}

func TestEstimateBudget_LowConfidenceIntegration(t *testing.T) {
    t.Log("end-to-end: calibration sets deviation, EstimateBudget reads it")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:             true,
            SampleInterval:      1,
            DeviationThreshold:  0.01,
            AutoSwitchThreshold: 0.05,
        },
    }
    e := NewEngine(cfg, "")
    e.countViaMLXFunc = func(ctx context.Context, text string) (int, error) {
        return 500, nil
    }

    _, _ = e.CountTokens(context.TODO(), "hi")

    budget := e.EstimateBudget(10, nil, "model", false, false)
    if !budget.LowConfidence {
        t.Fatal("expected LowConfidence from calibrated deviation")
    }
    t.Logf("deviation-based budget: %+v", budget)
}

func TestCountTokens_LocalCountIncrements(t *testing.T) {
    t.Log("localCount atomic counter should increment when calibration enabled")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled:        true,
            SampleInterval: 100,
        },
    }
    e := NewEngine(cfg, "")

    before := e.localCount.Load()
    _, _ = e.CountTokens(context.TODO(), "test")
    after := e.localCount.Load()
    if after <= before {
        t.Fatalf("expected localCount to increment, before=%d after=%d", before, after)
    }
}

func TestCountTokens_LocalCountNoIncrementWhenDisabled(t *testing.T) {
    t.Log("localCount should not increment when calibration disabled")
    cfg := &config.TokenizerConfig{
        Provider: "whitespace",
        Calibration: config.CalibrationConfig{
            Enabled: false,
        },
    }
    e := NewEngine(cfg, "")

    before := e.localCount.Load()
    _, _ = e.CountTokens(context.TODO(), "test")
    after := e.localCount.Load()
    if after != before {
        t.Fatalf("expected localCount unchanged, before=%d after=%d", before, after)
    }
}
