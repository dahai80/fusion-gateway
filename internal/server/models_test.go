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
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
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

// TestHandleAnthropicMessages_AllowlistGatesModel verifies the F5 fix: a key
// whose allowed_models does not include the requested model must be 403'd at
// /v1/messages. Before the fix this handler skipped CheckModelAllowlist (every
// other /v1/* inference handler gated on it), so a restricted key could reach
// an unauthorized model via the Anthropic endpoint — the path Claude Code uses.
func TestHandleAnthropicMessages_AllowlistGatesModel(t *testing.T) {
    s := newTestServer()
    s.pool.Register("glm52", &mockProvider{name: "glm52"}, config.BackendConfig{Type: "anthropic", Enabled: true})

    body := `{"model":"claude-opus-4-7","max_tokens":32,"stream":false,"messages":[{"role":"user","content":"hi"}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    // key restricted to gpt-4-only must NOT reach claude-opus-4-7
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), &middleware.Principal{
        KeyConfig: &config.AuthKeyConfig{Name: "restricted", AllowedModels: []string{"gpt-4-only"}},
    }))
    rec := httptest.NewRecorder()
    s.handleAnthropicMessages(rec, req)

    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403 (model not in allowlist), got %d: %s", rec.Code, rec.Body.String())
    }
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
    s, srv := newMultimodalMLXServer(t, []string{"mlx-community--Qwen2.5-VL-7B-Instruct-4bit"})
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit"
    s.router.SetLocalReady(true)

    body := `{"model":"claude-fable-5","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleAnthropicMessages(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (local vision served), got %d: %s", rec.Code, rec.Body.String())
    }
    // RC4: the local vision model is actually loaded (RefreshModelSet populated
    // ModelSet from the mocked /v1/models), so the unified guard forces
    // LocalBackend with the model rewritten to the vision model and forwards to
    // fusion-mlx (mocked /v1/chat/completions returns OK). The image-bearing
    // request must NOT leak to a text-only cloud.
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

// TestChatRequestHasImage_BlockTypeDetection verifies #120's chat-side
// multimodal guard: an OpenAI image_url / input_audio content block is
// detected, but a plain-string text message and a text-typed block are not.
// extractTextContent drops non-string Content, so without this guard the
// router's text-only signal is blind to a multimodal /v1/chat/completions.
func TestChatRequestHasImage_BlockTypeDetection(t *testing.T) {
    cases := []struct {
        name string
        req  *adapter.ChatRequest
        want bool
    }{
        {
            name: "plain string text is not multimodal",
            req:  &adapter.ChatRequest{Messages: []adapter.ChatMessage{{Role: "user", Content: "hello"}}},
            want: false,
        },
        {
            name: "text block array is not multimodal",
            req: &adapter.ChatRequest{Messages: []adapter.ChatMessage{
                {Role: "user", Content: []any{map[string]any{"type": "text", "text": "hi"}}},
            }},
            want: false,
        },
        {
            name: "image_url block is multimodal",
            req: &adapter.ChatRequest{Messages: []adapter.ChatMessage{
                {Role: "user", Content: []any{
                    map[string]any{"type": "text", "text": "describe"},
                    map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBOR"}},
                }},
            }},
            want: true,
        },
        {
            name: "input_audio block is multimodal",
            req: &adapter.ChatRequest{Messages: []adapter.ChatMessage{
                {Role: "user", Content: []any{
                    map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "uw=="}},
                }},
            }},
            want: true,
        },
        {
            name: "nil request is not multimodal",
            req:  nil,
            want: false,
        },
    }
    for _, tc := range cases {
        if got := chatRequestHasImage(tc.req); got != tc.want {
            t.Fatalf("chatRequestHasImage(%s) = %v, want %v", tc.name, got, tc.want)
        }
    }
}

