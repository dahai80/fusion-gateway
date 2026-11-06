package adapter

import (
    "context"
    "errors"
    "sync"
    "testing"
)

type outcomeRecorder struct {
    mu     sync.Mutex
    events []outcomeEvent
}

type outcomeEvent struct {
    backend string
    ok      bool
}

func (r *outcomeRecorder) RecordOutcome(backend string, ok bool) {
    r.mu.Lock()
    r.events = append(r.events, outcomeEvent{backend, ok})
    r.mu.Unlock()
}

type stubProvider struct {
    name       string
    chatErr    error
    streamErr  error
    embedErr   error
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) HealthCheck(ctx context.Context) error { return nil }
func (s *stubProvider) ListModels(ctx context.Context) ([]ModelInfo, error) { return nil, nil }
func (s *stubProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    if s.chatErr != nil {
        return nil, s.chatErr
    }
    return &ChatResponse{}, nil
}
func (s *stubProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    if s.streamErr != nil {
        return nil, s.streamErr
    }
    ch := make(chan StreamChunk, 1)
    close(ch)
    return ch, nil
}
func (s *stubProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    if s.embedErr != nil {
        return nil, s.embedErr
    }
    return &EmbeddingResponse{}, nil
}
func (s *stubProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return &RerankResponse{}, nil
}

func TestCloudTrackingOutcomeRecorded(t *testing.T) {
    rec := &outcomeRecorder{}
    prev := cloudOutcomeRecorder
    SetCloudOutcomeRecorder(rec)
    defer SetCloudOutcomeRecorder(prev)

    ok := &stubProvider{name: "good-cloud"}
    bad := &stubProvider{name: "bad-cloud", chatErr: errors.New("503"), streamErr: errors.New("dial fail"), embedErr: errors.New("timeout")}

    ctx := context.Background()
    _, _ = WrapCloudTracking(ok).Chat(ctx, &ChatRequest{})
    _, _ = WrapCloudTracking(bad).Chat(ctx, &ChatRequest{})
    _, _ = WrapCloudTracking(bad).StreamChat(ctx, &ChatRequest{})
    _, _ = WrapCloudTracking(ok).StreamChat(ctx, &ChatRequest{})
    _, _ = WrapCloudTracking(bad).Embedding(ctx, &EmbeddingRequest{})
    _, _ = WrapCloudTracking(ok).Embedding(ctx, &EmbeddingRequest{})

    rec.mu.Lock()
    events := append([]outcomeEvent(nil), rec.events...)
    rec.mu.Unlock()

    want := []outcomeEvent{
        {"good-cloud", true},
        {"bad-cloud", false},
        {"bad-cloud", false},
        {"good-cloud", true},
        {"bad-cloud", false},
        {"good-cloud", true},
    }
    if len(events) != len(want) {
        t.Fatalf("expected %d outcome events, got %d: %+v", len(want), len(events), events)
    }
    for i, w := range want {
        if events[i] != w {
            t.Fatalf("event %d: expected %+v, got %+v", i, w, events[i])
        }
    }
}

func TestCloudTrackingOutcomeNilRecorderSafe(t *testing.T) {
    prev := cloudOutcomeRecorder
    SetCloudOutcomeRecorder(nil)
    defer SetCloudOutcomeRecorder(prev)

    p := WrapCloudTracking(&stubProvider{name: "any-cloud", chatErr: errors.New("boom")})
    if _, err := p.Chat(context.Background(), &ChatRequest{}); err == nil {
        t.Fatal("expected error passthrough")
    }
}
