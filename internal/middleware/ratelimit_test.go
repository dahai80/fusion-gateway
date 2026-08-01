package middleware

import (
    "context"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewRateLimiter(t *testing.T) {
    slog.Info("test NewRateLimiter")
    rl := NewRateLimiter()
    if rl == nil {
        t.Fatal("expected rate limiter")
    }
}

func TestRateLimiter_AllowRPM(t *testing.T) {
    slog.Info("test RateLimiter_AllowRPM")
    rl := NewRateLimiter()
    if !rl.AllowRPM("key1", 5) {
        t.Error("first request should be allowed")
    }
}

func TestRateLimiter_AllowRPM_Exceeded(t *testing.T) {
    slog.Info("test RateLimiter_AllowRPM_Exceeded")
    rl := NewRateLimiter()
    for i := 0; i < 5; i++ {
        rl.AllowRPM("key1", 5)
    }
    if rl.AllowRPM("key1", 5) {
        t.Error("6th request should be denied")
    }
}

func TestRateLimiter_AllowRPM_Zero(t *testing.T) {
    slog.Info("test RateLimiter_AllowRPM_Zero")
    rl := NewRateLimiter()
    if !rl.AllowRPM("key1", 0) {
        t.Error("zero RPM should always allow")
    }
}

func TestRateLimiter_AllowRPM_DifferentKeys(t *testing.T) {
    slog.Info("test RateLimiter_AllowRPM_DifferentKeys")
    rl := NewRateLimiter()
    for i := 0; i < 5; i++ {
        rl.AllowRPM("key1", 5)
    }
    if !rl.AllowRPM("key2", 5) {
        t.Error("different key should be allowed")
    }
}

func TestRateLimiter_AllowTPM(t *testing.T) {
    slog.Info("test RateLimiter_AllowTPM")
    rl := NewRateLimiter()
    if !rl.AllowTPM("key1", 1000, 500) {
        t.Error("first request should be allowed")
    }
}

func TestRateLimiter_AllowTPM_Exceeded(t *testing.T) {
    slog.Info("test RateLimiter_AllowTPM_Exceeded")
    rl := NewRateLimiter()
    rl.AllowTPM("key1", 1000, 600)
    if rl.AllowTPM("key1", 1000, 500) {
        t.Error("should deny when TPM exceeded")
    }
}

func TestRateLimiter_AllowTPM_Zero(t *testing.T) {
    slog.Info("test RateLimiter_AllowTPM_Zero")
    rl := NewRateLimiter()
    if !rl.AllowTPM("key1", 0, 9999) {
        t.Error("zero TPM should always allow")
    }
}

func TestRateLimiter_RemainingRPM(t *testing.T) {
    slog.Info("test RateLimiter_RemainingRPM")
    rl := NewRateLimiter()
    rl.AllowRPM("key1", 10)
    remaining := rl.RemainingRPM("key1", 10)
    if remaining != 9 {
        t.Errorf("expected 9 remaining, got %d", remaining)
    }
}

func TestRateLimiter_RemainingRPM_Zero(t *testing.T) {
    slog.Info("test RateLimiter_RemainingRPM_Zero")
    rl := NewRateLimiter()
    remaining := rl.RemainingRPM("key1", 0)
    if remaining != -1 {
        t.Errorf("expected -1 for zero RPM, got %d", remaining)
    }
}

func TestRateLimit_Disabled(t *testing.T) {
    slog.Info("test RateLimit_Disabled")
    cfg := &config.RateLimitConfig{Enabled: false}
    rl := NewRateLimiter()
    var called bool
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should call next when disabled")
    }
}

func TestRateLimit_MasterKey(t *testing.T) {
    slog.Info("test RateLimit_MasterKey")
    cfg := &config.RateLimitConfig{Enabled: true}
    rl := NewRateLimiter()
    var called bool
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    p := &Principal{IsMaster: true}
    ctx := ContextWithPrincipal(req.Context(), p)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("master key should bypass rate limit")
    }
}

func TestRateLimit_NoKeyConfig_NoEnforcement(t *testing.T) {
    slog.Info("test RateLimit_NoKeyConfig_NoEnforcement")
    cfg := &config.RateLimitConfig{Enabled: true, KeyEnforcement: false}
    rl := NewRateLimiter()
    var called bool
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should allow without key config when enforcement disabled")
    }
}

func TestRateLimit_NoKeyConfig_Enforcement(t *testing.T) {
    slog.Info("test RateLimit_NoKeyConfig_Enforcement")
    cfg := &config.RateLimitConfig{Enabled: true, KeyEnforcement: true}
    rl := NewRateLimiter()
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("expected 429, got %d", rec.Code)
    }
}

