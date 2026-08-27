package server

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// blockingProvider simulates an unreachable cloud backend: ListModels blocks
// until the per-provider timeout cancels its context.
type blockingProvider struct {
    mockProvider
}

func (b *blockingProvider) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
    <-ctx.Done()
    return nil, ctx.Err()
}

func TestHandleModels_ConcurrentSkipsFailedProvider(t *testing.T) {
    s := newTestServer()
    s.pool.Register("local-mlx", &mockProvider{
        name:   "local-mlx",
        models: []adapter.ModelInfo{{ID: "qwen3", OwnedBy: "fusion-mlx"}},
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})
    s.pool.Register("cloud-dead", &mockProvider{
        name:      "cloud-dead",
        modelsErr: fmt.Errorf("connection refused"),
    }, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    var resp struct {
        Object string              `json:"object"`
        Data   []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if resp.Object != "list" {
        t.Errorf("expected object=list, got %q", resp.Object)
    }
    if len(resp.Data) != 1 || resp.Data[0].ID != "qwen3" {
        t.Fatalf("expected only local model qwen3 (cloud failed must be skipped), got %+v", resp.Data)
    }
}

func TestHandleModels_ModeLocalOnlyReturnsLocal(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.Mode = "local"
    s.pool.Register("local-mlx", &mockProvider{
        name:   "local-mlx",
        models: []adapter.ModelInfo{{ID: "qwen3", OwnedBy: "fusion-mlx"}},
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})
    s.pool.Register("cloud-openai", &mockProvider{
        name:   "cloud-openai",
        models: []adapter.ModelInfo{{ID: "gpt-4", OwnedBy: "openai"}},
    }, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)

    var resp struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if len(resp.Data) != 1 || resp.Data[0].ID != "qwen3" {
        t.Fatalf("mode=local must only return local provider models, got %+v", resp.Data)
    }
    for _, m := range resp.Data {
        if m.ID == "gpt-4" {
            t.Fatalf("mode=local must not return cloud models, but got gpt-4")
        }
    }
}

// TestHandleModels_LocalModelsListedFirst verifies that /v1/models orders
// local (owned_by="local") models ahead of cloud models, with loaded local
// models ahead of unloaded local models. Cloud providers are registered first
// in config (anthropic sits at the top of `backends:`), so without an explicit
// sort their models would dominate the head of the response and mask which
// models are actually served locally (#108 observation 2). Cloud model IDs
// ("a-cloud-1") sort alphabetically before local IDs ("z-local-1"), so a
// plain alphabetical sort would fail this test — only a local-first rule can
// put z-local-1 at index 0.
func TestHandleModels_LocalModelsListedFirst(t *testing.T) {
    s := newTestServer()
    // cloud provider registered first; its model IDs sort ahead alphabetically
    s.pool.Register("glm52", &mockProvider{
        name: "glm52",
        models: []adapter.ModelInfo{
            {ID: "a-cloud-1", OwnedBy: "anthropic"},
            {ID: "a-cloud-2", OwnedBy: "anthropic"},
        },
    }, config.BackendConfig{Type: "anthropic", Enabled: true})
    s.pool.Register("fusion-mlx", &mockProvider{
        name: "fusion-mlx",
        models: []adapter.ModelInfo{
            {ID: "z-local-unloaded", OwnedBy: "local"},
            {ID: "z-local-loaded", OwnedBy: "local", Loaded: true},
        },
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})

    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    var resp struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if len(resp.Data) != 4 {
        t.Fatalf("expected 4 models, got %d: %+v", len(resp.Data), resp.Data)
    }
    wantOrder := []string{"z-local-loaded", "z-local-unloaded", "a-cloud-1", "a-cloud-2"}
    for i, want := range wantOrder {
        if resp.Data[i].ID != want {
            t.Fatalf("index %d: expected %q (local-first, loaded-first), got %q. full order: %+v",
                i, want, resp.Data[i].ID, ids(resp.Data))
        }
    }
}

func ids(ms []adapter.ModelInfo) []string {
    out := make([]string, len(ms))
    for i, m := range ms {
        out[i] = m.ID
    }
    return out
}

// TestHandleAnthropicMessages_MultimodalRoutesLocalWithVisionModel verifies
// that a multimodal /v1/messages request (carrying an image content block)
// is forced to the local backend with its model rewritten to the configured
// local vision model, instead of being mapped to the text-only cloud model
// (glm5.2) that rejects images with 400 -> gateway 502. The handler detects
// the image before Decide so the router's text-only signal cannot divert a
// multimodal payload to a text-only cloud backend (issue: CC screenshot 502).
// FusionMLXProvider is NOT a MessagesProvider, so the local path takes the
// AnthropicToOpenAIChatRequest conversion branch (real MLX behavior); the
// mock here uses mockProvider (same non-MessagesProvider shape) and records
// the rewritten model the handler forwards.
func TestHandleAnthropicMessages_MultimodalRoutesLocalWithVisionModel(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit"
    // Register a local fusion-mlx provider that records the forwarded model.
    local := &modelRecordingProvider{mockProvider: mockProvider{
        name:      "fusion-mlx",
        chatResp:  &adapter.ChatResponse{ID: "mm-local", Choices: []adapter.ChatChoice{{Message: adapter.ChatMessage{Role: "assistant", Content: "seen"}}}},
    }}
    s.pool.Register("fusion-mlx", local, config.BackendConfig{Type: "fusion-mlx", Enabled: true})
    s.router.SetLocalReady(true)

    body := `{"model":"claude-fable-5","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleAnthropicMessages(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (local vision served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := local.lastModel.Load(); got != "mlx-community--Qwen2.5-VL-7B-Instruct-4bit" {
        t.Fatalf("expected model rewritten to local VL model, got %q", got)
    }
}

// TestAnthropicRequestHasImage_ThinkingAndToolUseNotMultimodal verifies the
// #113 regression: the multimodal guard must NOT treat thinking/tool_use/
// tool_result content blocks as multimodal. Claude Code in extended-thinking
// mode (CLAUDE_CODE_EFFORT_LEVEL=max) sends assistant history with type:"thinking"
// blocks; the over-broad guard (block.Type != "text" && != "") forced every
// such request to the local vision model -> local slow/timeout -> context
// canceled -> A4 cloud fallback also canceled -> CC "Waiting for API response".
// Only true multimodal types (image/audio/document) force local; a thinking/
// tool-use text conversation must pass through unchanged.
func TestAnthropicRequestHasImage_ThinkingAndToolUseNotMultimodal(t *testing.T) {
    cases := []struct {
        name     string
        messages []adapter.AnthropicMessage
        want     bool
    }{
        {
            name: "thinking block is not multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "assistant", Content: []adapter.AnthropicContentBlock{
                    {Type: "thinking", Thinking: "compute"},
                    {Type: "text", Text: "4"},
                }},
            },
            want: false,
        },
        {
            name: "tool_use block is not multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "assistant", Content: []adapter.AnthropicContentBlock{
                    {Type: "tool_use", ID: "tu1", Name: "ls"},
                }},
                {Role: "user", Content: []adapter.AnthropicContentBlock{
                    {Type: "tool_result", ToolUseID: "tu1", ACContent: "file1"},
                }},
            },
            want: false,
        },
        {
            name: "redacted_thinking is not multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "assistant", Content: []adapter.AnthropicContentBlock{
                    {Type: "redacted_thinking"},
                }},
            },
            want: false,
        },
        {
            name: "plain text is not multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "user", Content: []adapter.AnthropicContentBlock{{Type: "text", Text: "hi"}}},
            },
            want: false,
        },
        {
            name: "image block is multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "user", Content: []adapter.AnthropicContentBlock{
                    {Type: "text", Text: "describe"},
                    {Type: "image", Source: &adapter.AnthropicImageSource{Type: "base64", MediaType: "image/png", Data: "iVBOR"}},
                }},
            },
            want: true,
        },
        {
            name: "audio block is multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "user", Content: []adapter.AnthropicContentBlock{
                    {Type: "audio", Source: &adapter.AnthropicImageSource{Type: "base64", MediaType: "audio/wav", Data: "UklGR"}},
                }},
            },
            want: true,
        },
        {
            name: "document block is multimodal",
            messages: []adapter.AnthropicMessage{
                {Role: "user", Content: []adapter.AnthropicContentBlock{
                    {Type: "document", Source: &adapter.AnthropicImageSource{Type: "base64", MediaType: "application/pdf", Data: "JVBER"}},
                }},
            },
            want: true,
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := anthropicRequestHasImage(tc.messages); got != tc.want {
                t.Fatalf("anthropicRequestHasImage(%s) = %v, want %v", tc.name, got, tc.want)
            }
        })
    }
}

// TestHandleAnthropicMessages_MultimodalRejectsWhenNoLocalModel verifies that
// when an image-bearing request arrives but no routing.multimodal.local_model
// is configured, the gateway rejects with a clear 400 (invalid_request) naming
// the misconfiguration — NOT a cloud 400 (multimodal_not_supported) wrapped to
// 502. A masked 502 leaves the client unable to self-diagnose; an honest 400
// tells the operator exactly which knob to set.
func TestHandleAnthropicMessages_MultimodalRejectsWhenNoLocalModel(t *testing.T) {
    s := newTestServer()
    // No multimodal.local_model configured. Cloud glm5.2 is text-only.
    s.pool.Register("glm52", &mockProvider{name: "glm52"}, config.BackendConfig{Type: "anthropic", Enabled: true})

    body := `{"model":"claude-fable-5","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleAnthropicMessages(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 (clear multimodal rejection), got %d: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "multimodal") {
        t.Fatalf("error body should name multimodal misconfiguration, got: %s", rec.Body.String())
    }
}

// modelRecordingProvider is a non-MessagesProvider (matches FusionMLXProvider
// shape) that atomically records the model id the handler forwards, so the
// multimodal rewrite assertion can read it after the handler returns.
type modelRecordingProvider struct {
    mockProvider
    lastModel atomic.Value
}

func (m *modelRecordingProvider) Chat(_ context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    m.lastModel.Store(req.Model)
    return m.chatResp, m.chatErr
}

func (m *modelRecordingProvider) StreamChat(_ context.Context, req *adapter.ChatRequest) (<-chan adapter.StreamChunk, error) {
    m.lastModel.Store(req.Model)
    return m.streamCh, m.streamErr
}

func TestHandleModels_PerProviderTimeoutSkipsSlow(t *testing.T) {
    s := newTestServer()
    // slow cloud backend blocks until the 3s per-provider timeout fires
    s.pool.Register("cloud-slow", &blockingProvider{
        mockProvider: mockProvider{name: "cloud-slow"},
    }, config.BackendConfig{Type: "openai-compatible", Enabled: true})
    // local backend returns immediately
    s.pool.Register("local-mlx", &mockProvider{
        name:   "local-mlx",
        models: []adapter.ModelInfo{{ID: "qwen3", OwnedBy: "fusion-mlx"}},
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})

    start := time.Now()
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)
    elapsed := time.Since(start)

    var resp struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if len(resp.Data) != 1 || resp.Data[0].ID != "qwen3" {
        t.Fatalf("expected local qwen3 despite slow cloud backend, got %+v", resp.Data)
    }
    // Worst case is the 3s per-provider timeout, far below the 30s+ serial block.
    if elapsed > 8*time.Second {
        t.Fatalf("handleModels blocked too long: %v (per-provider timeout should cap at ~3s)", elapsed)
    }
}
