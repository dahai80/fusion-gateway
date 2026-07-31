package middleware

import (
    "context"
    "log/slog"
    "net/http"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type contextKey string

const (
    AuthKeyConfigKey contextKey = "auth_key_config"
    IsMasterKeyKey   contextKey = "is_master_key"
    RequestIDKey     contextKey = "request_id"
)

type RateLimiter struct {
    mu       sync.Mutex
    counters map[string]*slidingWindow
}

type slidingWindow struct {
    timestamps  []time.Time
    tokens      []int
    totalTokens int
}

func NewRateLimiter() *RateLimiter {
    return &RateLimiter{
        counters: make(map[string]*slidingWindow),
    }
}

func (rl *RateLimiter) AllowRPM(key string, rpm int) bool {
    if rpm <= 0 {
        return true
    }
    rl.mu.Lock()
    defer rl.mu.Unlock()

    now := time.Now()
    windowStart := now.Add(-time.Minute)

    sw, exists := rl.counters[key+":rpm"]
    if !exists {
        sw = &slidingWindow{}
        rl.counters[key+":rpm"] = sw
    }

    filtered := sw.timestamps[:0]
    for _, t := range sw.timestamps {
        if t.After(windowStart) {
            filtered = append(filtered, t)
        }
    }
    sw.timestamps = filtered

    if len(sw.timestamps) >= rpm {
        return false
    }

    sw.timestamps = append(sw.timestamps, now)
    return true
}

func (rl *RateLimiter) AllowTPM(key string, tpm int, tokenCount int) bool {
    if tpm <= 0 {
        return true
    }
    rl.mu.Lock()
    defer rl.mu.Unlock()

    now := time.Now()
    windowStart := now.Add(-time.Minute)

    mapKey := key + ":tpm"
    sw, exists := rl.counters[mapKey]
    if !exists {
        sw = &slidingWindow{}
        rl.counters[mapKey] = sw
    }

    filteredTs := sw.timestamps[:0]
    filteredTok := sw.tokens[:0]
    totalTokens := 0
    for i, t := range sw.timestamps {
        if t.After(windowStart) {
            filteredTs = append(filteredTs, t)
            filteredTok = append(filteredTok, sw.tokens[i])
            totalTokens += sw.tokens[i]
        }
    }
    sw.timestamps = filteredTs
    sw.tokens = filteredTok
    sw.totalTokens = totalTokens

    if sw.totalTokens+tokenCount > tpm {
        return false
    }

    sw.timestamps = append(sw.timestamps, now)
    sw.tokens = append(sw.tokens, tokenCount)
    sw.totalTokens += tokenCount
    return true
}

func (rl *RateLimiter) RemainingRPM(key string, rpm int) int {
    if rpm <= 0 {
        return -1
    }
    rl.mu.Lock()
    defer rl.mu.Unlock()

    now := time.Now()
    windowStart := now.Add(-time.Minute)

    sw, exists := rl.counters[key+":rpm"]
    if !exists {
        return rpm
    }

    count := 0
    for _, t := range sw.timestamps {
        if t.After(windowStart) {
            count++
        }
    }
    remaining := rpm - count
    if remaining < 0 {
        return 0
    }
    return remaining
}

func RateLimit(cfg *config.RateLimitConfig, limiter *RateLimiter, tokFn func(ctx context.Context) int) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !cfg.Enabled {
                next.ServeHTTP(w, r)
                return
            }

            isMaster, _ := r.Context().Value(IsMasterKeyKey).(bool)
            if isMaster {
                next.ServeHTTP(w, r)
                return
            }

            keyCfg, _ := r.Context().Value(AuthKeyConfigKey).(*config.AuthKeyConfig)
            if keyCfg == nil {
                if cfg.KeyEnforcement {
                    slog.Warn("rate limit: no key config in context, denying", "path", r.URL.Path)
                    http.Error(w, `{"error":{"message":"Rate limit: no API key","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
                    return
                }
                next.ServeHTTP(w, r)
                return
            }

            keyName := keyCfg.Name
            if keyName == "" {
                keyName = keyCfg.Key[:8]
            }

            if !limiter.AllowRPM(keyName, keyCfg.RPM) {
                slog.Warn("rate limit: RPM exceeded", "key", keyName, "rpm", keyCfg.RPM)
                w.Header().Set("Retry-After", "1")
                w.Header().Set("X-RateLimit-Remaining", "0")
                http.Error(w, `{"error":{"message":"Rate limit exceeded (RPM)","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
                return
            }

            tokenCount := 0
            if tokFn != nil {
                tokenCount = tokFn(r.Context())
            }
            if tokenCount > 0 && !limiter.AllowTPM(keyName, keyCfg.TPM, tokenCount) {
                slog.Warn("rate limit: TPM exceeded", "key", keyName, "tpm", keyCfg.TPM, "tokens", tokenCount)
                w.Header().Set("Retry-After", "1")
                w.Header().Set("X-RateLimit-Remaining", "0")
                http.Error(w, `{"error":{"message":"Rate limit exceeded (TPM)","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
                return
            }

            remaining := limiter.RemainingRPM(keyName, keyCfg.RPM)
            if remaining >= 0 {
                w.Header().Set("X-RateLimit-Remaining", formatInt(remaining))
            }

            next.ServeHTTP(w, r)
        })
    }
}

func formatInt(v int) string {
    if v == 0 {
        return "0"
    }
    buf := make([]byte, 0, 12)
    negative := v < 0
    if negative {
        v = -v
    }
    for v > 0 {
        buf = append(buf, byte('0'+v%10))
        v /= 10
    }
    if negative {
        buf = append(buf, '-')
    }
    for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
        buf[i], buf[j] = buf[j], buf[i]
    }
    return string(buf)
}
