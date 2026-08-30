package server

import (
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

type ctxAwareStreamProvider struct {
    name    string
    chunks  []adapter.StreamChunk
    started chan struct{}
    closed  chan struct{}
    mu      sync.Mutex
    sawCtx  context.Context
}

func (p *ctxAwareStreamProvider) Name() string { return p.name }
func (p *ctxAwareStreamProvider) HealthCheck(_ context.Context) error { return nil }
func (p *ctxAwareStreamProvider) Chat(_ context.Context, _ *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    return nil, fmt.Errorf("not implemented")
}
func (p *ctxAwareStreamProvider) StreamChat(ctx context.Context, _ *adapter.ChatRequest) (<-chan adapter.StreamChunk, error) {
    p.mu.Lock()
    p.sawCtx = ctx
    p.mu.Unlock()
    if p.started != nil {
        close(p.started)
    }
    ch := make(chan adapter.StreamChunk, len(p.chunks)+1)
    safego.Go("test_stream_pump", func() {
        defer func() {
            close(ch)
            if p.closed != nil {
                close(p.closed)
            }
        }()
        for _, c := range p.chunks {
            select {
            case <-ctx.Done():
                return
            case ch <- c:
            }
        }
    })
    return ch, nil
}
func (p *ctxAwareStreamProvider) Embedding(_ context.Context, _ *adapter.EmbeddingRequest) (*adapter.EmbeddingResponse, error) {
    return nil, fmt.Errorf("not implemented")
}
func (p *ctxAwareStreamProvider) Rerank(_ context.Context, _ *adapter.RerankRequest) (*adapter.RerankResponse, error) {
    return nil, fmt.Errorf("not implemented")
}
func (p *ctxAwareStreamProvider) ListModels(_ context.Context) ([]adapter.ModelInfo, error) {
    return nil, nil
}

func newResumableTestServer(t *testing.T) *Server {
    t.Helper()
    s := newTestServer()
    s.streamBuffers = NewStreamBufferStore(256, 1<<20, 0, 10*time.Minute)
    s.cfg.Config.Routing.Stream.ResumeEnabled = true
    return s
}

func chunksFor(n int) []adapter.StreamChunk {
    out := make([]adapter.StreamChunk, 0, n)
    for i := 0; i < n; i++ {
        out = append(out, adapter.StreamChunk{
            ID:      "chatcmpl-test",
            Object:  "chat.completion.chunk",
            Created: 1700000000,
            Model:   "qwen-7b",
            Choices: []adapter.ChoiceDelta{{Index: 0, Delta: map[string]string{"content": fmt.Sprintf("tok%d ", i)}}},
        })
    }
    return out
}

// TestResumableStream_EmitsIDLines: local resumable stream writes
// "id: <sid>:<seq>" lines + X-Fusion-Stream-ID header.
// Guard: revert Append-frame write or header → no id: lines / header.
func TestResumableStream_EmitsIDLines(t *testing.T) {
    s := newResumableTestServer(t)
    sid := "req-id-emit-test"
    provider := &ctxAwareStreamProvider{name: "fusion-mlx", chunks: chunksFor(3)}
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true})

    ctx := middleware.InjectRequestID(context.Background(), sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    s.handleStreamChat(ctx, rec, provider, req, decision, budget, time.Now())

    body := rec.Body.String()
    if got := rec.Header().Get("X-Fusion-Stream-ID"); got != sid {
        t.Fatalf("X-Fusion-Stream-ID header = %q, want %q", got, sid)
    }
    if !strings.Contains(body, "id: "+sid+":") {
        t.Fatalf("body missing id: <sid>:<seq> lines, got:\n%s", body)
    }
    if !strings.Contains(body, "[DONE]") {
        t.Fatalf("body missing terminal [DONE], got:\n%s", body)
    }
}

