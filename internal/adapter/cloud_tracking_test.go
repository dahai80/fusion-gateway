package adapter

import (
    "context"
    "errors"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/observability"
)

type mockProvider struct {
    name      string
    streamErr error
    chunks    []StreamChunk
}

func (m *mockProvider) Name() string                                  { return m.name }
func (m *mockProvider) HealthCheck(ctx context.Context) error         { return nil }
func (m *mockProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return nil, nil
}
func (m *mockProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return &ChatResponse{}, nil
}
func (m *mockProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return &EmbeddingResponse{}, nil
}
func (m *mockProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return &RerankResponse{}, nil
}
func (m *mockProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    if m.streamErr != nil {
        return nil, m.streamErr
    }
    ch := make(chan StreamChunk, len(m.chunks))
    for _, c := range m.chunks {
        ch <- c
    }
    close(ch)
    return ch, nil
}

func resetCloudInFlight() {
    observability.UpdateInFlight("cloud", 0)
}

// TestEI6_WrapChatBalanced guards that a wrapped cloud provider inc's on Chat
// entry and dec's on return — CloudInFlight must return to its baseline after
// the call, never leaking an in-flight slot.
func TestEI6_WrapChatBalanced(t *testing.T) {
    resetCloudInFlight()
    wrapped := WrapCloudTracking(&mockProvider{name: "mock-cloud"})
    _, err := wrapped.Chat(context.Background(), &ChatRequest{})
    if err != nil {
        t.Fatalf("unexpected chat error: %v", err)
    }
    if got := observability.CloudInFlight(); got != 0 {
        t.Fatalf("EI6: cloud in-flight must return to 0 after Chat, got %d (slot leak)", got)
    }
}

// TestEI6_WrapStreamChatErrorDecs guards that a StreamChat returning an error
// dec's immediately — no drain goroutine started, no slot leak.
func TestEI6_WrapStreamChatErrorDecs(t *testing.T) {
    resetCloudInFlight()
    wrapped := WrapCloudTracking(&mockProvider{name: "mock-cloud", streamErr: errors.New("upstream 502")})
    _, err := wrapped.StreamChat(context.Background(), &ChatRequest{})
    if err == nil {
        t.Fatal("expected stream error")
    }
    if got := observability.CloudInFlight(); got != 0 {
        t.Fatalf("EI6: cloud in-flight must be 0 after StreamChat error, got %d (slot leak)", got)
    }
}

// TestEI6_WrapStreamChatDrainsOnClose guards that a StreamChat returning a
// channel dec's only when the channel is drained/closed — the in-flight slot
// is held for the stream's lifetime, mirroring the cluster node_adapter
// pattern. This is the EI6 lifecycle fix: inc on entry, dec on stream end.
func TestEI6_WrapStreamChatDrainsOnClose(t *testing.T) {
    resetCloudInFlight()
    // Use a channel the test controls so we can assert the slot is held
    // mid-stream and released on close.
    ctrlCh := make(chan StreamChunk, 1)
    mp := &controllableStreamProvider{name: "mock-cloud", ch: ctrlCh}
    wrapped := WrapCloudTracking(mp)

    _, err := wrapped.StreamChat(context.Background(), &ChatRequest{})
    if err != nil {
        t.Fatalf("unexpected stream error: %v", err)
    }

    // Slot must be HELD while the stream channel is still open.
    if got := observability.CloudInFlight(); got != 1 {
        t.Fatalf("EI6: cloud in-flight must be 1 while stream open, got %d", got)
    }

    // Closing the stream channel triggers the drain goroutine to dec.
    close(ctrlCh)

    // Give the drain goroutine a moment to observe the close.
    deadline := time.Now().Add(time.Second)
    for time.Now().Before(deadline) {
        if observability.CloudInFlight() == 0 {
            return
        }
        time.Sleep(5 * time.Millisecond)
    }
    t.Fatalf("EI6: cloud in-flight must return to 0 after stream closed, got %d (slot leak)", observability.CloudInFlight())
}

