package server

// multimodal_divert_test.go — #389: a multimodal request whose decision is
// local carries a local VLM model id after the entry guard rewrites it. Every
// downstream divert of that decision (overflow cooldown pre-divert, RR4
// slot-full, stream / non-stream cloud overflow) previously sent the VLM id to
// the text-only cloud_default (glm-5) when it diverted — which rejects the VLM
// id with 400 -> gateway 502 — intermittently, because an unrelated local
// failure arms the 2m overflow cooldown and pre-diverts the NEXT multimodal
// request straight to cloud. Each divert must target a configured cloud VLM or
// be refused (fail-closed).

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
)

const mmDivertVLModel = "mlx-community--Qwen2.5-VL-7B-Instruct-4bit"

const mmDivertImageBody = `{"model":"qwen-7b","max_tokens":64,"stream":%s,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`

// divertCountingCloud records every cloud Chat/StreamChat invocation with
// the model id the handler forwarded; lastModel.Load() == nil proves cloud was
// never called.
type divertCountingCloud struct {
    mockProvider
    hits      atomic.Int64
    lastModel atomic.Value
}

func (c *divertCountingCloud) Chat(_ context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    c.hits.Add(1)
    c.lastModel.Store(req.Model)
    if c.chatErr != nil {
        return nil, c.chatErr
    }
    return c.chatResp, nil
}

func (c *divertCountingCloud) StreamChat(_ context.Context, req *adapter.ChatRequest) (<-chan adapter.StreamChunk, error) {
    c.hits.Add(1)
    c.lastModel.Store(req.Model)
    if c.streamErr != nil {
        return nil, c.streamErr
    }
    return c.streamCh, nil
}

func newDivertCountingCloud(name string) *divertCountingCloud {
    return &divertCountingCloud{mockProvider: mockProvider{
        name:    name,
        healthy: true,
        chatResp: &adapter.ChatResponse{ID: "cloud-ok", Choices: []adapter.ChatChoice{{Message: adapter.ChatMessage{Role: "assistant", Content: "from-cloud"}}}},
        streamCh: func() chan adapter.StreamChunk {
            ch := make(chan adapter.StreamChunk, 4)
            ch <- adapter.StreamChunk{ID: "cloud-ok", Object: "chat.completion.chunk", Choices: []adapter.ChoiceDelta{{Delta: adapter.ChatMessage{Role: "assistant", Content: "from-cloud"}}}}
            close(ch)
            return ch
        }(),
    }}
}

// newDivertMLXServer builds a fusion-mlx mock whose chat endpoint fails with
// chatStatus (500 = local engine failure, exercising the overflow divert
// sites) and whose /v1/models registers the given ids (RefreshModelSet). The
// slot-full sites are exercised with a dedicated blocking server per test.
func newDivertMLXServer(t *testing.T, mlxModels []string, chatStatus int) (*Server, *httptest.Server) {
    t.Helper()
    modelsData := make([]map[string]string, 0, len(mlxModels))
    for _, id := range mlxModels {
        modelsData = append(modelsData, map[string]string{"id": id, "object": "model", "owned_by": "mlx"})
    }
    modelsBody, _ := json.Marshal(map[string]any{"object": "list", "data": modelsData})
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/health":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"status":"healthy","ready":true,"model_loaded":true}`))
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write(modelsBody)
        case "/v1/chat/completions":
            if chatStatus != http.StatusOK {
                w.WriteHeader(chatStatus)
                _, _ = w.Write([]byte(`{"error":{"message":"engine boom"}}`))
                return
            }
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
    }, config.RoutingConfig{
        LocalPriority: config.LocalPriorityConfig{MaxConcurrent: 1},
    })
    s.pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "fusion-mlx", BaseURL: srv.URL, Enabled: true})
    mlx.RefreshModelSet(context.Background())
    s.router.SetLocalInFlight(mlx.InFlight)
    s.router.SetLocalModels(mlx.ModelSet)
    s.router.SetLocalReady(true)
    return s, srv
}

// mustChatReq parses a chat JSON body into an *http.Request bound to the
// config snapshot context, with the allowlist header the handler expects.
func mustChatReq(t *testing.T, body string, snap *config.ConfigSnapshot) *http.Request {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = req.WithContext(config.WithSnapshot(req.Context(), snap))
    req.Header.Set("X-Fusion-Allowed-Models", "*")
    return req
}

