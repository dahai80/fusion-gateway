package router

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

// LayaClassifier implements IntentClassifier by calling fusion-mlx's
// /v1/laya/decide endpoint (laya-mlx typed decision model, ~421M params,
// sub-15ms P50). Replaces the 30-50ms LLM-based RouterLightClassifier when
// configured via intent_classifier.type = "laya". Issue #191.
//
// The laya endpoint returns structured "answers" for a preset of questions.
// The "router" preset returns: domain (choice), difficulty (score),
// needs_tools (noul), is_sensitive (noul). This classifier maps those
// answers to the gateway's Intent types. When the laya endpoint is
// unavailable (503/timeout) or returns low confidence, the caller falls
// back to RouterLightClassifier then the rule chain — see wireIntentClassifier.
type LayaClassifier struct {
    httpClient    *http.Client
    endpoint      string // fusion-mlx base URL, e.g. http://127.0.0.1:11434
    decideURL     string // cached endpoint + "/v1/laya/decide"
    apiKey        string
    minConfidence float64
    // fallback is the secondary classifier invoked when laya fails or returns
    // low confidence. nil = no fallback (caller defers to rule chain).
    fallback IntentClassifier
}

// NewLayaClassifier builds a LayaClassifier from the intent_classifier config.
// Returns nil if the endpoint is empty. The fallback classifier is set by the
// caller (wireIntentClassifier) after construction so a circular import is
// avoided.
func NewLayaClassifier(cfg config.IntentClassifierConfig) *LayaClassifier {
    endpoint := cfg.Endpoint
    if endpoint == "" {
        endpoint = "http://127.0.0.1:11434"
    }
    // Laya needs a tighter timeout than the LLM classifier — the spec targets
    // sub-15ms. Default 20ms per the issue; fall back on any miss. When the
    // config Timeout is set (backward-compat field, default 2s), use a
    // laya-specific cap: min(cfg.Timeout, 50ms) so a misconfigured large
    // timeout does not block the hot path.
    timeout := 20 * time.Millisecond
    if cfg.Timeout > 0 && cfg.Timeout < 50*time.Millisecond {
        timeout = cfg.Timeout
    }
    minConf := cfg.MinConfidence
    if minConf <= 0 {
        minConf = 0.7
    }
    // Intent classifier hits the same fusion-mlx endpoint as the local
    // backend. When intent_classifier.api_key is empty (the documented
    // default — the "fg-" prefix trips C1 so the key is injected via env),
    // fall back to FUSION_MLX_API_KEY so laya auth works out of the box
    // without a redundant config field.
    apiKey := cfg.APIKey
    if apiKey == "" {
        apiKey = os.Getenv("FUSION_MLX_API_KEY")
    }
    return &LayaClassifier{
        httpClient: &http.Client{
            Timeout:   timeout,
            Transport: httpx.TransportForBackend(config.BackendConfig{BaseURL: endpoint}),
        },
        endpoint:      strings.TrimRight(endpoint, "/"),
        decideURL:     strings.TrimRight(endpoint, "/") + "/v1/laya/decide",
        apiKey:        apiKey,
        minConfidence: minConf,
    }
}

// SetFallback installs the secondary classifier (typically RouterLightClassifier)
// used when laya is unavailable or returns low confidence.
func (c *LayaClassifier) SetFallback(f IntentClassifier) {
    c.fallback = f
}

// layaDecideRequest is the POST /v1/laya/decide body.
type layaDecideRequest struct {
    Prompt   string `json:"prompt"`
    Preset   string `json:"preset"`
}

// layaDecideResponse is the response from /v1/laya/decide.
type layaDecideResponse struct {
    Answers layaAnswers `json:"answers"`
    Usage   layaUsage   `json:"usage"`
}

// layaAnswers holds the structured answers from the router preset.
type layaAnswers struct {
    Domain      layaChoiceAnswer `json:"domain"`
    Difficulty  layaScoreAnswer  `json:"difficulty"`
    NeedsTools  layaNoulAnswer   `json:"needs_tools"`
    IsSensitive layaNoulAnswer   `json:"is_sensitive"`
}

// layaChoiceAnswer is a "choice" type answer (select from options).
type layaChoiceAnswer struct {
    Type         string             `json:"type"`
    Choice       string             `json:"choice"`
    Confidence   float64            `json:"confidence"`
}

// layaScoreAnswer is a "score" type answer (0-N scale).
type layaScoreAnswer struct {
    Type       string  `json:"type"`
    Score      float64 `json:"score"`
    Confidence float64 `json:"confidence"`
}

// layaNoulAnswer is a "noul" type answer (boolean-like, 0..1).
type layaNoulAnswer struct {
    Type       string  `json:"type"`
    Noul       float64 `json:"noul"`
    Confidence float64 `json:"confidence"`
}

