package adapter

import (
    "context"
    "log/slog"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// CloudOutcomeRecorder (G1) receives one call per cloud request outcome so
// the router's CloudStrategy can arm/clear per-deployment cooldowns.
// Implemented by router.CloudStrategy; wired once at server startup.
type CloudOutcomeRecorder interface {
    RecordOutcome(backend string, ok bool)
}

var cloudOutcomeMu sync.RWMutex
var cloudOutcomeRecorder CloudOutcomeRecorder

// SetCloudOutcomeRecorder wires the recorder; nil clears it. Called from
// server.New before serving — requests before that simply record nothing.
func SetCloudOutcomeRecorder(r CloudOutcomeRecorder) {
    cloudOutcomeMu.Lock()
    cloudOutcomeRecorder = r
    cloudOutcomeMu.Unlock()
}

func recordCloudOutcome(backend string, ok bool) {
    cloudOutcomeMu.RLock()
    r := cloudOutcomeRecorder
    cloudOutcomeMu.RUnlock()
    if r != nil && backend != "" {
        r.RecordOutcome(backend, ok)
    }
}

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
    resp, err := c.inner.Chat(ctx, req)
    observability.DecrCloudInFlight()
    recordCloudOutcome(c.inner.Name(), err == nil)
    return resp, err
}

func (c *cloudTrackingProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    observability.IncrCloudInFlight()
    resp, err := c.inner.Embedding(ctx, req)
    observability.DecrCloudInFlight()
    recordCloudOutcome(c.inner.Name(), err == nil)
    return resp, err
}

func (c *cloudTrackingProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    observability.IncrCloudInFlight()
    resp, err := c.inner.Rerank(ctx, req)
    observability.DecrCloudInFlight()
    recordCloudOutcome(c.inner.Name(), err == nil)
    return resp, err
}

// StreamChat records the outcome of the call itself only: a setup error is
// a failure, a successfully opened stream is a success. Mid-stream upstream
// failures surface as channel close/cancel and cannot be classified here
// without re-parsing chunks — they deliberately do not count toward the
// cooldown (conservative: cooldown only arms on definite failures).
func (c *cloudTrackingProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    observability.IncrCloudInFlight()
    ch, err := c.inner.StreamChat(ctx, req)
    if err != nil {
        observability.DecrCloudInFlight()
        recordCloudOutcome(c.inner.Name(), false)
        return nil, err
    }
    recordCloudOutcome(c.inner.Name(), true)
    return wrapCloudInFlightStream(ctx, ch), nil
}

// wrapCloudInFlightStream wraps the provider's chunk channel so the in-flight
// gauge is decremented when the stream is fully drained (channel closed) —
// no extra racing drain consumer (#168). The previous design spawned a
// per-request "cloud_inflight_drain" goroutine that raced the real consumer
// for chunks and could block forever if the channel was never closed.
func wrapCloudInFlightStream(ctx context.Context, ch <-chan StreamChunk) <-chan StreamChunk {
    out := make(chan StreamChunk, cap(ch))
    safego.Go("cloud_inflight_wrap", func() {
        defer close(out)
        defer observability.DecrCloudInFlight()
        for chunk := range ch {
            // C17 (audit): the bare send blocked forever when the consumer
            // exited without canceling ctx — the forwarder goroutine and the
            // in-flight gauge leaked. The ctx arm unwinds both (same pattern
            // as local_slot.go).
            select {
            case out <- chunk:
            case <-ctx.Done():
                slog.Debug("cloud in-flight stream forwarder stopped, consumer gone", "error", ctx.Err())
                return
            }
        }
    })
    return out
}