// TestMultimodalDivertDecision_Helper pins the three-state helper contract.
func TestMultimodalDivertDecision_Helper(t *testing.T) {
    s := newTestServer()

    // text-only request: divert rules unchanged (nil, true)
    textReq := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}}
    d, ok := s.multimodalDivertDecision(textReq)
    if !ok || d != nil {
        t.Fatalf("text request: want (nil,true), got (%v,%v)", d, ok)
    }

    // image request + no cloud VLM config: divert forbidden (nil, false)
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    imgReq := &adapter.ChatRequest{Model: mmDivertVLModel, Messages: []adapter.ChatMessage{{Role: "user", Content: []any{
        map[string]any{"type": "text", "text": "describe"},
        map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBOR"}},
    }}}}
    d, ok = s.multimodalDivertDecision(imgReq)
    if ok || d != nil {
        t.Fatalf("image no-VLM: want (nil,false), got (%v,%v)", d, ok)
    }

    // image request + cloud VLM config: divert decision + model rewrite
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    d, ok = s.multimodalDivertDecision(imgReq)
    if !ok || d == nil || d.Backend != router.CloudBackend || d.Reason != "multimodal_cloud_vlm_divert" || d.CloudTarget != "openai" {
        t.Fatalf("image VLM: want cloud multimodal_cloud_vlm_divert/openai, got %v/%v", d, ok)
    }
    if imgReq.Model != "gpt-4o" {
        t.Fatalf("image VLM: request model must rewrite to gpt-4o, got %q", imgReq.Model)
    }
}

// Test389_CooldownKeepsMultimodalLocal — divert site 1 (#301 cooldown
// pre-divert): an unrelated local failure armed the overflow cooldown; a
// multimodal request must NOT be pre-diverted to the text-only cloud_default.
// With no cloud VLM configured the cooldown is an optimization the multimodal
// path cannot use — stay local.
func Test389_CooldownKeepsMultimodalLocal(t *testing.T) {
    s, srv := newDivertMLXServer(t, []string{mmDivertVLModel}, http.StatusOK)
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = true
    s.cfg.Config.Routing.Fallback.Overflow.Cooldown = 2 * time.Minute
    s.overflow.armCooldown(time.Now(), 2*time.Minute)
    cloud := newDivertCountingCloud("glm-5")
    s.pool.Register("glm-5", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "false"), s.cfg))

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (stay local), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("X-Route-Decision"); !strings.HasPrefix(got, "local:multimodal_local_vision") {
        t.Fatalf("expected local vision decision, got %q", got)
    }
    if cloud.hits.Load() != 0 {
        t.Fatalf("cloud must not be called for a multimodal request without a configured cloud VLM")
    }
}

// Test389_CooldownDivertsMultimodalToCloudVLM — divert site 1 with a cloud
// VLM configured: the cooldown pre-divert may proceed, but the target must be
// the configured vision-capable backend + model, not the text-only default.
func Test389_CooldownDivertsMultimodalToCloudVLM(t *testing.T) {
    s, srv := newDivertMLXServer(t, []string{mmDivertVLModel}, http.StatusOK)
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = true
    s.cfg.Config.Routing.Fallback.Overflow.Cooldown = 2 * time.Minute
    s.overflow.armCooldown(time.Now(), 2*time.Minute)
    cloud := newDivertCountingCloud("openai")
    s.pool.Register("openai", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "false"), s.cfg))

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (cloud VLM served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := rec.Header().Get("X-Route-Decision"); !strings.HasPrefix(got, "cloud:multimodal_cloud_vlm_divert") {
        t.Fatalf("expected cloud VLM divert decision, got %q", got)
    }
    if got := cloud.lastModel.Load(); got != "gpt-4o" {
        t.Fatalf("cloud VLM must receive rewritten model gpt-4o, got %v", got)
    }
}

