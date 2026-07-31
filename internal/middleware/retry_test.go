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
