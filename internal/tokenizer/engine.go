package tokenizer

import (
    "context"
    "log/slog"
    "sync"
    "sync/atomic"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type TokenBudget struct {
    InputTokens         int
    PredictOutputTokens int
    TotalBudget         int
    LowConfidence       bool
}

type contextKey string

const TokenBudgetKey contextKey = "token_budget"

func WithTokenBudget(ctx context.Context, budget TokenBudget) context.Context {
    return context.WithValue(ctx, TokenBudgetKey, budget)
}

func BudgetFromContext(ctx context.Context) (TokenBudget, bool) {
    b, ok := ctx.Value(TokenBudgetKey).(TokenBudget)
    return b, ok
}

type Engine struct {
    cfg              *config.TokenizerConfig
    localCount       atomic.Int64
    calibMu          sync.Mutex
    calibState       *calibrationState
    mlxURL           string
    countViaMLXFunc  func(ctx context.Context, text string) (int, error)
}

type calibrationState struct {
    samplesSinceLastCalib int
    lastDeviation         float64
}

func NewEngine(cfg *config.TokenizerConfig, mlxURL string) *Engine {
    return &Engine{
        cfg:    cfg,
        mlxURL: mlxURL,
        calibState: &calibrationState{},
    }
}

func (e *Engine) CountTokens(ctx context.Context, text string) (int, error) {
    count := e.countLocal(text)

    if e.cfg.Calibration.Enabled {
        e.localCount.Add(1)
        e.maybeCalibrate(ctx, text, count)
    }

    return count, nil
}

func (e *Engine) EstimateBudget(inputTokens int, maxTokens *int, model string, hasTools bool, isStream bool) TokenBudget {
    budget := TokenBudget{
        InputTokens: inputTokens,
    }

    if maxTokens != nil && *maxTokens > 0 {
        budget.PredictOutputTokens = *maxTokens
    } else {
        budget.PredictOutputTokens = e.defaultMaxTokens(model, hasTools, isStream)
    }

    budget.TotalBudget = budget.InputTokens + budget.PredictOutputTokens

    e.calibMu.Lock()
    if e.calibState.lastDeviation > e.cfg.Calibration.AutoSwitchThreshold {
        budget.LowConfidence = true
    }
    e.calibMu.Unlock()

    return budget
}

func (e *Engine) defaultMaxTokens(model string, hasTools bool, isStream bool) int {
    switch {
    case hasTools:
        return e.cfg.ScenePresets.ToolCall
    case isStream && !hasTools:
        return e.cfg.ScenePresets.Chat
    default:
        // Use context window ratio with minimum
        return e.cfg.MinMaxTokens
    }
}

func (e *Engine) countLocal(text string) int {
    // Simple whitespace-based estimation for MVP
    // Phase 2 will integrate donge/go-tokenizer for precise counting
    words := 0
    for _, r := range text {
        if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
            words++
        }
    }
    if len(text) > 0 {
        words++
    }
    // Rough heuristic: ~1.3 tokens per word for English, ~2.0 for Chinese
    return int(float64(words) * 1.5)
}

func (e *Engine) maybeCalibrate(ctx context.Context, text string, localCount int) {
    e.calibMu.Lock()
    defer e.calibMu.Unlock()

    e.calibState.samplesSinceLastCalib++

    if e.calibState.samplesSinceLastCalib < e.cfg.Calibration.SampleInterval {
        return
    }

    e.calibState.samplesSinceLastCalib = 0

    // Perform calibration against fusion-mlx /v1/count_tokens
    mlxCount, err := e.countViaMLX(ctx, text)
    if err != nil {
        slog.Warn("tokenizer calibration failed", "error", err)
        return
    }

    if mlxCount == 0 {
        return
    }

    deviation := float64(localCount-mlxCount) / float64(mlxCount)
    if deviation < 0 {
        deviation = -deviation
    }

    e.calibState.lastDeviation = deviation

    if deviation > e.cfg.Calibration.DeviationThreshold {
        slog.Warn("tokenizer deviation exceeds threshold",
            "deviation", deviation,
            "local_count", localCount,
            "mlx_count", mlxCount,
            "threshold", e.cfg.Calibration.DeviationThreshold,
        )
    }

    if deviation > e.cfg.Calibration.AutoSwitchThreshold {
        slog.Error("tokenizer deviation exceeds auto-switch threshold, marking low confidence",
            "deviation", deviation,
            "threshold", e.cfg.Calibration.AutoSwitchThreshold,
        )
    }
}

func (e *Engine) countViaMLX(ctx context.Context, text string) (int, error) {
    if e.countViaMLXFunc != nil {
        return e.countViaMLXFunc(ctx, text)
    }
    // Phase 2: implement fusion-mlx /v1/count_tokens call
    // For MVP, return 0 to skip calibration
    return 0, nil
}