// TestStreamBuffer_AppendFramesAfterCursor: FramesAfter returns only frames
// after the cursor seq. Guard: monotonic seq + filter both required.
func TestStreamBuffer_AppendFramesAfterCursor(t *testing.T) {
    store := NewStreamBufferStore(256, 1<<20, 0, 10*time.Minute)
    buf := store.Open("sid-cursor")
    if buf == nil {
        t.Fatal("Open returned nil")
    }
    var seqs []int
    for i := 0; i < 5; i++ {
        seq, _ := buf.Append([]byte(fmt.Sprintf(`{"i":%d}`, i)))
        seqs = append(seqs, seq)
    }
    if len(seqs) != 5 || seqs[0] != 1 || seqs[4] != 5 {
        t.Fatalf("seqs not monotonic 1..5: %v", seqs)
    }
    after := buf.FramesAfter(2)
    if len(after) != 3 {
        t.Fatalf("FramesAfter(2) = %d frames, want 3", len(after))
    }
    for i, f := range after {
        if f.seq != 3+i {
            t.Errorf("after[%d].seq = %d, want %d", i, f.seq, 3+i)
        }
        if !strings.Contains(string(f.frame), fmt.Sprintf("sid-cursor:%d", f.seq)) {
            t.Errorf("after[%d] frame missing id line for seq %d", i, f.seq)
        }
    }
}

// TestResumableStream_ClientDisconnectKeepsBufferGrowing: client disconnect
// must NOT close the pump; buffer keeps growing until natural completion.
// Guard: if pump coupled to client ctx, channel closes early + buffer short.
func TestResumableStream_ClientDisconnectKeepsBufferGrowing(t *testing.T) {
    s := newResumableTestServer(t)
    sid := "req-id-disc-test"
    closed := make(chan struct{})
    started := make(chan struct{})
    provider := &ctxAwareStreamProvider{name: "fusion-mlx", chunks: chunksFor(20), closed: closed, started: started}
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true})

    // Client ctx cancels immediately after the pump starts — simulates a
    // disconnect. The decoupled liveCtx must keep the pump alive.
    clientCtx, clientCancel := context.WithCancel(context.Background())
    clientCtx = middleware.InjectRequestID(clientCtx, sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    // run the stream in a goroutine; cancel the client ctx once the pump starts
    done := make(chan struct{})
    safego.Go("test_resumable_call", func() {
        s.handleStreamChat(clientCtx, rec, provider, req, decision, budget, time.Now())
        close(done)
    })
    <-started
    clientCancel()

    select {
    case <-done:
    case <-time.After(10 * time.Second):
        t.Fatal("handleStreamChat did not return within 10s after client cancel")
    }
    // The pump closed (natural completion) AFTER the client disconnect: the
    // decoupled liveCtx was not canceled by clientCancel.
    select {
    case <-closed:
    case <-time.After(10 * time.Second):
        t.Fatal("pump channel never closed — liveCtx leaked or pump stalled")
    }
    // Buffer holds all 20 chunks + [DONE] = 21 frames (usage chunk absent:
    // includeUsage false). A client-coupled pump would finalize early with <20.
    buf := s.streamBuffers.Get(sid)
    if buf == nil {
        t.Fatal("buffer evicted before assertion — TTL/reap should not run in-test")
    }
    frames := buf.FramesAfter(0)
    if len(frames) < 20 {
        t.Fatalf("buffer has %d frames after disconnect, want >=20 (pump kept draining)", len(frames))
    }
    if !buf.IsFinalized() {
        t.Fatal("buffer not finalized after pump closed")
    }
}

