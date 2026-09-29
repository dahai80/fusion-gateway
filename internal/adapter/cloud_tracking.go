package adapter

import (
    "context"

    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

type cloudTrackingProvider struct {
    inner Provider
}

func WrapCloudTracking(p Provider) Provider {
    return &cloudTrackingProvider{inner: p}
}

// Unwrap exposes the underlying provider so interface assertions (e.g. the
// MessagesProvider check in the /v1/messages handler) resolve to the wrapped
// provider's real type. Without this, bedrock/vertex/foundry wrapped by
// WrapCloudTracking fail the MessagesProvider type assertion and silently fall
// to the lossy OpenAI conversion path, losing native Anthropic SSE events
// (audit H2). This is the Go convention for decorator types (errors.Unwrap,
// http.RoundTripper chains).
func (c *cloudTrackingProvider) Unwrap() Provider {
    return c.inner
}

func (c *cloudTrackingProvider) Name() string {
    return c.inner.Name()
}

func (c *cloudTrackingProvider) HealthCheck(ctx context.Context) error {
    return c.inner.HealthCheck(ctx)
}

func (c *cloudTrackingProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return c.inner.ListModels(ctx)
}

func (c *cloudTrackingProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    observability.IncrCloudInFlight()
    defer observability.DecrCloudInFlight()
    return c.inner.Chat(ctx, req)
}

func (c *cloudTrackingProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    observability.IncrCloudInFlight()
    defer observability.DecrCloudInFlight()
    return c.inner.Embedding(ctx, req)
}

func (c *cloudTrackingProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    observability.IncrCloudInFlight()
    defer observability.DecrCloudInFlight()
    return c.inner.Rerank(ctx, req)
}

func (c *cloudTrackingProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    observability.IncrCloudInFlight()
    ch, err := c.inner.StreamChat(ctx, req)
    if err != nil {
        observability.DecrCloudInFlight()
        return nil, err
    }
    return wrapCloudInFlightStream(ch), nil
}

// wrapCloudInFlightStream wraps the provider's chunk channel so the in-flight
// gauge is decremented when the stream is fully drained (channel closed) —
// no extra racing drain consumer (#168). The previous design spawned a
// per-request "cloud_inflight_drain" goroutine that raced the real consumer
// for chunks and could block forever if the channel was never closed.
func wrapCloudInFlightStream(ch <-chan StreamChunk) <-chan StreamChunk {
    out := make(chan StreamChunk, cap(ch))
    safego.Go("cloud_inflight_wrap", func() {
        defer close(out)
        defer observability.DecrCloudInFlight()
        for chunk := range ch {
            out <- chunk
        }
    })
    return out
}
