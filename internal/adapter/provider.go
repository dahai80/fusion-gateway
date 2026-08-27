package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
)

type fusionHeadersKey struct{}

var fusionPassthroughHeaders = []string{
    "X-Fusion-Project-Id",
    "X-Fusion-Chat-Id",
    "X-Fusion-Route",
    "X-Space-Id",
}

func WithFusionHeaders(ctx context.Context, r *http.Request) context.Context {
    headers := make(map[string]string)
    for _, h := range fusionPassthroughHeaders {
        if v := r.Header.Get(h); v != "" {
            headers[h] = v
        }
    }
    if len(headers) == 0 {
        return ctx
    }
    return context.WithValue(ctx, fusionHeadersKey{}, headers)
}

func InjectFusionHeaders(ctx context.Context, req *http.Request) {
    headers, _ := ctx.Value(fusionHeadersKey{}).(map[string]string)
    for k, v := range headers {
        req.Header.Set(k, v)
    }
}

// FusionHeadersFromContext returns the passthrough header map carried in ctx
// (set by WithFusionHeaders), or nil. Used to copy fusion headers onto a
// decoupled ctx (e.g. the resumable-stream pump's liveCtx, issue #116) so
// outbound upstream requests still propagate X-Request-ID / auth headers.
func FusionHeadersFromContext(ctx context.Context) map[string]string {
    headers, _ := ctx.Value(fusionHeadersKey{}).(map[string]string)
    return headers
}

// WithFusionHeadersMap attaches a pre-built header map to ctx (the inverse of
// FusionHeadersFromContext), so a decoupled ctx inherits the inbound headers
// without an *http.Request.
func WithFusionHeadersMap(ctx context.Context, headers map[string]string) context.Context {
    if len(headers) == 0 {
        return ctx
    }
    cp := make(map[string]string, len(headers))
    for k, v := range headers {
        cp[k] = v
    }
    return context.WithValue(ctx, fusionHeadersKey{}, cp)
}

func SpaceIDFromContext(ctx context.Context) string {
    headers, _ := ctx.Value(fusionHeadersKey{}).(map[string]string)
    if headers == nil {
        return ""
    }
    return headers["X-Space-Id"]
}

type ChatMessage struct {
    Role    string      `json:"role"`
    Content interface{} `json:"content"`
}

type ChatRequest struct {
    Model         string         `json:"model"`
    Messages      []ChatMessage  `json:"messages"`
    Temperature   *float64       `json:"temperature,omitempty"`
    MaxTokens     *int           `json:"max_tokens,omitempty"`
    Stream        bool           `json:"stream"`
    StreamOptions *StreamOptions `json:"stream_options,omitempty"`
    Stop          []string       `json:"stop,omitempty"`
    TopP          *float64       `json:"top_p,omitempty"`
    Tools         interface{}    `json:"tools,omitempty"`
    ToolChoice    interface{}    `json:"tool_choice,omitempty"`
    // Adapters passes one or more LoRA adapter names to fusion-mlx so it
    // hot-mounts the derived engine (e.g. "lora-code") on top of the pre-loaded
    // base model. Opaque to cloud providers; only fusion-mlx consumes it. May
    // be a string or []string — fusion-mlx accepts both shapes.
    Adapters      interface{}    `json:"adapters,omitempty"`
    // ResponseFormat passes OpenAI constrained-decoding params (json_schema /
    // json_object) through to fusion-mlx (xgrammar/llguidance backend). The
    // gateway does not interpret it; fusion-mlx enforces the structure.
    ResponseFormat interface{}   `json:"response_format,omitempty"`
}

type StreamOptions struct {
    IncludeUsage bool `json:"include_usage"`
}

type StreamChunk struct {
    ID        string         `json:"id"`
    Object    string         `json:"object"`
    Created   int64          `json:"created"`
    Model     string         `json:"model"`
    Choices   []ChoiceDelta  `json:"choices"`
    Usage     *UsageResponse `json:"usage,omitempty"`
    Degraded  bool           `json:"degraded,omitempty"`
    // E1 (audit): Raw carries the verbatim upstream SSE data bytes for
    // OpenAI-wire-format providers (fusion-mlx, openai_compatible). When set,
    // the server forward loop emits Raw directly instead of re-marshaling the
    // struct — skipping the json.Marshal the audit found burning 1-2 cores of
    // pure (de)serialization at ~50 concurrent long streams. The struct fields
    // above are still populated (parseSSEStream unmarshals into the struct for
    // usage/ID/model tracking AND sets Raw), so observability + token counting
    // are unchanged. omitempty keeps a freshly-built chunk (no Raw, e.g. the
    // synthetic usage chunk) marshaling exactly as before. Anthropic-format
    // providers build converted chunks (Anthropic→OpenAI) so they leave Raw
    // nil and the marshal path is unchanged — passthrough is only for paths
    // where the wire format is already OpenAI chunks.
    Raw json.RawMessage `json:"-"`
}