// TestResumableStream_CloudPathUnaffected: cloud path (streamBuffers nil OR
// CloudBackend) falls through to plain handleStreamChat — no id: lines.
// Guard: if dispatch fired for CloudBackend, id: lines appear in body.
func TestResumableStream_CloudPathUnaffected(t *testing.T) {
    s := newResumableTestServer(t)
    provider := &ctxAwareStreamProvider{name: "test-cloud", chunks: chunksFor(3)}
    s.pool.Register("test-cloud", provider, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    ctx := middleware.InjectRequestID(context.Background(), "req-cloud-test")
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "gpt-4", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    s.handleStreamChat(ctx, rec, provider, req, decision, budget, time.Now())

    body := rec.Body.String()
    if strings.Contains(body, "id: req-cloud-test:") {
        t.Fatalf("cloud path emitted resumable id: lines (should fall through to plain):\n%s", body)
    }
    if got := rec.Header().Get("X-Fusion-Stream-ID"); got != "" {
        t.Fatalf("cloud path set X-Fusion-Stream-ID=%q (plain path must not)", got)
    }
}

// TestStreamResume_ReplayAfterCursor: GET /v1/messages/{sid}/events with
// Last-Event-ID replays buffered frames after the cursor, then ends at finalize.
// Guard: if endpoint ignored cursor, it replays from start (more frames).
func TestStreamResume_ReplayAfterCursor(t *testing.T) {
    s := newResumableTestServer(t)
    sid := "req-replay-test"
    buf := s.streamBuffers.Open(sid)
    for i := 0; i < 5; i++ {
        buf.Append([]byte(fmt.Sprintf(`{"i":%d}`, i)))
    }
    buf.MarkFinalized()

    // Cursor "req-replay-test:2" → replay frames 3,4,5 only.
    req := httptest.NewRequest(http.MethodGet, "/v1/messages/"+sid+"/events", nil)
    req.Header.Set("Last-Event-ID", sid+":2")
    rec := httptest.NewRecorder()
    s.handleStreamResume(rec, req)

    body := rec.Body.String()
    gotSeqs := strings.Count(body, "id: "+sid+":")
    if gotSeqs != 3 {
        t.Fatalf("replay emitted %d frames, want 3 (after cursor seq=2). body:\n%s", gotSeqs, body)
    }
    if strings.Contains(body, "id: "+sid+":1\n") || strings.Contains(body, "id: "+sid+":2\n") {
        t.Fatalf("replay included frames at/behind cursor. body:\n%s", body)
    }
}

// TestStreamResume_404WhenDisabled: resume endpoint 404s when streamBuffers nil.
func TestStreamResume_404WhenDisabled(t *testing.T) {
    s := newTestServer()
    if s.streamBuffers != nil {
        t.Fatal("newTestServer unexpectedly set streamBuffers")
    }
    req := httptest.NewRequest(http.MethodGet, "/v1/messages/whatever/events", nil)
    rec := httptest.NewRecorder()
    s.handleStreamResume(rec, req)
    if rec.Code != http.StatusNotFound {
        t.Fatalf("expected 404 when resume disabled, got %d", rec.Code)
    }
}

// TestStreamResume_NeutralRoute: GET /v1/stream/{sid}/events (the #139
// namespace-neutral route an OpenAI-wire client uses) replays the same buffer
// as /v1/messages/{sid}/events. Guard: if the parser only accepted the
// /v1/messages/ prefix, the neutral path 404s and replay is unreachable.
func TestStreamResume_NeutralRoute(t *testing.T) {
    s := newResumableTestServer(t)
    sid := "req-neutral-route"
    buf := s.streamBuffers.Open(sid)
    for i := 0; i < 4; i++ {
        buf.Append([]byte(fmt.Sprintf(`{"i":%d}`, i)))
    }
    buf.MarkFinalized()

    req := httptest.NewRequest(http.MethodGet, "/v1/stream/"+sid+"/events", nil)
    req.Header.Set("Last-Event-ID", sid+":2")
    rec := httptest.NewRecorder()
    s.handleStreamResume(rec, req)

    body := rec.Body.String()
    gotSeqs := strings.Count(body, "id: "+sid+":")
    if gotSeqs != 2 {
        t.Fatalf("neutral route replayed %d frames, want 2 (after cursor seq=2). body:\n%s", gotSeqs, body)
    }
    if got := rec.Header().Get("X-Fusion-Stream-Resume-URL"); got != "/v1/stream/"+sid+"/events" {
        t.Fatalf("X-Fusion-Stream-Resume-URL = %q, want /v1/stream/%s/events", got, sid)
    }
}

// TestResumableStream_EmitsResumeURLHeader: the live stream response carries
// the self-describing X-Fusion-Stream-Resume-URL header so a client discovers
// the replay path without hardcoding a namespace (#139). Guard: if the header
// is absent, an OpenAI-wire client cannot know where to reconnect.
func TestResumableStream_EmitsResumeURLHeader(t *testing.T) {
    s := newResumableTestServer(t)
    sid := "req-resume-url-header"
    provider := &ctxAwareStreamProvider{name: "fusion-mlx", chunks: chunksFor(2)}
    s.pool.Register("fusion-mlx", provider, config.BackendConfig{Type: "fusion-mlx", Enabled: true})

    ctx := middleware.InjectRequestID(context.Background(), sid)
    decision := &router.RouteDecision{Backend: router.LocalBackend, Reason: "test"}
    budget := tokenizer.TokenBudget{InputTokens: 5, TotalBudget: 20}
    req := &adapter.ChatRequest{Model: "qwen-7b", Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}}, Stream: true}

    rec := httptest.NewRecorder()
    s.handleStreamChat(ctx, rec, provider, req, decision, budget, time.Now())

    want := "/v1/stream/" + sid + "/events"
    if got := rec.Header().Get("X-Fusion-Stream-Resume-URL"); got != want {
        t.Fatalf("X-Fusion-Stream-Resume-URL = %q, want %q", got, want)
    }
}
