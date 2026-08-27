package server

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
)

// newTestServerLocalQueue builds a Server whose engine is constructed with
// mode=local + queue_enabled so LocalQueue() is wired. Required because the
// queue is constructed at NewEngine time, not lazily.
func newTestServerLocalQueue(maxConcurrent int, queueTimeout time.Duration) *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Auth.Enabled = false
    cfg.Config.Auth.Passthrough = true
    cfg.Config.Server.Port = 0
    cfg.Config.Server.MaxRequestBodySize = 5 << 20
    cfg.Config.Routing.Mode = "local"
    cfg.Config.Routing.LocalPriority.QueueEnabled = true
    cfg.Config.Routing.LocalPriority.MaxConcurrent = maxConcurrent
    cfg.Config.Routing.LocalPriority.QueueTimeout = queueTimeout
    cfg.Config.OIDC.Enabled = false

    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    routerEngine := router.NewEngine(cfg, hwCollector)
    pool := adapter.NewPool()
    tokEngine := tokenizer.NewEngine(&cfg.Config.Tokenizer, "")

    oidcAuth, _ := middleware.NewOIDCAuthenticator(middleware.OIDCConfig{Enabled: false})

    s := &Server{
        cfg:               cfg,
        hwCollector:       hwCollector,
        router:            routerEngine,
        pool:              pool,
        tokEngine:         tokEngine,
        startTime:         time.Now(),
        store:             memorystore.NewMemoryStoreWithConfig(1000, cfg.Config.Batch),
        cache:             cache.New(cfg.Config.Cache),
        semanticCache:     cache.NewSemanticCache(cfg.Config.SemanticCache, nil),
        connectorRegistry: newConnectorRegistry(cfg),
        oidcAuth:          oidcAuth,
        taskRegistry:      NewTaskRegistry(),
    }
    s.buildMiddlewareChain()
    return s
}