// Test389_StreamSlotFullNoCloudVLM503 — divert site 2 (RR4 slot-full, stream):
// the local hard cap is reached on a multimodal stream request with no cloud
// VLM configured. Diverting would send the VLM id to the text-only
// cloud_default (#389) — refuse with a clear 503; cloud must not be called.
func Test389_StreamSlotFullNoCloudVLM503(t *testing.T) {
    releaseCh := make(chan struct{})
    startedCh := make(chan struct{})
    var localHits atomic.Int64
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/health":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"status":"healthy","ready":true,"model_loaded":true}`))
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen-7b","object":"model","owned_by":"mlx"},{"id":"` + mmDivertVLModel + `","object":"model","owned_by":"mlx"}]}`))
        case "/v1/chat/completions":
            localHits.Add(1)
            close(startedCh)
            select {
            case <-releaseCh:
            case <-r.Context().Done():
                return
            }
            w.Header().Set("Content-Type", "text/event-stream")
            _, _ = w.Write([]byte("data: {\"id\":\"s1\",\"choices\":[{\"delta\":{\"content\":\"local\"}}]}\n\ndata: [DONE]\n\n"))
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    defer srv.Close()

    s := newTestServer()
    s.cfg.Config.Routing.Mode = "local"
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: srv.URL,
        Enabled: true,
    }, config.RoutingConfig{
        LocalPriority: config.LocalPriorityConfig{MaxConcurrent: 1},
    })
    s.pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "fusion-mlx", BaseURL: srv.URL, Enabled: true})
    mlx.RefreshModelSet(context.Background())
    s.router.SetLocalInFlight(mlx.InFlight)
    s.router.SetLocalModels(mlx.ModelSet)
    s.router.SetLocalReady(true)
    cloud := newDivertCountingCloud("glm-5")
    s.pool.Register("glm-5", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    // Request 1: text-only stream holds the single local slot.
    rec1 := httptest.NewRecorder()
    go s.handleChatCompletions(rec1, mustChatReq(t, `{"model":"qwen-7b","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, s.cfg))
    select {
    case <-startedCh:
    case <-time.After(2 * time.Second):
        t.Fatalf("timed out waiting for local backend to hold the stream slot")
    }

    // Request 2: multimodal stream, local full, no cloud VLM -> 503.
    rec2 := httptest.NewRecorder()
    s.handleChatCompletions(rec2, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "true"), s.cfg))

    if rec2.Code != http.StatusServiceUnavailable {
        t.Fatalf("expected 503 (no cloud VLM for multimodal slot-full divert), got %d: %s", rec2.Code, rec2.Body.String())
    }
    if !strings.Contains(rec2.Body.String(), "multimodal") {
        t.Fatalf("expected 503 body to name the multimodal knob, got %s", rec2.Body.String())
    }
    if cloud.hits.Load() != 0 {
        t.Fatalf("cloud must not be called (no cloud VLM configured)")
    }
    if localHits.Load() != 1 {
        t.Fatalf("local should hold exactly the slot-holder request, got %d", localHits.Load())
    }
    close(releaseCh)
}

// Test389_StreamSlotFullDivertsToCloudVLM — divert site 2 with a cloud VLM
// configured: the RR4 divert may proceed, retargeted to the configured
// vision-capable backend + model.
func Test389_StreamSlotFullDivertsToCloudVLM(t *testing.T) {
    releaseCh := make(chan struct{})
    startedCh := make(chan struct{})
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/health":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"status":"healthy","ready":true,"model_loaded":true}`))
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen-7b","object":"model","owned_by":"mlx"},{"id":"` + mmDivertVLModel + `","object":"model","owned_by":"mlx"}]}`))
        case "/v1/chat/completions":
            close(startedCh)
            select {
            case <-releaseCh:
            case <-r.Context().Done():
                return
            }
            w.Header().Set("Content-Type", "text/event-stream")
            _, _ = w.Write([]byte("data: {\"id\":\"s1\",\"choices\":[{\"delta\":{\"content\":\"local\"}}]}\n\ndata: [DONE]\n\n"))
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    defer srv.Close()

    s := newTestServer()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: srv.URL,
        Enabled: true,
    }, config.RoutingConfig{
        LocalPriority: config.LocalPriorityConfig{MaxConcurrent: 1},
    })
    s.pool.Register("fusion-mlx", mlx, config.BackendConfig{Type: "fusion-mlx", BaseURL: srv.URL, Enabled: true})
    mlx.RefreshModelSet(context.Background())
    s.router.SetLocalInFlight(mlx.InFlight)
    s.router.SetLocalModels(mlx.ModelSet)
    s.router.SetLocalReady(true)
    cloud := newDivertCountingCloud("openai")
    s.pool.Register("openai", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec1 := httptest.NewRecorder()
    go s.handleChatCompletions(rec1, mustChatReq(t, `{"model":"qwen-7b","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, s.cfg))
    select {
    case <-startedCh:
    case <-time.After(2 * time.Second):
        t.Fatalf("timed out waiting for local backend to hold the stream slot")
    }

    rec2 := httptest.NewRecorder()
    s.handleChatCompletions(rec2, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "true"), s.cfg))

    if rec2.Code != http.StatusOK {
        t.Fatalf("expected 200 (cloud VLM stream served), got %d: %s", rec2.Code, rec2.Body.String())
    }
    if got := cloud.lastModel.Load(); got != "gpt-4o" {
        t.Fatalf("cloud VLM must receive gpt-4o, got %v", got)
    }
    close(releaseCh)
}

// Test389_StreamOverflowNoCloudVLMFailClosed — divert site 3 (stream
// establish-error overflow): the local engine fails the stream establish for a
// multimodal request. Overflow is enabled but no cloud VLM is configured —
// diverting would send the VLM id to the text-only cloud_default (#389), so
// the local error surfaces instead (fail-closed); cloud must not be called.
func Test389_StreamOverflowNoCloudVLMFailClosed(t *testing.T) {
    s, srv := newDivertMLXServer(t, []string{mmDivertVLModel}, http.StatusInternalServerError)
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = true
    s.cfg.Config.Routing.Fallback.Overflow.CostCeiling = 100
    cloud := newDivertCountingCloud("glm-5")
    s.pool.Register("glm-5", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "true"), s.cfg))

    if rec.Code != http.StatusBadGateway {
        t.Fatalf("expected 502 (local error surfaced, fail-closed), got %d: %s", rec.Code, rec.Body.String())
    }
    if cloud.hits.Load() != 0 {
        t.Fatalf("cloud must not be called (no cloud VLM configured)")
    }
}

// Test389_StreamOverflowDivertsToCloudVLM — divert site 3 with a cloud VLM
// configured: the stream overflow may proceed, retargeted to the configured
// vision-capable backend + model.
func Test389_StreamOverflowDivertsToCloudVLM(t *testing.T) {
    s, srv := newDivertMLXServer(t, []string{mmDivertVLModel}, http.StatusInternalServerError)
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = true
    s.cfg.Config.Routing.Fallback.Overflow.CostCeiling = 100
    cloud := newDivertCountingCloud("openai")
    s.pool.Register("openai", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "true"), s.cfg))

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (cloud VLM stream served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := cloud.lastModel.Load(); got != "gpt-4o" {
        t.Fatalf("cloud VLM must receive gpt-4o, got %v", got)
    }
}

// Test389_NonStreamOverflowNoCloudVLMFailClosed — divert site 4 (non-stream
// A4 fallback): local Chat fails for a multimodal request, overflow enabled,
// no cloud VLM configured — the local error surfaces; cloud must not be called.
func Test389_NonStreamOverflowNoCloudVLMFailClosed(t *testing.T) {
    s, srv := newDivertMLXServer(t, []string{mmDivertVLModel}, http.StatusInternalServerError)
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = true
    s.cfg.Config.Routing.Fallback.Overflow.CostCeiling = 100
    cloud := newDivertCountingCloud("glm-5")
    s.pool.Register("glm-5", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "false"), s.cfg))

    if rec.Code != http.StatusBadGateway {
        t.Fatalf("expected 502 (local error surfaced, fail-closed), got %d: %s", rec.Code, rec.Body.String())
    }
    if cloud.hits.Load() != 0 {
        t.Fatalf("cloud must not be called (no cloud VLM configured)")
    }
}

// Test389_NonStreamOverflowDivertsToCloudVLM — divert site 4 with a cloud VLM
// configured: the A4 fallback may proceed, retargeted at the configured
// vision-capable backend + model.
func Test389_NonStreamOverflowDivertsToCloudVLM(t *testing.T) {
    s, srv := newDivertMLXServer(t, []string{mmDivertVLModel}, http.StatusInternalServerError)
    defer srv.Close()
    s.cfg.Config.Routing.Multimodal.LocalModel = mmDivertVLModel
    s.cfg.Config.Routing.Multimodal.CloudBackend = "openai"
    s.cfg.Config.Routing.Multimodal.CloudModel = "gpt-4o"
    s.cfg.Config.Routing.Fallback.CloudDefault = "glm-5"
    s.cfg.Config.Routing.Fallback.Overflow.Enabled = true
    s.cfg.Config.Routing.Fallback.Overflow.CostCeiling = 100
    cloud := newDivertCountingCloud("openai")
    s.pool.Register("openai", cloud, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, mustChatReq(t, fmt.Sprintf(mmDivertImageBody, "false"), s.cfg))

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 (cloud VLM served), got %d: %s", rec.Code, rec.Body.String())
    }
    if got := cloud.lastModel.Load(); got != "gpt-4o" {
        t.Fatalf("cloud VLM must receive gpt-4o, got %v", got)
    }
}