type ChoiceDelta struct {
    Index        int         `json:"index"`
    Delta        interface{} `json:"delta"`
    FinishReason *string     `json:"finish_reason,omitempty"`
}

type ChatResponse struct {
    ID      string         `json:"id"`
    Object  string         `json:"object"`
    Created int64          `json:"created"`
    Model   string         `json:"model"`
    Choices []ChatChoice   `json:"choices"`
    Usage   UsageResponse  `json:"usage"`
}

type ChatChoice struct {
    Index        int         `json:"index"`
    Message      interface{} `json:"message"`
    FinishReason string      `json:"finish_reason"`
}

type UsageResponse struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}

type EmbeddingRequest struct {
    Model string   `json:"model"`
    Input []string `json:"input"`
}

func (r *EmbeddingRequest) UnmarshalJSON(data []byte) error {
    type alias EmbeddingRequest
    var raw struct {
        alias
        Input json.RawMessage `json:"input"`
    }
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }
    r.Model = raw.alias.Model
    if len(raw.Input) == 0 {
        return nil
    }
    trimmed := bytes.TrimSpace(raw.Input)
    if len(trimmed) > 0 && trimmed[0] == '"' {
        var s string
        if err := json.Unmarshal(trimmed, &s); err != nil {
            return err
        }
        r.Input = []string{s}
        return nil
    }
    var inputs []string
    if err := json.Unmarshal(trimmed, &inputs); err != nil {
        return err
    }
    r.Input = inputs
    return nil
}

type EmbeddingResponse struct {
    Object string            `json:"object"`
    Data   []EmbeddingData   `json:"data"`
    Model  string            `json:"model"`
    Usage  UsageResponse     `json:"usage"`
}

type EmbeddingData struct {
    Object    string    `json:"object"`
    Embedding []float64 `json:"embedding"`
    Index     int       `json:"index"`
}

type RerankRequest struct {
    Model           string   `json:"model"`
    Query           string   `json:"query"`
    Documents       []string `json:"documents"`
    TopN            *int     `json:"top_n,omitempty"`
    ReturnDocuments bool     `json:"return_documents,omitempty"`
}

type RerankResponse struct {
    ID      string         `json:"id"`
    Model   string         `json:"model"`
    Results []RerankResult `json:"results"`
    Usage   UsageResponse  `json:"usage"`
}

type RerankResult struct {
    Index          int     `json:"index"`
    RelevanceScore float64 `json:"relevance_score"`
    Document       *string `json:"document,omitempty"`
}

type ModelInfo struct {
    ID                string   `json:"id"`
    Object            string   `json:"object"`
    OwnedBy           string   `json:"owned_by"`
    AvailableBackends []string `json:"available_backends,omitempty"`
    // Loaded reports whether the model is actually resident in a local engine
    // (fusion-mlx). false for cloud-only or registered-but-unloaded local
    // models so downstream consumers can distinguish "listed" from "servable"
    // (#59). Omitted for cloud models where the concept does not apply.
    Loaded bool `json:"loaded,omitempty"`
}

type ImageRequest struct {
    Model  string `json:"model"`
    Prompt string `json:"prompt"`
    N      int    `json:"n,omitempty"`
    Size   string `json:"size,omitempty"`
}

type ImageResponse struct {
    Created int64       `json:"created"`
    Data    []ImageData `json:"data"`
}

type ImageData struct {
    URL           string `json:"url,omitempty"`
    B64JSON       string `json:"b64_json,omitempty"`
    RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type Provider interface {
    Name() string
    HealthCheck(ctx context.Context) error
    Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
    StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error)
    Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)
    Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error)
    ListModels(ctx context.Context) ([]ModelInfo, error)
}
