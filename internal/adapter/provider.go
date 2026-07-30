package adapter

import (
    "context"
)

type ChatMessage struct {
    Role    string      `json:"role"`
    Content interface{} `json:"content"`
}

type ChatRequest struct {
    Model       string        `json:"model"`
    Messages    []ChatMessage `json:"messages"`
    Temperature *float64      `json:"temperature,omitempty"`
    MaxTokens   *int          `json:"max_tokens,omitempty"`
    Stream      bool          `json:"stream"`
    Stop        []string      `json:"stop,omitempty"`
    TopP        *float64      `json:"top_p,omitempty"`
    Tools       interface{}   `json:"tools,omitempty"`
    ToolChoice  interface{}   `json:"tool_choice,omitempty"`
}

type StreamChunk struct {
    ID        string         `json:"id"`
    Object    string         `json:"object"`
    Created   int64          `json:"created"`
    Model     string         `json:"model"`
    Choices   []ChoiceDelta  `json:"choices"`
    Usage     *UsageResponse `json:"usage,omitempty"`
    Degraded  bool           `json:"degraded,omitempty"`
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
    ID               string   `json:"id"`
    Object           string   `json:"object"`
    OwnedBy          string   `json:"owned_by"`
    AvailableBackends []string `json:"available_backends,omitempty"`
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
