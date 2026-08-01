package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"

	"github.com/fusion-gateway/fusion-gateway/internal/safego"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type AnthropicMessage struct {
    Role    string                 `json:"role"`
    Content []AnthropicContentBlock `json:"content"`
}

type AnthropicContentBlock struct {
    Type string `json:"type"`
    Text string `json:"text,omitempty"`
    Source *AnthropicImageSource `json:"source,omitempty"`
    ID    string          `json:"id,omitempty"`
    Name  string          `json:"name,omitempty"`
    Input json.RawMessage `json:"input,omitempty"`
    ToolUseID   string      `json:"tool_use_id,omitempty"`
    ACContent   interface{} `json:"content,omitempty"`
    IsError     bool        `json:"is_error,omitempty"`
    Thinking string `json:"thinking,omitempty"`
    Signature string `json:"signature,omitempty"`
}

type AnthropicImageSource struct {
    Type      string `json:"type"`
    MediaType string `json:"media_type"`
    Data      string `json:"data,omitempty"`
    URL       string `json:"url,omitempty"`
}

type AnthropicTool struct {
    Name        string          `json:"name"`
    Description string          `json:"description,omitempty"`
    InputSchema json.RawMessage `json:"input_schema"`
}

type AnthropicThinkingConfig struct {
    Type         string `json:"type"`
    BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type AnthropicRequest struct {
    Model       string                   `json:"model"`
    Messages    []AnthropicMessage       `json:"messages"`
    System      interface{}              `json:"system,omitempty"`
    MaxTokens   int                      `json:"max_tokens"`
    Stream      bool                     `json:"stream"`
    Tools       []AnthropicTool          `json:"tools,omitempty"`
    ToolChoice  interface{}              `json:"tool_choice,omitempty"`
    Thinking    *AnthropicThinkingConfig `json:"thinking,omitempty"`
    Temperature *float64                 `json:"temperature,omitempty"`
    TopP        *float64                 `json:"top_p,omitempty"`
    StopSequences []string              `json:"stop_sequences,omitempty"`
    Metadata    interface{}              `json:"metadata,omitempty"`
}

type AnthropicResponse struct {
    ID           string                  `json:"id"`
    Type         string                  `json:"type"`
    Role         string                  `json:"role"`
    Content      []AnthropicContentBlock `json:"content"`
    Model        string                  `json:"model"`
    StopReason   string                  `json:"stop_reason"`
    StopSequence *string                 `json:"stop_sequence,omitempty"`
    Usage        AnthropicUsage          `json:"usage"`
}

type AnthropicUsage struct {
    InputTokens  int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type AnthropicStreamEvent struct {
    Type         string                 `json:"type"`
    Message      *AnthropicResponse     `json:"message,omitempty"`
    Index        int                    `json:"index,omitempty"`
    ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`
    Delta        json.RawMessage        `json:"delta,omitempty"`
    Usage        *AnthropicUsage        `json:"usage,omitempty"`
    MessageID    string                 `json:"message_id,omitempty"`
    Model        string                 `json:"model,omitempty"`
    StopReason   string                 `json:"stop_reason,omitempty"`
    StopSequence *string                `json:"stop_sequence,omitempty"`
}

type AnthropicProvider struct {
    name       string
    baseURL    string
    apiKey     string
    apiVersion string
    httpClient *http.Client
}

func NewAnthropicProvider(name string, backendCfg config.BackendConfig) *AnthropicProvider {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 120 * time.Second
    }
    return &AnthropicProvider{
        name:       name,
        baseURL:    backendCfg.BaseURL,
        apiKey:     backendCfg.APIKey,
        apiVersion: "2023-06-01",
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (p *AnthropicProvider) Name() string { return p.name }

func (p *AnthropicProvider) HealthCheck(ctx context.Context) error { return nil }

func (p *AnthropicProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    antReq := OpenAIToAnthropic(req)
    antReq.Stream = false
    body, err := json.Marshal(antReq)
    if err != nil {
        return nil, fmt.Errorf("marshal anthropic request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create anthropic request: %w", err)
    }
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("anthropic request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("anthropic returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var antResp AnthropicResponse
    if err := json.NewDecoder(resp.Body).Decode(&antResp); err != nil {
        return nil, fmt.Errorf("decode anthropic response: %w", err)
    }
    return AnthropicToOpenAI(&antResp), nil
}

func (p *AnthropicProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    antReq := OpenAIToAnthropic(req)
    antReq.Stream = true
    body, err := json.Marshal(antReq)
    if err != nil {
        return nil, fmt.Errorf("marshal anthropic stream request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create anthropic stream request: %w", err)
    }
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("anthropic stream request failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("anthropic stream returned status %d: %s", resp.StatusCode, string(respBody))
    }
    ch := make(chan StreamChunk, 64)
    safego.Go("anthropic_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        p.parseAnthropicSSE(resp.Body, ch, req.Model)
    })
    return ch, nil
}

func (p *AnthropicProvider) Messages(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error) {
    req.Stream = false
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal anthropic messages request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create anthropic messages request: %w", err)
    }
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("anthropic messages request failed: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("anthropic messages returned status %d: %s", resp.StatusCode, string(respBody))
    }
    var antResp AnthropicResponse
    if err := json.NewDecoder(resp.Body).Decode(&antResp); err != nil {
        return nil, fmt.Errorf("decode anthropic messages response: %w", err)
    }
    return &antResp, nil
}

func (p *AnthropicProvider) StreamMessages(ctx context.Context, req *AnthropicRequest) (<-chan AnthropicStreamEvent, error) {
    req.Stream = true
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal anthropic stream messages request: %w", err)
    }
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create anthropic stream messages request: %w", err)
    }
    p.setHeaders(httpReq)
    InjectFusionHeaders(ctx, httpReq)
    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("anthropic stream messages failed: %w", err)
    }
    if resp.StatusCode != http.StatusOK {
        respBody, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("anthropic stream messages returned status %d: %s", resp.StatusCode, string(respBody))
    }
    ch := make(chan AnthropicStreamEvent, 64)
    safego.Go("anthropic_stream", func() {
        defer close(ch)
        defer resp.Body.Close()
        p.parseAnthropicStreamEvents(resp.Body, ch)
    })
    return ch, nil
}

func (p *AnthropicProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return nil, fmt.Errorf("anthropic: embeddings not supported")
}

func (p *AnthropicProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return nil, fmt.Errorf("anthropic: rerank not supported")
}

func (p *AnthropicProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return []ModelInfo{
        {ID: "claude-3-5-sonnet-20241022", Object: "model", OwnedBy: "anthropic"},
        {ID: "claude-3-5-haiku-20241022", Object: "model", OwnedBy: "anthropic"},
        {ID: "claude-3-opus-20240229", Object: "model", OwnedBy: "anthropic"},
        {ID: "claude-sonnet-4-20250514", Object: "model", OwnedBy: "anthropic"},
        {ID: "claude-opus-4-20250514", Object: "model", OwnedBy: "anthropic"},
    }, nil
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("x-api-key", p.apiKey)
    req.Header.Set("anthropic-version", p.apiVersion)
}

func (p *AnthropicProvider) parseAnthropicSSE(body io.Reader, ch chan<- StreamChunk, model string) {
    var outputTokens int
    var msgID string
    buf := make([]byte, 4096)
    var lineBuf []byte
    const maxLineSize = 1 << 20 // 1 MiB cap per line to prevent unbounded growth
    for {
        n, err := body.Read(buf)
        if n > 0 {
            lineBuf = append(lineBuf, buf[:n]...)
            if len(lineBuf) > maxLineSize {
                slog.Error("anthropic sse line exceeded max size, discarding", "size", len(lineBuf))
                lineBuf = nil
            }
        }
        for {
            idx := bytes.IndexByte(lineBuf, byte('\n'))
            if idx < 0 {
                break
            }
            line := string(bytes.TrimSpace(lineBuf[:idx]))
            lineBuf = lineBuf[idx+1:]
            if line == "" || strings.HasPrefix(line, ":") {
                continue
            }
            if !strings.HasPrefix(line, "data: ") {
                continue
            }
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" {
                return
            }
            var event AnthropicStreamEvent
            if err := json.Unmarshal([]byte(data), &event); err != nil {
                slog.Warn("anthropic sse unmarshal error", "error", err, "data", data)
                continue
            }
            switch event.Type {
            case "message_start":
                if event.Message != nil {
                    msgID = event.Message.ID
                    outputTokens = event.Message.Usage.OutputTokens
                }
            case "content_block_delta":
                var delta struct {
                    Type string `json:"type"`
                    Text string `json:"text,omitempty"`
                }
                if err := json.Unmarshal(event.Delta, &delta); err == nil && delta.Type == "text_delta" {
                    chunk := StreamChunk{
                        ID:      msgID,
                        Object:  "chat.completion.chunk",
                        Created: time.Now().Unix(),
                        Model:   model,
                        Choices: []ChoiceDelta{{
                            Index: event.Index,
                            Delta: map[string]string{"role": "assistant", "content": delta.Text},
                        }},
                    }
                    select {
                    case ch <- chunk:
                    default:
                        slog.Warn("anthropic sse backpressure")
                        return
                    }
                }
            case "message_delta":
                if event.Usage != nil {
                    outputTokens = event.Usage.OutputTokens
                }
                stopReason := event.StopReason
                if stopReason == "end_turn" {
                    stopReason = "stop"
                }
                fr := stopReason
                if fr == "" {
                    fr = "stop"
                }
                chunk := StreamChunk{
                    ID:      msgID,
                    Object:  "chat.completion.chunk",
                    Created: time.Now().Unix(),
                    Model:   model,
                    Choices: []ChoiceDelta{{
                        Index:        0,
                        FinishReason: &fr,
                    }},
                    Usage: &UsageResponse{
                        PromptTokens:     0,
                        CompletionTokens: outputTokens,
                        TotalTokens:      outputTokens,
                    },
                }
                select {
                case ch <- chunk:
                default:
                }
            case "message_stop":
                return
            }
        }
        if err != nil {
            if err != io.EOF {
                slog.Error("anthropic sse read error", "error", err)
            }
            break
        }
    }
}

func (p *AnthropicProvider) parseAnthropicStreamEvents(body io.Reader, ch chan<- AnthropicStreamEvent) {
    buf := make([]byte, 4096)
    var lineBuf []byte
    const maxLineSize = 1 << 20 // 1 MiB cap per line to prevent unbounded growth
    for {
        n, err := body.Read(buf)
        if n > 0 {
            lineBuf = append(lineBuf, buf[:n]...)
            if len(lineBuf) > maxLineSize {
                slog.Error("anthropic stream event line exceeded max size, discarding", "size", len(lineBuf))
                lineBuf = nil
            }
        }
        for {
            idx := bytes.IndexByte(lineBuf, 10)
            if idx < 0 { break }
            line := string(bytes.TrimSpace(lineBuf[:idx]))
            lineBuf = lineBuf[idx+1:]
            if line == "" || strings.HasPrefix(line, ":") { continue }
            if !strings.HasPrefix(line, "data: ") { continue }
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" { return }
            var event AnthropicStreamEvent
            if err := json.Unmarshal([]byte(data), &event); err != nil {
                slog.Warn("anthropic stream event unmarshal error", "error", err)
                continue
            }
            select {
            case ch <- event:
            default:
                slog.Warn("anthropic stream event backpressure")
                return
            }
        }
        if err != nil {
            if err != io.EOF {
                slog.Error("anthropic stream event read error", "error", err)
            }
            break
        }
    }
}

func OpenAIToAnthropic(req *ChatRequest) *AnthropicRequest {
    antReq := &AnthropicRequest{
        Model:     req.Model,
        MaxTokens: 4096,
        Stream:    req.Stream,
    }
    if req.MaxTokens != nil {
        antReq.MaxTokens = *req.MaxTokens
    }
    if req.Temperature != nil { antReq.Temperature = req.Temperature }
    if req.TopP != nil { antReq.TopP = req.TopP }
    if len(req.Stop) > 0 { antReq.StopSequences = req.Stop }

    var systemTexts []string
    var messages []AnthropicMessage
    for _, msg := range req.Messages {
        if msg.Role == "system" {
            systemTexts = append(systemTexts, contentToString(msg.Content))
            continue
        }
        antMsg := AnthropicMessage{
            Role:    msg.Role,
            Content: convertContentToBlocks(msg.Content),
        }
        if msg.Role == "assistant" && len(antMsg.Content) == 0 {
            antMsg.Content = []AnthropicContentBlock{{Type: "text", Text: contentToString(msg.Content)}}
        }
        messages = append(messages, antMsg)
    }
    antReq.Messages = messages
    if len(systemTexts) > 0 {
        antReq.System = strings.Join(systemTexts, "\n\n")
    }

    if req.Tools != nil {
        toolsRaw, ok := req.Tools.([]interface{})
        if ok {
            for _, t := range toolsRaw {
                toolMap, ok := t.(map[string]interface{})
                if !ok { continue }
                fn, ok := toolMap["function"].(map[string]interface{})
                if !ok { continue }
                name, _ := fn["name"].(string)
                desc, _ := fn["description"].(string)
                schema, _ := fn["parameters"].(json.RawMessage)
                if schema == nil {
                    schemaBytes, _ := json.Marshal(fn["parameters"])
                    schema = schemaBytes
                }
                antReq.Tools = append(antReq.Tools, AnthropicTool{
                    Name: name, Description: desc, InputSchema: schema,
                })
            }
        }
    }
    if req.ToolChoice != nil { antReq.ToolChoice = req.ToolChoice }
    return antReq
}

func AnthropicToOpenAI(resp *AnthropicResponse) *ChatResponse {
    var toolCalls []interface{}
    var textContent string
    for _, block := range resp.Content {
        switch block.Type {
        case "text":
            textContent += block.Text
        case "tool_use":
            tc := map[string]interface{}{
                "id":   block.ID,
                "type": "function",
                "function": map[string]interface{}{
                    "name":      block.Name,
                    "arguments": string(block.Input),
                },
            }
            toolCalls = append(toolCalls, tc)
        }
    }
    finishReason := resp.StopReason
    if finishReason == "end_turn" { finishReason = "stop" }
    if finishReason == "tool_use" { finishReason = "tool_calls" }
    message := map[string]interface{}{
        "role":    resp.Role,
        "content": textContent,
    }
    if len(toolCalls) > 0 { message["tool_calls"] = toolCalls }
    return &ChatResponse{
        ID:      resp.ID,
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   resp.Model,
        Choices: []ChatChoice{{
            Index:        0,
            Message:      message,
            FinishReason: finishReason,
        }},
        Usage: UsageResponse{
            PromptTokens:     resp.Usage.InputTokens,
            CompletionTokens: resp.Usage.OutputTokens,
            TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
        },
    }
}

func AnthropicToOpenAIChatRequest(antReq *AnthropicRequest) *ChatRequest {
    chatReq := &ChatRequest{
        Model:  antReq.Model,
        Stream: antReq.Stream,
    }
    maxTokens := antReq.MaxTokens
    chatReq.MaxTokens = &maxTokens
    if antReq.Temperature != nil { chatReq.Temperature = antReq.Temperature }
    if antReq.TopP != nil { chatReq.TopP = antReq.TopP }
    if len(antReq.StopSequences) > 0 { chatReq.Stop = antReq.StopSequences }

    if antReq.System != nil {
        sysText := ""
        switch v := antReq.System.(type) {
        case string:
            sysText = v
        default:
            b, _ := json.Marshal(v)
            sysText = string(b)
        }
        if sysText != "" {
            chatReq.Messages = append(chatReq.Messages, ChatMessage{Role: "system", Content: sysText})
        }
    }

    for _, antMsg := range antReq.Messages {
        content := anthropicBlocksToContent(antMsg.Content)
        chatReq.Messages = append(chatReq.Messages, ChatMessage{Role: antMsg.Role, Content: content})
    }

    if len(antReq.Tools) > 0 {
        var tools []interface{}
        for _, t := range antReq.Tools {
            tools = append(tools, map[string]interface{}{
                "type": "function",
                "function": map[string]interface{}{
                    "name":        t.Name,
                    "description": t.Description,
                    "parameters":  t.InputSchema,
                },
            })
        }
        chatReq.Tools = tools
    }
    if antReq.ToolChoice != nil { chatReq.ToolChoice = antReq.ToolChoice }
    return chatReq
}

func anthropicBlocksToContent(blocks []AnthropicContentBlock) interface{} {
    if len(blocks) == 0 { return "" }
    if len(blocks) == 1 && blocks[0].Type == "text" { return blocks[0].Text }

    var parts []interface{}
    for _, b := range blocks {
        switch b.Type {
        case "text":
            parts = append(parts, map[string]interface{}{"type": "text", "text": b.Text})
        case "image":
            if b.Source != nil {
                parts = append(parts, map[string]interface{}{
                    "type": "image_url",
                    "image_url": map[string]interface{}{
                        "url": fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data),
                    },
                })
            }
        case "tool_use":
            parts = append(parts, map[string]interface{}{
                "type": "tool_use",
                "id":   b.ID,
                "name": b.Name,
                "input": string(b.Input),
            })
        case "tool_result":
            parts = append(parts, map[string]interface{}{
                "type":       "tool_result",
                "tool_use_id": b.ToolUseID,
                "content":    b.ACContent,
                "is_error":   b.IsError,
            })
        default:
            part, _ := json.Marshal(b)
            var raw interface{}
            _ = json.Unmarshal(part, &raw)
            parts = append(parts, raw)
        }
    }
    return parts
}

func convertContentToBlocks(content interface{}) []AnthropicContentBlock {
    if content == nil { return nil }
    switch v := content.(type) {
    case string:
        if v == "" { return nil }
        return []AnthropicContentBlock{{Type: "text", Text: v}}
    case []interface{}:
        var blocks []AnthropicContentBlock
        for _, item := range v {
            m, ok := item.(map[string]interface{})
            if !ok { continue }
            typ, _ := m["type"].(string)
            switch typ {
            case "text":
                text, _ := m["text"].(string)
                blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
            case "image_url":
                imageURL, _ := m["image_url"].(map[string]interface{})
                url, _ := imageURL["url"].(string)
                if strings.HasPrefix(url, "data:") {
                    parts := strings.SplitN(url, ";", 2)
                    mediaType := "image/png"
                    if len(parts) > 0 { mediaType = strings.TrimPrefix(parts[0], "data:") }
                    base64Data := ""
                    if idx := strings.Index(url, ","); idx >= 0 { base64Data = url[idx+1:] }
                    blocks = append(blocks, AnthropicContentBlock{
                        Type: "image",
                        Source: &AnthropicImageSource{Type: "base64", MediaType: mediaType, Data: base64Data},
                    })
                } else {
                    blocks = append(blocks, AnthropicContentBlock{
                        Type: "image",
                        Source: &AnthropicImageSource{Type: "url", URL: url},
                    })
                }
            case "tool_use":
                id, _ := m["id"].(string)
                name, _ := m["name"].(string)
                input, _ := m["input"].(json.RawMessage)
                if input == nil {
                    inputBytes, _ := json.Marshal(m["input"])
                    input = inputBytes
                }
                blocks = append(blocks, AnthropicContentBlock{Type: "tool_use", ID: id, Name: name, Input: input})
            case "tool_result":
                toolUseID, _ := m["tool_use_id"].(string)
                blocks = append(blocks, AnthropicContentBlock{Type: "tool_result", ToolUseID: toolUseID, ACContent: m["content"]})
            }
        }
        return blocks
    default:
        b, err := json.Marshal(content)
        if err != nil { return nil }
        return []AnthropicContentBlock{{Type: "text", Text: string(b)}}
    }
}

func contentToString(content interface{}) string {
    switch v := content.(type) {
    case string:
        return v
    case []interface{}:
        var parts []string
        for _, item := range v {
            m, ok := item.(map[string]interface{})
            if !ok { continue }
            if m["type"] == "text" {
                if text, ok := m["text"].(string); ok { parts = append(parts, text) }
            }
        }
        return strings.Join(parts, "\n")
    default:
        b, _ := json.Marshal(content)
        return string(b)
    }
}
