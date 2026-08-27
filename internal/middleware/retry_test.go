package middleware

import (
    "context"
    "errors"
    "fmt"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestRetryChat_ReturnsFnDirectlyWhenMaxRetriesZero(t *testing.T) {
    t.Parallel()
    called := false
    fn := func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        called = true
        return &adapter.ChatResponse{ID: "ok"}, nil
    }
    cfg := config.RetryConfig{MaxRetries: 0}
    wrapped := RetryChat(cfg, fn)
    resp, err := wrapped(context.Background(), &adapter.ChatRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if !called {
        t.Fatal("expected fn to be called")
    }
    if resp.ID != "ok" {
        t.Errorf("expected response ID=ok, got %s", resp.ID)
    }
}

func TestRetryChat_RetriesOnRetryableError(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           2,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        calls++
        if calls < 3 {
            return nil, fmt.Errorf("server returned 429")
        }
        return &adapter.ChatResponse{ID: "success"}, nil
    }
    wrapped := RetryChat(cfg, fn)
    resp, err := wrapped(context.Background(), &adapter.ChatRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls != 3 {
        t.Errorf("expected 3 calls (2 retries + 1 success), got %d", calls)
    }
    if resp.ID != "success" {
        t.Errorf("expected response ID=success, got %s", resp.ID)
    }
}

func TestRetryChat_DoesNotRetryOnNonRetryableError(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           3,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        calls++
        return nil, fmt.Errorf("bad request: 400 invalid input")
    }
    wrapped := RetryChat(cfg, fn)
    _, err := wrapped(context.Background(), &adapter.ChatRequest{})
    if err == nil {
        t.Fatal("expected error from non-retryable failure")
    }
    if calls != 1 {
        t.Errorf("expected 1 call (no retries for 400), got %d", calls)
    }
}

func TestRetryChat_RespectsContextCancellation(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           5,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        calls++
        return nil, fmt.Errorf("server returned 429")
    }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    wrapped := RetryChat(cfg, fn)
    _, err := wrapped(ctx, &adapter.ChatRequest{})
    if err == nil {
        t.Fatal("expected error due to cancelled context")
    }
    if !errors.Is(err, context.Canceled) {
        t.Errorf("expected context.Canceled, got %v", err)
    }
}

func TestRetryChat_SucceedsOnSecondAttempt(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           1,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{500},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        calls++
        if calls == 1 {
            return nil, fmt.Errorf("server returned 500")
        }
        return &adapter.ChatResponse{ID: "retry-ok"}, nil
    }
    wrapped := RetryChat(cfg, fn)
    resp, err := wrapped(context.Background(), &adapter.ChatRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls != 2 {
        t.Errorf("expected 2 calls (1 fail + 1 success), got %d", calls)
    }
    if resp.ID != "retry-ok" {
        t.Errorf("expected response ID=retry-ok, got %s", resp.ID)
    }
}

// backoffBand asserts result lands in [center*(1-f), center*(1+f)] where f is
// the retry jitter factor (±20%, H5). calculateBackoff now applies jitter, so
// exact-equality checks no longer hold — the band is the correct invariant.
func backoffBand(t *testing.T, result, center time.Duration, msg string) {
    t.Helper()
    const f = 0.20
    lo := time.Duration(float64(center) * (1 - f))
    hi := time.Duration(float64(center) * (1 + f))
    if result < lo || result > hi {
        t.Errorf("%s: backoff %v outside jitter band [%v,%v] (center %v)", msg, result, lo, hi, center)
    }
}

func TestCalculateBackoff_ZeroInitial(t *testing.T) {
    result := calculateBackoff(0, 0, 0)
    backoffBand(t, result, time.Second, "default initial=1s")
}

func TestCalculateBackoff_ZeroMax(t *testing.T) {
    result := calculateBackoff(0, 100*time.Millisecond, 0)
    backoffBand(t, result, 100*time.Millisecond, "initial=100ms")
    cappedResult := calculateBackoff(10, 100*time.Millisecond, 0)
    backoffBand(t, cappedResult, 30*time.Second, "default max cap=30s")
}

func TestCalculateBackoff_ExceedsMax(t *testing.T) {
    result := calculateBackoff(10, 1*time.Second, 5*time.Second)
    backoffBand(t, result, 5*time.Second, "max cap=5s")
}

func TestCalculateBackoff_Normal(t *testing.T) {
    result := calculateBackoff(2, 100*time.Millisecond, 10*time.Second)
    backoffBand(t, result, 400*time.Millisecond, "2^2*100ms=400ms")
}