type controllableStreamProvider struct {
    name string
    ch   chan StreamChunk
}

func (m *controllableStreamProvider) Name() string                                  { return m.name }
func (m *controllableStreamProvider) HealthCheck(ctx context.Context) error         { return nil }
func (m *controllableStreamProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return nil, nil
}
func (m *controllableStreamProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    return &ChatResponse{}, nil
}
func (m *controllableStreamProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return &EmbeddingResponse{}, nil
}
func (m *controllableStreamProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return &RerankResponse{}, nil
}
func (m *controllableStreamProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    return m.ch, nil
}

// TestEI6_WrapEmbeddingRerankBalanced guards Embedding and Rerank paths inc/dec
// the same as Chat — all inference entry points are tracked, not just chat.
func TestEI6_WrapEmbeddingRerankBalanced(t *testing.T) {
    resetCloudInFlight()
    wrapped := WrapCloudTracking(&mockProvider{name: "mock-cloud"})
    var wg sync.WaitGroup
    wg.Add(2)
    go func() {
        defer wg.Done()
        _, _ = wrapped.Embedding(context.Background(), &EmbeddingRequest{})
    }()
    go func() {
        defer wg.Done()
        _, _ = wrapped.Rerank(context.Background(), &RerankRequest{})
    }()
    wg.Wait()
    if got := observability.CloudInFlight(); got != 0 {
        t.Fatalf("EI6: cloud in-flight must return to 0 after Embedding+Rerank, got %d (slot leak)", got)
    }
}

// mockMessagesProvider is a Provider that ALSO implements MessagesProvider
// (the native Anthropic passthrough interface), standing in for
// bedrock/vertex/foundry in the H2 guard.
type mockMessagesProvider struct {
    mockProvider
}

func (m *mockMessagesProvider) Messages(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error) {
    return &AnthropicResponse{}, nil
}
func (m *mockMessagesProvider) StreamMessages(ctx context.Context, req *AnthropicRequest) (<-chan AnthropicStreamEvent, error) {
    ch := make(chan AnthropicStreamEvent)
    close(ch)
    return ch, nil
}

// TestH2_WrappedMessagesProvider_Unwraps guards audit H2: a cloud provider
// implementing MessagesProvider, when wrapped by WrapCloudTracking, must STILL
// be resolvable as a MessagesProvider via the Unwrap chain. Before the fix,
// cloudTrackingProvider did not expose Unwrap and did not redeclare
// MessagesProvider, so the /v1/messages handler's type assertion failed and
// silently routed Claude requests through the lossy OpenAI conversion path
// (dropping tool_use/thinking SSE events). Revert Unwrap() → this assertion
// fails.
func TestH2_WrappedMessagesProvider_Unwraps(t *testing.T) {
    signed := &mockMessagesProvider{mockProvider: mockProvider{name: "bedrock"}}
    wrapped := WrapCloudTracking(signed)

    // The wrapped value itself must NOT satisfy MessagesProvider directly
    // (that is the whole bug — the decorator does not redeclare it).
    if _, ok := wrapped.(MessagesProvider); ok {
        t.Fatalf("H2 premise violated: cloudTrackingProvider must NOT directly implement MessagesProvider")
    }

    // It must expose Unwrap, and the unwrapped provider must satisfy
    // MessagesProvider — this is what the /v1/messages handler relies on.
    unwrapper, ok := wrapped.(interface{ Unwrap() Provider })
    if !ok {
        t.Fatalf("H2: cloudTrackingProvider must expose Unwrap() so MessagesProvider resolves through the decorator chain")
    }
    inner := unwrapper.Unwrap()
    if inner == nil {
        t.Fatalf("H2: Unwrap() returned nil")
    }
    if _, ok := inner.(MessagesProvider); !ok {
        t.Fatalf("H2: unwrapped provider must satisfy MessagesProvider (bedrock/vertex/foundry native Anthropic passthrough), got %T", inner)
    }
}
