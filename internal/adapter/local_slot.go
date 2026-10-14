package adapter

import (
    "context"
    "errors"

    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// ErrImageUnsupported is returned by localSlotProvider.Images when the
// wrapped provider has no Images passthrough (mirrors the image handler's
// "Selected backend does not support image generation" 400).
var ErrImageUnsupported = errors.New("backend does not support image generation")

// localSlotProvider wraps a local-classified serving provider (an
// openai-compatible backend whose endpoint IS the fusion-mlx engine, #210)
// with RR4 slot accounting against the canonical FusionMLXProvider role
// instance, which owns the in-flight counter and the max_concurrent cap.
// Without it the serving path never touches the counter: RR4 is inert and
// the router's local in-flight signal stays 0 while real local requests run.
type localSlotProvider struct {
    inner Provider
    mlx   *FusionMLXProvider
}

// WrapLocalSlot decorates a local serving provider with the given engine
// role's slot accounting. The role instance is what main.go wires into the
// router (InFlight / ModelSet) and refreshes every 60s.
func WrapLocalSlot(p Provider, mlx *FusionMLXProvider) Provider {
    return &localSlotProvider{inner: p, mlx: mlx}
}

// Unwrap exposes the underlying provider so interface assertions (e.g. the
// Images check in the image handler, or MessagesProvider in /v1/messages)
// resolve to the wrapped provider's real type — a decorator without Unwrap
// silently drops those capabilities (H2 lesson from cloudTrackingProvider).
func (l *localSlotProvider) Unwrap() Provider {
    return l.inner
}

func (l *localSlotProvider) Name() string {
    return l.inner.Name()
}

func (l *localSlotProvider) HealthCheck(ctx context.Context) error {
    return l.inner.HealthCheck(ctx)
}

func (l *localSlotProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    return l.inner.ListModels(ctx)
}

// Chat enforces the RR4 hard cap before any connection opens: when the local
// engine is at max_concurrent, return ErrLocalSlotFull so the handler diverts
// to the A4 cloud fallback instead of overflowing the engine.
func (l *localSlotProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
    release, ok := l.mlx.AcquireLocalSlot()
    if !ok {
        return nil, ErrLocalSlotFull
    }
    defer release()
    return l.inner.Chat(ctx, req)
}

// StreamChat acquires the slot up front and hands ownership to a release
// watcher that fires when the inner pump closes the chunk channel. No extra
// channel hop: the consumer reads the inner channel directly, so cancel
// semantics (ctx-watcher closes resp.Body, pump exits, channel closes) are
// identical to the typed FusionMLXProvider path — the slot releases exactly
// when the stream pump finishes.
func (l *localSlotProvider) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
    release, ok := l.mlx.AcquireLocalSlot()
    if !ok {
        return nil, ErrLocalSlotFull
    }
    ch, err := l.inner.StreamChat(ctx, req)
    if err != nil {
        release()
        return nil, err
    }
    safego.Go("local_slot_release_watch", func() {
        for range ch {
        }
        release()
    })
    return ch, nil
}

// Embedding and Rerank forward without a slot acquire — the typed
// FusionMLXProvider does not cap them either (RR4 covers the chat inference
// paths); matching its semantics keeps one pattern, not two.
func (l *localSlotProvider) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
    return l.inner.Embedding(ctx, req)
}

func (l *localSlotProvider) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
    return l.inner.Rerank(ctx, req)
}

// Images forwards the passthrough (the reason the engine is registered as
// openai-compatible in the first place) without a slot acquire — the typed
// provider has no Images method, so there is no precedent semantics to match;
// image generation does not hold a chat inference slot.
func (l *localSlotProvider) Images(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
    type imager interface {
        Images(ctx context.Context, req *ImageRequest) (*ImageResponse, error)
    }
    if img, ok := l.inner.(imager); ok {
        return img.Images(ctx, req)
    }
    return nil, ErrImageUnsupported
}