type layaUsage struct {
    InputTokens int     `json:"input_tokens"`
    LatencyMs   float64 `json:"latency_ms"`
}

// Classify calls /v1/laya/decide and maps the domain/difficulty answers to
// an Intent. On timeout, HTTP error, or low confidence, falls back to the
// secondary classifier (if set) or returns IntentUnknown.
func (c *LayaClassifier) Classify(ctx context.Context, req *RouteRequest) (*IntentResult, error) {
    query := ""
    if req != nil {
        query = strings.TrimSpace(req.Text)
    }
    if query == "" {
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
    }

    start := time.Now()
    res, err := c.callLaya(ctx, query)
    latency := time.Since(start)
    if err != nil {
        slog.Info("laya classifier failed, falling back",
            "error", err, "latency_ms", latency.Milliseconds())
        return c.fallbackOrUnknown(ctx, req)
    }

    intent, confidence, params := c.mapLayaToIntent(res)
    slog.Info("intent classified by laya",
        "intent", intent,
        "confidence", confidence,
        "domain", res.Answers.Domain.Choice,
        "difficulty", res.Answers.Difficulty.Score,
        "needs_tools", res.Answers.NeedsTools.Noul,
        "is_sensitive", res.Answers.IsSensitive.Noul,
        "latency_ms", latency.Milliseconds(),
    )

    if confidence < c.minConfidence {
        slog.Info("laya confidence below threshold, falling back",
            "confidence", confidence, "min", c.minConfidence)
        return c.fallbackOrUnknown(ctx, req)
    }

    if params == nil {
        params = map[string]string{}
    }
    params["_source"] = "laya"
    params["laya_latency_ms"] = fmt.Sprintf("%.1f", latency.Seconds()*1000)
    return &IntentResult{
        Intent:     intent,
        Confidence: confidence,
        Params:     params,
    }, nil
}

// callLaya issues the HTTP POST to /v1/laya/decide and decodes the response.
func (c *LayaClassifier) callLaya(ctx context.Context, prompt string) (*layaDecideResponse, error) {
    payload, err := json.Marshal(layaDecideRequest{
        Prompt: prompt,
        Preset: "router",
    })
    if err != nil {
        return nil, fmt.Errorf("marshal laya request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.decideURL, bytes.NewReader(payload))
    if err != nil {
        return nil, fmt.Errorf("create laya request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if c.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
    }

    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("laya request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        respBody := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("laya decide returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var lr layaDecideResponse
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&lr); err != nil {
        return nil, fmt.Errorf("decode laya response: %w", err)
    }
    return &lr, nil
}

// mapLayaToIntent maps the laya domain + difficulty answers to a gateway
// Intent. Difficulty overrides domain when high (heavy model needed).
func (c *LayaClassifier) mapLayaToIntent(res *layaDecideResponse) (Intent, float64, map[string]string) {
    domain := strings.ToLower(strings.TrimSpace(res.Answers.Domain.Choice))
    difficulty := res.Answers.Difficulty.Score
    confidence := res.Answers.Domain.Confidence
    if res.Answers.Difficulty.Confidence > confidence {
        confidence = res.Answers.Difficulty.Confidence
    }

    params := map[string]string{
        "domain":      domain,
        "difficulty":  fmt.Sprintf("%.1f", difficulty),
        "needs_tools": fmt.Sprintf("%.2f", res.Answers.NeedsTools.Noul),
        "is_sensitive": fmt.Sprintf("%.2f", res.Answers.IsSensitive.Noul),
    }

    // Difficulty override: high difficulty → heavy model regardless of domain.
    if difficulty >= 3.5 {
        params["difficulty_override"] = "heavy_model"
        return IntentHeavyModel, confidence, params
    }

    var intent Intent
    switch domain {
    case "code", "math_or_logic", "factual_lookup", "data_analysis", "chitchat":
        intent = IntentLightweight
    case "writing":
        intent = IntentHeavyModel
    default:
        intent = IntentUnknown
    }

    // needs_tools > 0.5 flags tool-calling — route to local (fusion-mlx
    // handles tool dispatch). The engine's rule chain still applies.
    if res.Answers.NeedsTools.Noul > 0.5 && intent == IntentUnknown {
        intent = IntentLightweight
        params["needs_tools_flag"] = "true"
    }

    return intent, confidence, params
}

// fallbackOrUnknown delegates to the fallback classifier if set, otherwise
// returns IntentUnknown so the engine defers to the rule chain.
func (c *LayaClassifier) fallbackOrUnknown(ctx context.Context, req *RouteRequest) (*IntentResult, error) {
    if c.fallback != nil {
        res, err := c.fallback.Classify(ctx, req)
        if err != nil || res == nil {
            return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
        }
        if res.Params == nil {
            res.Params = map[string]string{}
        }
        res.Params["_source"] = "laya_fallback"
        return res, nil
    }
    return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
}
