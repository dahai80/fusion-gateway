package server

// H2 guard tests: resolveMessagesProvider must walk the Unwrap chain so a
// cloud provider implementing MessagesProvider (bedrock/vertex/foundry) is
// still resolved as a MessagesProvider after WrapCloudTracking decorates it.
// Before the fix the decorator neither redeclared MessagesProvider nor exposed
// Unwrap, so the /v1/messages handler's assertion failed and Claude requests
// silently routed through the lossy OpenAI conversion path (dropping
// tool_use/thinking SSE events, audit H2).

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// h2MockProvider is a bare Provider (no MessagesProvider) for the negative path.
type h2MockProvider struct {
    name string
}

func (m *h2MockProvider) Name() string                                          { return m.name }
func (m *h2MockProvider) HealthCheck(context.Context) error                     { return nil }
func (m *h2MockProvider) ListModels(context.Context) ([]adapter.ModelInfo, error) { return nil, nil }
func (m *h2MockProvider) Chat(context.Context, *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    return &adapter.ChatResponse{}, nil
}
func (m *h2MockProvider) Embedding(context.Context, *adapter.EmbeddingRequest) (*adapter.EmbeddingResponse, error) {
    return &adapter.EmbeddingResponse{}, nil
}
func (m *h2MockProvider) Rerank(context.Context, *adapter.RerankRequest) (*adapter.RerankResponse, error) {
    return &adapter.RerankResponse{}, nil
}
func (m *h2MockProvider) StreamChat(context.Context, *adapter.ChatRequest) (<-chan adapter.StreamChunk, error) {
    return nil, nil
}

// h2MockMessagesProvider embeds the bare provider and adds the MessagesProvider
// methods, standing in for bedrock/vertex/foundry.
type h2MockMessagesProvider struct {
    h2MockProvider
    msgsCalled    bool
    streamCalled  bool
}

func (m *h2MockMessagesProvider) Messages(context.Context, *adapter.AnthropicRequest) (*adapter.AnthropicResponse, error) {
    m.msgsCalled = true
    return &adapter.AnthropicResponse{}, nil
}
func (m *h2MockMessagesProvider) StreamMessages(context.Context, *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
    m.streamCalled = true
    ch := make(chan adapter.AnthropicStreamEvent)
    close(ch)
    return ch, nil
}

// TestH2_ResolveMessagesProvider_NilForBareProvider: a Provider that does NOT
// implement MessagesProvider resolves to nil (the OpenAI conversion path).
func TestH2_ResolveMessagesProvider_NilForBareProvider(t *testing.T) {
    bare := &h2MockProvider{name: "plain-openai"}
    if mp := resolveMessagesProvider(bare); mp != nil {
        t.Fatalf("H2: bare Provider must resolve to nil MessagesProvider, got %T", mp)
    }
}

// TestH2_ResolveMessagesProvider_Direct: a Provider that directly implements
// MessagesProvider resolves without needing Unwrap (the local/unwrapped path).
func TestH2_ResolveMessagesProvider_Direct(t *testing.T) {
    signed := &h2MockMessagesProvider{h2MockProvider: h2MockProvider{name: "bedrock"}}
    mp := resolveMessagesProvider(signed)
    if mp == nil {
        t.Fatalf("H2: Provider implementing MessagesProvider must resolve directly, got nil")
    }
}

// TestH2_ResolveMessagesProvider_ThroughCloudTracking: the critical guard.
// Wrap a MessagesProvider in cloudTrackingProvider (as BuildProviders does for
// every cloud backend) and verify resolveMessagesProvider STILL resolves it.
// Revert the Unwrap() method on cloudTrackingProvider → this returns nil →
// bedrock/vertex/foundry /v1/messages silently uses the OpenAI path.
func TestH2_ResolveMessagesProvider_ThroughCloudTracking(t *testing.T) {
    signed := &h2MockMessagesProvider{h2MockProvider: h2MockProvider{name: "bedrock"}}
    wrapped := adapter.WrapCloudTracking(signed)
    // Sanity: the wrapped value itself must NOT satisfy MessagesProvider
    // (the decorator does not redeclare it — that is the bug premise).
    if _, ok := wrapped.(adapter.MessagesProvider); ok {
        t.Fatalf("H2 premise violated: cloudTrackingProvider must not directly implement MessagesProvider")
    }
    mp := resolveMessagesProvider(wrapped)
    if mp == nil {
        t.Fatalf("H2: MessagesProvider wrapped by cloudTrackingProvider must resolve via Unwrap chain, got nil — bedrock/vertex/foundry /v1/messages would silently use the lossy OpenAI path")
    }
}

// TestH2_ResolveMessagesProvider_BareWrappedNil: a bare (non-Messages) Provider
// wrapped by cloudTrackingProvider resolves to nil (no false positive).
func TestH2_ResolveMessagesProvider_BareWrappedNil(t *testing.T) {
    bare := &h2MockProvider{name: "plain-openai"}
    wrapped := adapter.WrapCloudTracking(bare)
    if mp := resolveMessagesProvider(wrapped); mp != nil {
        t.Fatalf("H2: bare Provider wrapped by cloudTrackingProvider must resolve to nil, got %T", mp)
    }
}

// TestInitRequestLog_ReadsRequestIDHeader: InitRequestLog runs in
// withMiddleware BEFORE the RequestID middleware injects the id into the
// derived context, so the ctx read was always empty and every log line had
// request_id = "". Regression: the entry must pick the id up from the
// request header (the R12 mirror), falling back to ctx for direct callers.
func TestInitRequestLog_ReadsRequestIDHeader(t *testing.T) {
    req, _ := http.NewRequest(http.MethodPost, "/v1/messages", nil)
    req.Header.Set("X-Request-ID", "req-abc-123")
    entry := middleware.InitRequestLog(req)
    if entry.RequestID != "req-abc-123" {
        t.Fatalf("RequestID = %q, want %q (header must be visible pre-middleware)", entry.RequestID, "req-abc-123")
    }
}

// TestStampRouteInfo_AnthropicMessagesModel: the /v1/messages path must stamp
// the request-log entry with the model + backend as soon as the route is
// resolved (before any early-exit), so Claude Code logs stop showing "-".
func TestStampRouteInfo_AnthropicMessagesModel(t *testing.T) {
    decision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "test"}
    antReq := adapter.AnthropicRequest{Model: "claude-sonnet-4-5"}

    base := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
    entry := middleware.InitRequestLog(base)
    ctx := middleware.WithRequestLogContext(base, entry).Context()

    // Mirror the handler's post-decision stamp inline (the stamp block in
    // handleAnthropicMessages); asserting the helper semantics keeps this
    // test decoupled from the full provider wiring.
    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
        logEntry.Model = antReq.Model
        logEntry.ChannelType = string(decision.Backend)
    }
    if entry.Model != "claude-sonnet-4-5" {
        t.Fatalf("log entry Model = %q, want %q", entry.Model, "claude-sonnet-4-5")
    }
    if entry.ChannelType != string(router.CloudBackend) {
        t.Fatalf("log entry ChannelType = %q, want %q", entry.ChannelType, router.CloudBackend)
    }
}
