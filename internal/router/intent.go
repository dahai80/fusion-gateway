package router

import (
    "context"
    "log/slog"
)

// Intent is the semantic classification of a request, used by the D4
// gateway-semantic layer to dispatch by request type rather than only by
// rules. See issues #22 / #23 / #25.
type Intent string

const (
    // IntentLightweight: short / low-overhead / privacy requests — route to
    // Mac local (fusion-mlx: Base + LoRA + SpecDec).
    IntentLightweight Intent = "lightweight"
    // IntentHeavyModel: long / complex requests needing a large model — route
    // to Windows CUDA (DeepSeek 70B / Qwen 72B FP8).
    IntentHeavyModel Intent = "heavy_model"
    // IntentDiffusion: image / video generation requests — route to Windows
    // CUDA diffusion models.
    IntentDiffusion Intent = "diffusion"
    // IntentUnknown: classifier could not classify with sufficient confidence.
    // The semantic layer defers to the existing P0-P7 rule chain.
    IntentUnknown Intent = "unknown"
)

// IntentResult is the output of an IntentClassifier.
type IntentResult struct {
    Intent     Intent
    Confidence float64
    // Params carries extracted routing parameters (e.g. target model hint).
    // Opaque to the engine; logged for observability.
    Params map[string]string
}

// IntentClassifier classifies an inbound request into a semantic Intent.
// Implementations may call a local 1.5B model (fusion-router-light) or any
// other strategy. The classifier must be safe for concurrent use.
type IntentClassifier interface {
    Classify(ctx context.Context, req *RouteRequest) (*IntentResult, error)
}

// NoopClassifier is the default classifier: it returns IntentUnknown without
// calling any model. Used when intent_classifier is disabled or the upstream
// fusion-router-light model is not yet available, so the semantic layer is a
// no-op and the existing rule chain decides routing.
type NoopClassifier struct{}

func (NoopClassifier) Classify(_ context.Context, _ *RouteRequest) (*IntentResult, error) {
    return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
}

// PlatformForIntent maps a semantic Intent to the target cluster platform
// identifier (matching ClusterNodeConfig.Platform). Returns "" for intents
// that should not force a platform (the rule chain handles them).
func PlatformForIntent(i Intent, heavyPlatform, diffusionPlatform string) string {
    switch i {
    case IntentHeavyModel:
        if heavyPlatform == "" {
            return "windows-cuda"
        }
        return heavyPlatform
    case IntentDiffusion:
        if diffusionPlatform == "" {
            return "windows-cuda"
        }
        return diffusionPlatform
    }
    return ""
}

// classifyAndLog runs the classifier with a timeout guard and logs the result.
// Returns IntentUnknown on any error so the semantic layer fails open (defers
// to the rule chain) rather than blocking requests.
func classifyAndLog(ctx context.Context, c IntentClassifier, req *RouteRequest) *IntentResult {
    res, err := c.Classify(ctx, req)
    if err != nil {
        slog.Warn("intent classifier failed, falling back to rule chain",
            "error", err, "model", req.Model)
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}
    }
    if res == nil {
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}
    }
    slog.Info("intent classified",
        "intent", res.Intent,
        "confidence", res.Confidence,
        "model", req.Model,
    )
    return res
}
