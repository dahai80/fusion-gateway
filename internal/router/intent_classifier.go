package router

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// routerLightLabelSet is the closed set of task types the fusion-router-light
// 1B LoRA adapter (fusion-trainer#11) was trained to emit. All five are
// lightweight, locally-runnable on Apple Silicon via fusion-mlx, so each maps
// to IntentLightweight. Any other token the model emits is treated as
// IntentUnknown so the semantic layer fails open to the rule chain.
var routerLightLabelSet = map[string]struct{}{
    "code":      {},
    "chat":      {},
    "math":      {},
    "translate": {},
    "summary":   {},
}

// RouterLightClassifier is the real D4 semantic intent classifier (issue #22).
// It calls the served fusion-router-light 1B LoRA adapter through the local
// fusion-mlx /v1/chat/completions endpoint, using the OpenAI "adapters" field
// to hot-load the derived LoRA engine. The model emits one of five task-type
// labels; all map to IntentLightweight (local-capable). The classifier is safe
// for concurrent use — it holds no mutable state and creates one HTTP request
// per Classify call.
type RouterLightClassifier struct {
    httpClient *http.Client
    endpoint   string // fusion-mlx base URL, e.g. http://127.0.0.1:11434
    baseModel  string // LoRA base model id
    adapter    string // absolute adapter dir path (adapters.safetensors)
    apiKey     string
}

// NewRouterLightClassifier builds a classifier from the intent_classifier
// config block. Returns nil if the config is incomplete (no base model), so the
// caller falls back to NoopClassifier.
func NewRouterLightClassifier(cfg config.IntentClassifierConfig) *RouterLightClassifier {
    endpoint := cfg.Endpoint
    if endpoint == "" {
        endpoint = "http://127.0.0.1:11434"
    }
    baseModel := cfg.BaseModel
    if cfg.Model != "" {
        baseModel = cfg.Model
    }
    if baseModel == "" {
        slog.Warn("intent classifier disabled: missing base_model", "endpoint", endpoint)
        return nil
    }
    timeout := cfg.Timeout
    if timeout <= 0 {
        timeout = 2 * time.Second
    }
    return &RouterLightClassifier{
        httpClient: &http.Client{Timeout: timeout},
        endpoint:   strings.TrimRight(endpoint, "/"),
        baseModel:  baseModel,
        adapter:    cfg.Adapter,
        apiKey:     cfg.APIKey,
    }
}

// classifyRequest is the OpenAI chat-completion body sent to fusion-mlx. The
// "adapters" field is fusion-mlx's mlx-lm-compatible LoRA path: when set, the
// engine pool lazily creates a derived entry keyed by (model, adapter).
type classifyRequest struct {
    Model       string        `json:"model"`
    Adapters    string        `json:"adapters,omitempty"`
    Messages    []classifyMsg `json:"messages"`
    Temperature float64       `json:"temperature"`
    MaxTokens   int           `json:"max_tokens"`
    Stream      bool          `json:"stream"`
    Stop        []string      `json:"stop,omitempty"`
}

type classifyMsg struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type classifyResponse struct {
    Choices []struct {
        Message struct {
            Content string `json:"content"`
        } `json:"message"`
    } `json:"choices"`
}

// Classify sends the last user message to the router-light adapter and maps the
// emitted task-type label to a gateway Intent. Confidence is 1.0 when the model
// emits a known label and 0.0 otherwise (the small model is deterministic
// enough that label membership is the confidence signal). On any
// transport/decode error it returns an error so classifyAndLog fails open to
// the rule chain.
func (c *RouterLightClassifier) Classify(ctx context.Context, req *RouteRequest) (*IntentResult, error) {
    query := ""
    if req != nil {
        query = strings.TrimSpace(req.Text)
    }
    if query == "" {
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
    }

    prompt := "Classify the intent of the following user query into one of: code, chat, math, translate, summary.\n\nQuery: " + query + "\n\nIntent:"
    body := classifyRequest{
        Model:    c.baseModel,
        Adapters: c.adapter,
        Messages: []classifyMsg{{Role: "user", Content: prompt}},
        // Greedy, short generation: the adapter emits a single label token.
        Temperature: 0.0,
        MaxTokens:   8,
        Stream:      false,
        Stop:        []string{"\n"},
    }
    payload, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("marshal classify request: %w", err)
    }

    url := c.endpoint + "/v1/chat/completions"
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
    if err != nil {
        return nil, fmt.Errorf("create classify request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if c.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
    }

    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("classify request failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        respBody := adapter.ReadErrorBody(resp)
        return nil, fmt.Errorf("classify returned status %d: %s", resp.StatusCode, string(respBody))
    }

    var cr classifyResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(adapter.LimitResponseReader(resp.Body)).Decode(&cr); err != nil {
        return nil, fmt.Errorf("decode classify response: %w", err)
    }
    if len(cr.Choices) == 0 {
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
    }

    label := normalizeLabel(cr.Choices[0].Message.Content)
    if _, ok := routerLightLabelSet[label]; !ok {
        slog.Info("intent classifier emitted unknown label", "label", label, "model", c.baseModel)
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
    }

    slog.Info("intent classified by router-light",
        "task_type", label,
        "model", c.baseModel,
        "has_adapter", c.adapter != "",
    )
    // All five router-light task types are lightweight/local-capable. Record
    // the task type in Params for observability; the engine defers lightweight
    // intents to the rule chain (hardware/circuit-breaker still apply).
    return &IntentResult{
        Intent:     IntentLightweight,
        Confidence: 1.0,
        Params:     map[string]string{"task_type": label},
    }, nil
}

// normalizeLabel lowercases, trims, and strips trailing punctuation/whitespace
// the small model may emit around the label token (e.g. "code." or " Code\n").
func normalizeLabel(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    s = strings.TrimRight(s, ".。，,，\n\r\t :：")
    s = strings.TrimPrefix(s, "intent:")
    return strings.TrimSpace(s)
}