// newMultimodalMLXServer builds a test server whose fusion-mlx /v1/models mock
// returns the given model ids (so RefreshModelSet populates ModelSet). The chat
// endpoint returns a fixed OK so the handler can complete the forward.
func newMultimodalMLXServer(t *testing.T, mlxModels []string) (*Server, *httptest.Server) {
    t.Helper()
    modelsData := make([]map[string]string, 0, len(mlxModels))
    for _, id := range mlxModels {
        modelsData = append(modelsData, map[string]string{"id": id, "object": "model", "owned_by": "mlx"})
    }
    modelsBody, _ := json.Marshal(map[string]any{"object": "list", "data": modelsData})
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write(modelsBody)
        case "/v1/chat/completions":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"id":"mlx-ok","choices":[{"message":{"role":"assistant","content":"seen"}}]}`))
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    s := newTestServer()
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: srv.URL,
        Enabled: true,
    }, config.RoutingConfig{})
    s.pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "fusion-mlx", BaseURL: srv.URL, Enabled: true})
    mlx.RefreshModelSet(context.Background())
    return s, srv
}

// TestHandleChatCompletions_MultimodalLocalFirst routes a multimodal chat
// request to the local vision model when it is loaded (LocalModel in ModelSet).
// Verifies the request model is rewritten to the vision model and forwarded
// to the local fusion-mlx provider (not a cloud text-only model).
func TestHandleChatCompletions_MultimodalLocalFirst(t *testing.T) {
    s, srv := newMultimodalMLXServer(t, []string{"mlx-community--Qwen2.5-VL-7B-Instruct-4bit"})
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit"
    s.router.SetLocalReady(true)

    body := `{"model":"qwen-7b","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    // allowlist must permit the forced vision model (handler checks before rewrite)
    req.Header.Set("X-Fusion-Allowed-Models", "*")
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (local vision served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("X-Route-Decision"); !strings.HasPrefix(got, "local:multimodal_local_vision") {
        t.Fatalf("expected X-Route-Decision local:multimodal_local_vision, got %q", got)
    }
}

// TestHandleChatCompletions_MultimodalCloudVLMFallback is the #120 core case:
// the local node has NO vision model loaded (ModelSet empty), but
// routing.multimodal.cloud_backend + cloud_model are configured. A multimodal
// /v1/chat/completions must route to the cloud VLM backend with the request
// model rewritten to the cloud VLM model — NOT a text-only local model that
// would reject the image with 400 -> 502. This is the fusion-browser Visual
// Grounding fallback path.
func TestHandleChatCompletions_MultimodalCloudVLMFallback(t *testing.T) {
    s, srv := newMultimodalMLXServer(t, []string{}) // local has NO vision model
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit" // configured but not loaded
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    s.cfg.Config.Routing.Fallback.Enabled = true
    s.router.SetLocalReady(true)

    // cloud VLM provider records the forwarded model
    cloud := &modelRecordingProvider{mockProvider: mockProvider{
        name:     "openai",
        chatResp: &adapter.ChatResponse{ID: "vlm-cloud", Choices: []adapter.ChatChoice{{Message: adapter.ChatMessage{Role: "assistant", Content: "coords"}}}},
    }}
    s.pool.Register("openai", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    body := `{"model":"qwen-7b","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"click"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    req.Header.Set("X-Fusion-Allowed-Models", "*")
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (cloud VLM served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := cloud.lastModel.Load(); got != "gpt-4o" {
        t.Fatalf("expected model rewritten to cloud VLM gpt-4o, got %v", got)
    }
    if got := rec.Header().Get("X-Route-Decision"); !strings.HasPrefix(got, "cloud:multimodal_cloud_vlm") {
        t.Fatalf("expected X-Route-Decision cloud:multimodal_cloud_vlm, got %q", got)
    }
}

// TestHandleChatCompletions_MultimodalRejectsWhenUnconfigured verifies the #120
// clear-400 path: a multimodal request with no loaded local vision model AND no
// cloud VLM configured is rejected with a 400 naming the missing knobs, not
// forwarded to a text-only model that would mask the failure as 502.
func TestHandleChatCompletions_MultimodalRejectsWhenUnconfigured(t *testing.T) {
    s, srv := newMultimodalMLXServer(t, []string{}) // no vision model loaded
    defer srv.Close()
    // LocalModel configured but not loaded; no cloud_backend/cloud_model
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit"
    s.router.SetLocalReady(true)

    body := `{"model":"qwen-7b","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    req.Header.Set("X-Fusion-Allowed-Models", "*")
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 (unconfigured multimodal), got %d: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "multimodal") {
        t.Fatalf("expected 400 body to name the multimodal knob, got %s", rec.Body.String())
    }
}

// TestRC4_MessagesCloudVLMFallback is the RC4 core case for /v1/messages: the
// local node has NO vision model loaded, but routing.multimodal.cloud_backend +
// cloud_model are configured. The prior guard forced local-only and 400'd when
// local_model was set but not loaded — leaking the image to nothing. The unified
// decision (RC4) now routes to the cloud VLM with the model rewritten to the
// cloud VLM model, mirroring /v1/chat/completions.
func TestRC4_MessagesCloudVLMFallback(t *testing.T) {
    s, srv := newMultimodalMLXServer(t, []string{}) // local has NO vision model
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit" // configured but not loaded
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    s.cfg.Config.Routing.Fallback.Enabled = true
    s.router.SetLocalReady(true)

    cloud := &modelRecordingProvider{mockProvider: mockProvider{
        name:     "openai",
        chatResp: &adapter.ChatResponse{ID: "vlm-cloud", Choices: []adapter.ChatChoice{{Message: adapter.ChatMessage{Role: "assistant", Content: "coords"}}}},
    }}
    s.pool.Register("openai", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    body := `{"model":"claude-fable-5","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleAnthropicMessages(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (cloud VLM served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := cloud.lastModel.Load(); got != "gpt-4o" {
        t.Fatalf("expected model rewritten to cloud VLM gpt-4o, got %v", got)
    }
}

// TestRC4_MessagesUnconfigured400 mirrors the chat path: a /v1/messages image
// request with no loaded local vision model AND no cloud VLM configured is
// rejected with a clear 400 naming the missing knobs (not a masked 502).
func TestRC4_MessagesUnconfigured400(t *testing.T) {
    s, srv := newMultimodalMLXServer(t, []string{}) // no vision model loaded
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit" // configured but not loaded
    s.router.SetLocalReady(true)

    body := `{"model":"claude-fable-5","max_tokens":32,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleAnthropicMessages(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Fatalf("expected 400 (unconfigured multimodal), got %d: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "multimodal") {
        t.Fatalf("expected 400 body to name the multimodal knob, got %s", rec.Body.String())
    }
}

// TestRC4_DecisionSharedLogic verifies the shared multimodalDecisionFor helper
// returns the same decision shape regardless of which handler calls it, and that
// the local-loaded check (not just config presence) gates the local path.
func TestRC4_DecisionSharedLogic(t *testing.T) {
    // local vision model loaded -> LocalBackend
    s1, srv1 := newMultimodalMLXServer(t, []string{"vl-7b"})
    defer srv1.Close()
    s1.cfg.Config.Routing.Multimodal.LocalModel = "vl-7b"
    d1, m1 := s1.multimodalDecisionFor("client-x")
    if d1.Backend != router.LocalBackend || d1.Reason != "multimodal_local_vision" || m1 != "vl-7b" {
        t.Fatalf("local-loaded: expected LocalBackend/multimodal_local_vision/vl-7b, got %s/%s/%q", d1.Backend, d1.Reason, m1)
    }

    // local configured but not loaded, cloud VLM set -> CloudBackend + cloud model
    s2, srv2 := newMultimodalMLXServer(t, []string{}) // nothing loaded
    defer srv2.Close()
    s2.cfg.Config.Routing.Multimodal.LocalModel = "vl-7b"
    s2.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s2.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    d2, m2 := s2.multimodalDecisionFor("client-x")
    if d2.Backend != router.CloudBackend || d2.Reason != "multimodal_cloud_vlm" || d2.CloudTarget != "openai" || m2 != "gpt-4o" {
        t.Fatalf("cloud-fallback: expected CloudBackend/multimodal_cloud_vlm/openai/gpt-4o, got %s/%s/%s/%q", d2.Backend, d2.Reason, d2.CloudTarget, m2)
    }

    // neither loaded nor cloud -> unconfigured sentinel
    s3, srv3 := newMultimodalMLXServer(t, []string{})
    defer srv3.Close()
    s3.cfg.Config.Routing.Multimodal.LocalModel = "vl-7b"
    d3, m3 := s3.multimodalDecisionFor("client-x")
    if !multimodalUnconfigured(d3) || m3 != "" {
        t.Fatalf("unconfigured: expected sentinel + empty model, got %s/%s/%q", d3.Backend, d3.Reason, m3)
    }
}
