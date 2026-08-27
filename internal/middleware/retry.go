package middleware

import (
    "context"
    "log/slog"
    "math"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/jitter"
)

type RetryableFunc func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error)

func RetryChat(cfg config.RetryConfig, fn RetryableFunc) RetryableFunc {
    if cfg.MaxRetries <= 0 {
        return fn
    }

    return func(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        var lastErr error
        for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
            resp, err := fn(ctx, req)
            if err == nil {
                return resp, nil
            }

            lastErr = err
            if attempt == cfg.MaxRetries {
                break
            }

            if !isRetryableError(err, cfg.RetryableStatusCodes) {
                slog.Debug("non-retryable error, skipping retry", "error", err, "attempt", attempt)
                break
            }

            backoff := calculateBackoff(attempt, cfg.InitialBackoff, cfg.MaxBackoff)
            slog.Warn("retrying chat request",
                "attempt", attempt+1,
                "max_retries", cfg.MaxRetries,
                "backoff", backoff,
                "error", err,
            )

            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(backoff):
            }
        }
        return nil, lastErr
    }
}

// RetryableStreamMessagesFunc mirrors RetryableFunc for the /v1/messages path,
// whose connection phase returns a receive-only Anthropic SSE event channel.
// Retry covers only the connection phase: when StreamMessages returns an error
// (TTFB timeout / 502 / 503 / 429 / EOF / connection reset) before any response
// header is written to the client. Once a channel is returned, headers are
// committed and mid-stream disconnects are NOT retried (SSE is already
// flushing). Reuses isRetryableError + calculateBackoff unchanged: both
// AnthropicProvider (fmt.Errorf "...status %d...") and cloud-signed providers
// (*MessagesHTTPError "upstream status %d...") embed the status code in the
// error string, so substring detection matches without a typed-error check.
// isRetryableError also matches transport-reset substrings (EOF, connection
// reset by peer) so an upstream TCP reset during the connection phase retries
// transparently (issue #73).
type RetryableStreamMessagesFunc func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error)

func RetryStreamMessages(cfg config.RetryConfig, fn RetryableStreamMessagesFunc) RetryableStreamMessagesFunc {
    if cfg.MaxRetries <= 0 {
        return fn
    }

    return func(ctx context.Context, req *adapter.AnthropicRequest) (<-chan adapter.AnthropicStreamEvent, error) {
        var lastErr error
        for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
            ch, err := fn(ctx, req)
            if err == nil {
                return ch, nil
            }

            lastErr = err
            if attempt == cfg.MaxRetries {
                break
            }

            if !isRetryableError(err, cfg.RetryableStatusCodes) {
                slog.Debug("non-retryable stream messages error, skipping retry", "error", err, "attempt", attempt)
                break
            }

            backoff := calculateBackoff(attempt, cfg.InitialBackoff, cfg.MaxBackoff)
            slog.Warn("retrying anthropic stream messages",
                "attempt", attempt+1,
                "max_retries", cfg.MaxRetries,
                "backoff", backoff,
                "error", err,
            )

            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(backoff):
            }
        }
        return nil, lastErr
    }
}

func isRetryableError(err error, codes []int) bool {
    if err == nil {
        return false
    }

    if len(codes) == 0 {
        codes = []int{429, 500, 502, 503}
    }

    errStr := err.Error()
    for _, code := range codes {
        if strings.Contains(errStr, statusCodeStr(code)) {
            return true
        }
    }

    if strings.Contains(errStr, "connection refused") ||
        strings.Contains(errStr, "timeout") ||
        strings.Contains(errStr, "deadline exceeded") ||
        strings.Contains(errStr, "EOF") ||
        strings.Contains(errStr, "connection reset by peer") ||
        strings.Contains(errStr, "use of closed network connection") {
        return true
    }

    return false
}

func calculateBackoff(attempt int, initial, max time.Duration) time.Duration {
    if initial <= 0 {
        initial = time.Second
    }
    if max <= 0 {
        max = 30 * time.Second
    }

    backoff := float64(initial) * math.Pow(2, float64(attempt))
    if backoff > float64(max) {
        backoff = float64(max)
    }
    // H5 (audit P1): apply ±20% jitter to the exponential backoff. Without
    // jitter, N clients that fail simultaneously (e.g. fusion-mlx restart)
    // align to identical backoff slots — 10s/20s/40s/60s… — and every retry
    // lands at the same instant, hammering the recovering engine in synchronized
    // bursts that reset its model load and starve it of a quiet window. The
    // jitter spreads the retries across a window instead of a single point.
    // Jitter is applied to the CAPPED value, then re-clamped to max: the ±20%
    // spread can push a near-max slot above max, so a final min(.,max) keeps
    // the hard ceiling while still spreading everything below it.
    jittered := jitter.Duration(time.Duration(backoff))
    if jittered > max {
        return max
    }
    return jittered
}

func statusCodeStr(code int) string {
    switch code {
    case 429:
        return "429"
    case 500:
        return "500"
    case 502:
        return "502"
    case 503:
        return "503"
    default:
        return ""
    }
}
