package middleware

import (
    "context"
    "log/slog"
    "net/http"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

type RateLimiter struct {
    counters sync.Map // map[string]*keyState
    // R1: lifecycle.Worker wrapping the idle-cleanup goroutine so Server.Shutdown
    // can Stop (cancel + join) it instead of leaking on shutdown.
    cleanupWorker *lifecycle.Worker
}

// Close stops the background idle-cleanup goroutine (R1). Idempotent; safe to
// call from Server.Shutdown. Before R1 the cleanup ticker leaked on shutdown.
func (rl *RateLimiter) Close() {
    if rl == nil || rl.cleanupWorker == nil {
        return
    }
    rl.cleanupWorker.Stop()
    rl.cleanupWorker = nil
}

type contextKey string

const RequestIDKey contextKey = "request_id"

type keyState struct {
    mu         sync.Mutex
    sw         slidingWindow
    lastAccess time.Time
}

type slidingWindow struct {
    timestamps  []time.Time
    tokens      []int
    totalTokens int
}

func NewRateLimiter() *RateLimiter {
    rl := &RateLimiter{}
    // R1: launch cleanup through lifecycle.Worker so Shutdown can Stop (cancel
    // + join) it instead of leaking. H3 panic-restart inherited from Worker.
    rl.cleanupWorker = lifecycle.Start(context.Background(), "ratelimit_cleanup_idle", rl.cleanupIdle)
    return rl
}

func (rl *RateLimiter) getOrCreate(key string) *keyState {
    if v, ok := rl.counters.Load(key); ok {
        ks := v.(*keyState)
        ks.lastAccess = time.Now()
        return ks
    }
    ks := &keyState{lastAccess: time.Now()}
    actual, _ := rl.counters.LoadOrStore(key, ks)
    return actual.(*keyState)
}

func (rl *RateLimiter) AllowRPM(key string, rpm int) bool {
    if rpm <= 0 {
        return true
    }
    ks := rl.getOrCreate(key + ":rpm")
    ks.mu.Lock()
    defer ks.mu.Unlock()

    now := time.Now()
    windowStart := now.Add(-time.Minute)

    filtered := ks.sw.timestamps[:0]
    for _, t := range ks.sw.timestamps {
        if t.After(windowStart) {
            filtered = append(filtered, t)
        }
    }
    ks.sw.timestamps = filtered

    if len(ks.sw.timestamps) >= rpm {
        return false
    }

    ks.sw.timestamps = append(ks.sw.timestamps, now)
    return true
}

func (rl *RateLimiter) AllowTPM(key string, tpm int, tokenCount int) bool {
    if tpm <= 0 {
        return true
    }
    ks := rl.getOrCreate(key + ":tpm")
    ks.mu.Lock()
    defer ks.mu.Unlock()

    now := time.Now()
    windowStart := now.Add(-time.Minute)

    filteredTs := ks.sw.timestamps[:0]
    filteredTok := ks.sw.tokens[:0]
    totalTokens := 0
    for i, t := range ks.sw.timestamps {
        if t.After(windowStart) {
            filteredTs = append(filteredTs, t)
            filteredTok = append(filteredTok, ks.sw.tokens[i])
            totalTokens += ks.sw.tokens[i]
        }
    }
    ks.sw.timestamps = filteredTs
    ks.sw.tokens = filteredTok
    ks.sw.totalTokens = totalTokens

    if ks.sw.totalTokens+tokenCount > tpm {
        return false
    }

    ks.sw.timestamps = append(ks.sw.timestamps, now)
    ks.sw.tokens = append(ks.sw.tokens, tokenCount)
    ks.sw.totalTokens += tokenCount
    return true
}

func (rl *RateLimiter) RemainingRPM(key string, rpm int) int {
    if rpm <= 0 {
        return -1
    }
    ks := rl.getOrCreate(key + ":rpm")
    ks.mu.Lock()
    defer ks.mu.Unlock()

    now := time.Now()
    windowStart := now.Add(-time.Minute)

    count := 0
    for _, t := range ks.sw.timestamps {
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

// cleanupIdle removes idle key states to prevent memory leak
func (rl *RateLimiter) cleanupIdle(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            // R1: honor shutdown so cleanup exits and Shutdown joins it.
            return
        case <-ticker.C:
        }
        now := time.Now()
        rl.counters.Range(func(key, value interface{}) bool {
            ks := value.(*keyState)
            ks.mu.Lock()
            idle := now.Sub(ks.lastAccess) > 10*time.Minute
            ks.mu.Unlock()
            if idle {
                rl.counters.Delete(key)
            }
            return true
        })
    }
}

func RateLimit(cfg *config.RateLimitConfig, limiter *RateLimiter, tokFn func(ctx context.Context) int) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !cfg.Enabled {
                next.ServeHTTP(w, r)
                return
            }

            // 硬伤1 fix: read from Principal instead of scattered context keys
            p := PrincipalFromContext(r.Context())
            if p != nil && p.IsMaster {
                next.ServeHTTP(w, r)
                return
            }

            var keyCfg *config.AuthKeyConfig
            if p != nil {
                keyCfg = p.KeyConfig
            }
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
                if len(keyCfg.Key) >= 8 {
                    keyName = keyCfg.Key[:8]
                } else {
                    keyName = keyCfg.Key
                }
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