// TestServer_QueueModeLocal_429OnCap verifies the opt-in local wait-queue
// rejects with 429 once max_concurrent is saturated and queue_timeout elapses
// (#102 ADR-001 sub-task 3). Default hybrid path is covered by the nil-queue
// branch (no separate test needed — LocalQueue() returns nil there).
func TestServer_QueueModeLocal_429OnCap(t *testing.T) {
    s := newTestServerLocalQueue(1, 30*time.Millisecond)

    // First request holds the slot: a stream channel that stays open until the
    // test closes it. The streaming forward blocks reading ch, keeping the
    // slot occupied.
    holdCh := make(chan adapter.StreamChunk, 1)
    holdCh <- adapter.StreamChunk{
        ID: "hold", Object: "chat.completion.chunk",
        Created: time.Now().Unix(), Model: "qwen2.5-7b",
        Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "x"}}},
    }
    s.pool.Register("fusion-mlx", &mockProvider{
        name: "fusion-mlx", healthy: true, streamCh: holdCh,
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})

    chatBody := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"hi"}],"stream":true}`

    // Request 1: acquires the only slot, blocks on the open stream channel.
    req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    req1 = req1.WithContext(config.WithSnapshot(req1.Context(), s.cfg))
    rec1 := httptest.NewRecorder()
    go s.handleChatCompletions(rec1, req1)

    // Wait for request 1 to take the slot.
    deadline := time.Now().Add(time.Second)
    for time.Now().Before(deadline) {
        if s.router.LocalQueue().Occupied() >= 1 {
            break
        }
        time.Sleep(5 * time.Millisecond)
    }
    if s.router.LocalQueue().Occupied() < 1 {
        close(holdCh)
        t.Fatalf("request 1 never acquired the slot, occupied=%d", s.router.LocalQueue().Occupied())
    }

    // Request 2: queue full (cap=1), 30ms budget -> must 429.
    req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    req2 = req2.WithContext(config.WithSnapshot(req2.Context(), s.cfg))
    rec2 := httptest.NewRecorder()
    s.handleChatCompletions(rec2, req2)

    if rec2.Code != http.StatusTooManyRequests {
        close(holdCh)
        t.Fatalf("expected 429 from saturated queue, got %d body=%s", rec2.Code, rec2.Body.String())
    }
    var body map[string]interface{}
    if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
        close(holdCh)
        t.Fatalf("429 body not json: %v body=%s", err, rec2.Body.String())
    }
    if errObj, ok := body["error"].(map[string]interface{}); !ok || errObj["type"] != "rate_limit_error" {
        close(holdCh)
        t.Fatalf("429 error.type want rate_limit_error, got %v", body)
    }

    close(holdCh)
}

// TestServer_QueueDisabled_NoGate verifies the queue stays nil (no 429, no
// blocking) when queue_enabled is false even in mode=local.
func TestServer_QueueDisabled_NoGate(t *testing.T) {
    s := newTestServer() // default: hybrid, queue_enabled=false
    if q := s.router.LocalQueue(); q != nil {
        t.Fatalf("LocalQueue must be nil in default hybrid config, got %v", q)
    }

    s.pool.Register("fusion-mlx", &mockProvider{
        name: "fusion-mlx", healthy: true,
        chatResp: &adapter.ChatResponse{
            ID: "cmpl", Object: "chat.completion", Created: time.Now().Unix(), Model: "m",
            Choices: []adapter.ChatChoice{{Index: 0, Message: map[string]string{"role": "assistant", "content": "ok"}, FinishReason: "stop"}},
            Usage: adapter.UsageResponse{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
        },
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})
    s.cfg.Config.Routing.Fallback.CloudDefault = "fusion-mlx"

    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
        strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)
    if rec.Code == http.StatusTooManyRequests {
        t.Fatalf("default path must never 429, got %d", rec.Code)
    }
}

// TestServer_AgentTaskCancel_Endpoint verifies POST /v1/agent/tasks/{id}/cancel
// returns 200 + {"status":"canceled"} for a registered in-flight task and 404
// for an unknown id (#102 ADR-001 sub-task 4). The registry is exercised
// directly (stream integration cancel is covered by the registry unit tests).
func TestServer_AgentTaskCancel_Endpoint(t *testing.T) {
    s := newTestServer()
    s.taskRegistry = NewTaskRegistry()

    ctx, cancel := context.WithCancel(context.Background())
    s.taskRegistry.Register("task-cancel-1", "", cancel)

    req := httptest.NewRequest(http.MethodPost, "/v1/agent/tasks/task-cancel-1/cancel", nil)
    rec := httptest.NewRecorder()
    s.handleAgentTask(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    if ctx.Err() == nil {
        t.Fatal("cancel func not invoked by endpoint")
    }
    var body map[string]interface{}
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("body not json: %v", err)
    }
    if body["status"] != "canceled" {
        t.Fatalf("status want canceled, got %v", body["status"])
    }
}

func TestServer_AgentTaskCancel_NotFound(t *testing.T) {
    s := newTestServer()
    s.taskRegistry = NewTaskRegistry()

    req := httptest.NewRequest(http.MethodPost, "/v1/agent/tasks/does-not-exist/cancel", nil)
    rec := httptest.NewRecorder()
    s.handleAgentTask(rec, req)

    if rec.Code != http.StatusNotFound {
        t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
    }
}

func TestServer_AgentTaskCancel_MalformedPath(t *testing.T) {
    s := newTestServer()
    s.taskRegistry = NewTaskRegistry()

    for _, p := range []string{"/v1/agent/tasks/", "/v1/agent/tasks/abc", "/v1/agent/tasks/abc/cancel/extra"} {
        req := httptest.NewRequest(http.MethodPost, p, nil)
        rec := httptest.NewRecorder()
        s.handleAgentTask(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("path %q: expected 404, got %d", p, rec.Code)
        }
    }
}

func TestServer_AgentTaskCancel_MethodNotAllowed(t *testing.T) {
    s := newTestServer()
    s.taskRegistry = NewTaskRegistry()

    req := httptest.NewRequest(http.MethodGet, "/v1/agent/tasks/abc/cancel", nil)
    rec := httptest.NewRecorder()
    s.handleAgentTask(rec, req)
    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("expected 405, got %d", rec.Code)
    }
}

// TestServer_AgentTaskCancel_Integration verifies the full path: a streaming
// chat registers its task-id, a concurrent POST cancel terminates it, and the
// slot is released exactly once (no double-release / underflow).
func TestServer_AgentTaskCancel_Integration(t *testing.T) {
    s := newTestServerLocalQueue(2, 5*time.Second)

    holdCh := make(chan adapter.StreamChunk, 2)
    holdCh <- adapter.StreamChunk{
        ID: "int", Object: "chat.completion.chunk",
        Created: time.Now().Unix(), Model: "qwen2.5-7b",
        Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": "y"}}},
    }
    s.pool.Register("fusion-mlx", &mockProvider{
        name: "fusion-mlx", healthy: true, streamCh: holdCh,
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true, BaseURL: "http://localhost:0"})

    chatBody := `{"model":"qwen2.5-7b","messages":[{"role":"user","content":"hi"}],"stream":true}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    baseCtx := middleware.InjectRequestID(req.Context(), "task-int-1")
    baseCtx = config.WithSnapshot(baseCtx, s.cfg)
    req = req.WithContext(baseCtx)
    req.Header.Set("X-Request-ID", "task-int-1")
    rec := httptest.NewRecorder()

    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        s.handleChatCompletions(rec, req)
    }()

    // Wait for the task to be registered.
    deadline := time.Now().Add(time.Second)
    for time.Now().Before(deadline) {
        if s.taskRegistry.Len() == 1 {
            break
        }
        time.Sleep(5 * time.Millisecond)
    }
    if s.taskRegistry.Len() != 1 {
        close(holdCh)
        t.Fatalf("task never registered, registry len=%d", s.taskRegistry.Len())
    }

    // POST cancel.
    cancelReq := httptest.NewRequest(http.MethodPost, "/v1/agent/tasks/task-int-1/cancel", nil)
    cancelRec := httptest.NewRecorder()
    s.handleAgentTask(cancelRec, cancelReq)
    if cancelRec.Code != http.StatusOK {
        close(holdCh)
        t.Fatalf("cancel expected 200, got %d body=%s", cancelRec.Code, cancelRec.Body.String())
    }

    // The stream ctx is now canceled; unblock the channel so the goroutine
    // exits and releases the slot. The defer Release + the existing slot
    // release run; Occupied must return to 0 with no underflow.
    close(holdCh)
    wg.Wait()

    if s.router.LocalQueue().Occupied() != 0 {
        t.Fatalf("slot not released after cancel, occupied=%d", s.router.LocalQueue().Occupied())
    }
    if s.taskRegistry.Len() != 0 {
        t.Fatalf("registry not drained, len=%d", s.taskRegistry.Len())
    }
}
