package adapter

import (
	"context"
	"testing"
	"time"
)

// stubSlotInner is a StreamChat provider that emits n numbered chunks then
// closes. Chunks carry a sequence so a stolen chunk is detectable.
type stubSlotInner struct {
	n     int
	delay time.Duration
}

func (s *stubSlotInner) Name() string { return "stub-slot-inner" }
func (s *stubSlotInner) HealthCheck(ctx context.Context) error { return nil }
func (s *stubSlotInner) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return nil, nil
}
func (s *stubSlotInner) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return nil, ErrLocalSlotFull
}
func (s *stubSlotInner) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, ErrLocalSlotFull
}
func (s *stubSlotInner) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	return nil, ErrLocalSlotFull
}
func (s *stubSlotInner) StreamChat(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, s.n)
	go func() {
		defer close(ch)
		for i := 0; i < s.n; i++ {
			ch <- StreamChunk{ID: string(rune('a' + i))}
			if s.delay > 0 {
				time.Sleep(s.delay)
			}
		}
	}()
	return ch, nil
}

// Regression: the release watcher used to `for range ch` the SAME channel the
// consumer reads — two racing receivers stole roughly half the chunks. The
// handler must now observe every chunk in order.
func TestLocalSlotStreamChatForwardsEveryChunk(t *testing.T) {
	inner := &stubSlotInner{n: 20, delay: time.Millisecond}
	mlx := &FusionMLXProvider{ /* zero-value role for slot accounting */ }
	// Bypass the engine cap plumbing: use the decorator purely for forwarding.
	prov := &localSlotProvider{inner: inner, mlx: mlx}

	ch, err := prov.StreamChat(context.Background(), &ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var got []string
	for chunk := range ch {
		got = append(got, chunk.ID)
	}
	if len(got) != inner.n {
		t.Fatalf("chunk loss: got %d of %d (%v)", len(got), inner.n, got)
	}
	for i, id := range got {
		if id != string(rune('a'+i)) {
			t.Fatalf("order broken at %d: %q", i, id)
		}
	}
}

// Slot release on stream completion: after the consumer drains the channel,
// the AcquireLocalSlot counter must be back to its pre-stream value.
func TestLocalSlotStreamChatReleasesSlotAfterClose(t *testing.T) {
	inner := &stubSlotInner{n: 5}
	mlx := &FusionMLXProvider{}
	prov := &localSlotProvider{inner: inner, mlx: mlx}

	before := mlx.InFlight()
	ch, err := prov.StreamChat(context.Background(), &ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range ch {
	}
	deadline := time.Now().Add(2 * time.Second)
	for mlx.InFlight() != before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if mlx.InFlight() != before {
		t.Fatalf("slot not released: in_flight=%d want=%d", mlx.InFlight(), before)
	}
}