func TestRateLimit_RPMExceeded(t *testing.T) {
    slog.Info("test RateLimit_RPMExceeded")
    cfg := &config.RateLimitConfig{Enabled: true}
    rl := NewRateLimiter()
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "limited", RPM: 2}}
    for i := 0; i < 2; i++ {
        rl.AllowRPM("limited", 2)
    }
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), p)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("expected 429, got %d", rec.Code)
    }
}

func TestRateLimit_TPMExceeded(t *testing.T) {
    slog.Info("test RateLimit_TPMExceeded")
    cfg := &config.RateLimitConfig{Enabled: true}
    rl := NewRateLimiter()
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "tpm-limited", RPM: 100, TPM: 100}}
    rl.AllowTPM("tpm-limited", 100, 100)
    tokFn := func(ctx context.Context) int { return 50 }
    handler := RateLimit(cfg, rl, tokFn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), p)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("expected 429 for TPM, got %d", rec.Code)
    }
}

func TestRateLimit_Success(t *testing.T) {
    slog.Info("test RateLimit_Success")
    cfg := &config.RateLimitConfig{Enabled: true}
    rl := NewRateLimiter()
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "ok", RPM: 100, TPM: 10000}}
    var called bool
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), p)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should call next handler")
    }
    if rec.Header().Get("X-RateLimit-Remaining") == "" {
        t.Error("should set remaining header")
    }
}

func TestRateLimit_KeyNameFallback(t *testing.T) {
    slog.Info("test RateLimit_KeyNameFallback")
    cfg := &config.RateLimitConfig{Enabled: true}
    rl := NewRateLimiter()
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Key: "abcdefghijklmnopqrstuvwxyz", RPM: 100, TPM: 10000}}
    var called bool
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), p)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should call next handler with key name fallback")
    }
}

func TestFormatInt(t *testing.T) {
    cases := []struct {
        input    int
        expected string
    }{
        {0, "0"},
        {1, "1"},
        {123, "123"},
        {-1, "-1"},
        {-42, "-42"},
    }
    for _, tc := range cases {
        result := formatInt(tc.input)
        if result != tc.expected {
            t.Errorf("formatInt(%d) = %s, expected %s", tc.input, result, tc.expected)
        }
    }
}

func TestCleanupIdle_RemovesExpired(t *testing.T) {
    slog.Info("test CleanupIdle_RemovesExpired")
    rl := NewRateLimiter()
    ks := rl.getOrCreate("idle-key")
    ks.mu.Lock()
    ks.lastAccess = time.Now().Add(-15 * time.Minute)
    ks.mu.Unlock()

    ks2 := rl.getOrCreate("active-key")
    ks2.mu.Lock()
    ks2.lastAccess = time.Now()
    ks2.mu.Unlock()

    rl.counters.Range(func(key, value interface{}) bool {
        ks := value.(*keyState)
        ks.mu.Lock()
        idle := time.Since(ks.lastAccess) > 10*time.Minute
        ks.mu.Unlock()
        if idle {
            rl.counters.Delete(key)
        }
        return true
    })

    _, idleExists := rl.counters.Load("idle-key")
    _, activeExists := rl.counters.Load("active-key")
    if idleExists {
        t.Error("idle key should have been deleted")
    }
    if !activeExists {
        t.Error("active key should still exist")
    }
}

func TestCleanupIdle_KeepsActive(t *testing.T) {
    slog.Info("test CleanupIdle_KeepsActive")
    rl := NewRateLimiter()
    ks := rl.getOrCreate("recent-key")
    ks.mu.Lock()
    ks.lastAccess = time.Now()
    ks.mu.Unlock()

    rl.counters.Range(func(key, value interface{}) bool {
        ks := value.(*keyState)
        ks.mu.Lock()
        idle := time.Since(ks.lastAccess) > 10*time.Minute
        ks.mu.Unlock()
        if idle {
            rl.counters.Delete(key)
        }
        return true
    })

    _, exists := rl.counters.Load("recent-key")
    if !exists {
        t.Error("recent key should still exist")
    }
}

func TestRateLimit_TokenCountZero(t *testing.T) {
    slog.Info("test RateLimit_TokenCountZero")
    cfg := &config.RateLimitConfig{Enabled: true}
    rl := NewRateLimiter()
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "toktest", RPM: 100, TPM: 100}}
    tokFn := func(ctx context.Context) int { return 0 }
    var called bool
    handler := RateLimit(cfg, rl, tokFn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := ContextWithPrincipal(req.Context(), p)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("should call next when token count is zero")
    }
}