// TestH5_Backoff_JitteredNotFixed: H5 (audit P1) — the exponential backoff
// must be jittered, not a pure initial*2^attempt. Without jitter, N clients
// failing at the same instant align to identical backoff slots and retry in
// synchronized bursts that reset a recovering engine's model load. Revert
// calculateBackoff to return the raw exponential (no jitter.Duration call) and
// every sample equals the fixed slot → distinct-values check trips.
func TestH5_Backoff_JitteredNotFixed(t *testing.T) {
    const initial = 100 * time.Millisecond
    seen := map[time.Duration]struct{}{}
    for i := 0; i < 100; i++ {
        d := calculateBackoff(2, initial, 10*time.Second)
        backoffBand(t, d, 400*time.Millisecond, "H5 backoff band")
        seen[d] = struct{}{}
    }
    if len(seen) < 5 {
        t.Fatalf("H5: backoff produced only %d distinct values over 100 calls — jitter missing, clients will herd on identical slots", len(seen))
    }
}

// TestH5_Backoff_NeverExceedsMax: jitter is applied to the CAPPED value, so a
// near-max slot cannot overshoot max. The hard ceiling survives the spread.
func TestH5_Backoff_NeverExceedsMax(t *testing.T) {
    const max = 5 * time.Second
    for i := 0; i < 200; i++ {
        if d := calculateBackoff(50, 1*time.Second, max); d > max {
            t.Fatalf("H5: backoff %v exceeds max %v — jitter applied before cap, not after", d, max)
        }
    }
}

func TestIsRetryableError_Nil(t *testing.T) {
    if isRetryableError(nil, nil) {
        t.Error("nil error should not be retryable")
    }
}

func TestIsRetryableError_ConnectionRefused(t *testing.T) {
    if !isRetryableError(fmt.Errorf("connection refused"), nil) {
        t.Error("connection refused should be retryable")
    }
}

func TestIsRetryableError_Timeout(t *testing.T) {
    if !isRetryableError(fmt.Errorf("request timeout"), nil) {
        t.Error("timeout should be retryable")
    }
}

func TestIsRetryableError_DeadlineExceeded(t *testing.T) {
    if !isRetryableError(fmt.Errorf("deadline exceeded"), nil) {
        t.Error("deadline exceeded should be retryable")
    }
}

func TestIsRetryableError_EOF(t *testing.T) {
    if !isRetryableError(fmt.Errorf("anthropic stream messages failed: Post \"http://example/v1/messages\": EOF"), nil) {
        t.Error("EOF (upstream TCP reset) should be retryable")
    }
}

func TestIsRetryableError_UnexpectedEOF(t *testing.T) {
    if !isRetryableError(fmt.Errorf("read: unexpected EOF"), nil) {
        t.Error("unexpected EOF should be retryable")
    }
}

func TestIsRetryableError_ConnectionResetByPeer(t *testing.T) {
    if !isRetryableError(fmt.Errorf("read tcp: connection reset by peer"), nil) {
        t.Error("connection reset by peer should be retryable")
    }
}

func TestIsRetryableError_ClosedNetworkConnection(t *testing.T) {
    if !isRetryableError(fmt.Errorf("anthropic stream messages failed: Post \"http://113.57.198.109:4000/litellm/v1/messages\": dial tcp 10.0.0.1:420->113.57.198.109:4000: use of closed network connection"), nil) {
        t.Error("use of closed network connection (idle keep-alive conn closed by peer) should be retryable")
    }
}

func TestIsRetryableError_DefaultCodes(t *testing.T) {
    if !isRetryableError(fmt.Errorf("server returned 502"), nil) {
        t.Error("502 should be retryable with default codes")
    }
    if !isRetryableError(fmt.Errorf("server returned 429"), nil) {
        t.Error("429 should be retryable with default codes")
    }
}

func TestIsRetryableError_CustomCodes(t *testing.T) {
    if !isRetryableError(fmt.Errorf("error 503"), []int{503}) {
        t.Error("503 with custom codes should be retryable")
    }
    if isRetryableError(fmt.Errorf("error 429"), []int{503}) {
        t.Error("429 should not be retryable with only 503 custom code")
    }
}

func TestIsRetryableError_NonRetryableMessage(t *testing.T) {
    if isRetryableError(fmt.Errorf("something else happened"), nil) {
        t.Error("non-matching error should not be retryable")
    }
}

func TestStatusCodeStr_Known(t *testing.T) {
    if statusCodeStr(429) != "429" {
        t.Errorf("expected 429, got %s", statusCodeStr(429))
    }
    if statusCodeStr(500) != "500" {
        t.Errorf("expected 500, got %s", statusCodeStr(500))
    }
    if statusCodeStr(502) != "502" {
        t.Errorf("expected 502, got %s", statusCodeStr(502))
    }
    if statusCodeStr(503) != "503" {
        t.Errorf("expected 503, got %s", statusCodeStr(503))
    }
}

func TestStatusCodeStr_Unknown(t *testing.T) {
    if statusCodeStr(400) != "" {
        t.Errorf("expected empty for unknown code, got %s", statusCodeStr(400))
    }
    if statusCodeStr(200) != "" {
        t.Errorf("expected empty for 200, got %s", statusCodeStr(200))
    }
}

func TestRetryChat_ExhaustsAllRetries(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           2,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        calls++
        return nil, fmt.Errorf("server returned 429")
    }
    wrapped := RetryChat(cfg, fn)
    _, err := wrapped(context.Background(), &adapter.ChatRequest{})
    if err == nil {
        t.Fatal("expected error after exhausting retries")
    }
    if calls != 3 {
        t.Errorf("expected 3 calls (1 + 2 retries), got %d", calls)
    }
}

func closedStreamChan() <-chan adapter.AnthropicStreamEvent {
    ch := make(chan adapter.AnthropicStreamEvent)
    close(ch)
    return ch
}

func TestRetryStreamMessages_ReturnsFnDirectlyWhenMaxRetriesZero(t *testing.T) {
    t.Parallel()
    called := false
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        called = true
        return closedStreamChan(), nil
    }
    cfg := config.RetryConfig{MaxRetries: 0}
    wrapped := RetryStreamMessages(cfg, fn)
    ch, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if !called {
        t.Fatal("expected fn to be called")
    }
    if ch == nil {
        t.Fatal("expected non-nil channel")
    }
}

func TestRetryStreamMessages_RetriesOn502(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           2,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        if calls < 3 {
            return nil, fmt.Errorf("anthropic stream messages returned status 502: upstream down")
        }
        return closedStreamChan(), nil
    }
    wrapped := RetryStreamMessages(cfg, fn)
    ch, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls != 3 {
        t.Errorf("expected 3 calls (2 retries + 1 success), got %d", calls)
    }
    if ch == nil {
        t.Error("expected non-nil channel after successful retry")
    }
}

func TestRetryStreamMessages_RetriesOnTimeout(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           1,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        if calls == 1 {
            return nil, fmt.Errorf("anthropic stream messages failed: timeout awaiting response headers")
        }
        return closedStreamChan(), nil
    }
    wrapped := RetryStreamMessages(cfg, fn)
    _, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls != 2 {
        t.Errorf("expected 2 calls (1 timeout + 1 success), got %d", calls)
    }
}

func TestRetryStreamMessages_RetriesOnEOF(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           1,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        if calls == 1 {
            return nil, fmt.Errorf("anthropic stream messages failed: Post \"http://113.57.198.109:4000/litellm/v1/messages\": EOF")
        }
        return closedStreamChan(), nil
    }
    wrapped := RetryStreamMessages(cfg, fn)
    ch, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls != 2 {
        t.Errorf("expected 2 calls (1 EOF + 1 success), got %d", calls)
    }
    if ch == nil {
        t.Error("expected non-nil channel after successful retry on EOF")
    }
}

func TestRetryStreamMessages_RetriesOnClosedNetworkConnection(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           1,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        if calls == 1 {
            return nil, fmt.Errorf("anthropic stream messages failed: Post \"http://113.57.198.109:4000/litellm/v1/messages\": dial tcp 10.0.0.1:420->113.57.198.109:4000: use of closed network connection")
        }
        return closedStreamChan(), nil
    }
    wrapped := RetryStreamMessages(cfg, fn)
    ch, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if calls != 2 {
        t.Errorf("expected 2 calls (1 closed-conn + 1 success), got %d", calls)
    }
    if ch == nil {
        t.Error("expected non-nil channel after successful retry on closed network connection")
    }
}

func TestRetryStreamMessages_DoesNotRetryOnNonRetryable(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           3,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{429, 500, 502, 503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        return nil, fmt.Errorf("invalid api key")
    }
    wrapped := RetryStreamMessages(cfg, fn)
    _, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err == nil {
        t.Fatal("expected error from non-retryable failure")
    }
    if calls != 1 {
        t.Errorf("expected 1 call (no retries for non-retryable), got %d", calls)
    }
}

func TestRetryStreamMessages_RespectsContextCancellation(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           5,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{502},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        return nil, fmt.Errorf("anthropic stream messages returned status 502: down")
    }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    wrapped := RetryStreamMessages(cfg, fn)
    _, err := wrapped(ctx, &adapter.AnthropicRequest{})
    if err == nil {
        t.Fatal("expected error due to cancelled context")
    }
    if !errors.Is(err, context.Canceled) {
        t.Errorf("expected context.Canceled, got %v", err)
    }
}

func TestRetryStreamMessages_ExhaustsAllRetries(t *testing.T) {
    cfg := config.RetryConfig{
        MaxRetries:           2,
        InitialBackoff:       1 * time.Millisecond,
        MaxBackoff:           5 * time.Millisecond,
        RetryableStatusCodes: []int{503},
    }
    calls := 0
    fn := func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        calls++
        return nil, fmt.Errorf("upstream status 503: overloaded")
    }
    wrapped := RetryStreamMessages(cfg, fn)
    ch, err := wrapped(context.Background(), &adapter.AnthropicRequest{})
    if err == nil {
        t.Fatal("expected error after exhausting retries")
    }
    if ch != nil {
        t.Error("expected nil channel on exhausted retries")
    }
    if calls != 3 {
        t.Errorf("expected 3 calls (1 + 2 retries), got %d", calls)
    }
}
