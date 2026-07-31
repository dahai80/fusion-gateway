package middleware

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestAllowRPM(t *testing.T) {
    t.Parallel()

    t.Run("under_limit_returns_true", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        for i := 0; i < 5; i++ {
            if !rl.AllowRPM("test-key", 5) {
                t.Fatalf("request %d should be allowed, rpm=5", i+1)
            }
        }
    })

    t.Run("over_limit_returns_false", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        for i := 0; i < 3; i++ {
            if !rl.AllowRPM("test-key", 3) {
                t.Fatalf("request %d should be allowed", i+1)
            }
        }
        if rl.AllowRPM("test-key", 3) {
            t.Fatal("4th request should be denied, rpm=3")
        }
    })

    t.Run("rpm_le_zero_returns_true", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowRPM("key", 0) {
            t.Fatal("rpm=0 should always allow")
        }
        if !rl.AllowRPM("key", -1) {
            t.Fatal("rpm=-1 should always allow")
        }
    })

    t.Run("different_keys_independent", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowRPM("key-a", 1) {
            t.Fatal("key-a first request should be allowed")
        }
        if rl.AllowRPM("key-a", 1) {
            t.Fatal("key-a second request should be denied")
        }
        if !rl.AllowRPM("key-b", 1) {
            t.Fatal("key-b should be independent from key-a")
        }
    })
}

func TestAllowTPM(t *testing.T) {
    t.Parallel()

    t.Run("under_limit_returns_true", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowTPM("test-key", 100, 30) {
            t.Fatal("30 tokens under 100 tpm should be allowed")
        }
        if !rl.AllowTPM("test-key", 100, 50) {
            t.Fatal("80 total tokens under 100 tpm should be allowed")
        }
    })

    t.Run("over_limit_returns_false", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowTPM("test-key", 100, 60) {
            t.Fatal("60 tokens under 100 tpm should be allowed")
        }
        if rl.AllowTPM("test-key", 100, 50) {
            t.Fatal("110 total tokens over 100 tpm should be denied")
        }
    })

    t.Run("tpm_le_zero_returns_true", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowTPM("key", 0, 999) {
            t.Fatal("tpm=0 should always allow")
        }
        if !rl.AllowTPM("key", -1, 999) {
            t.Fatal("tpm=-1 should always allow")
        }
    })

    t.Run("exact_limit_allowed", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowTPM("test-key", 100, 100) {
            t.Fatal("exactly 100 tokens at 100 tpm should be allowed")
        }
    })

    t.Run("one_over_limit_denied", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if !rl.AllowTPM("test-key", 100, 50) {
            t.Fatal("50 tokens should be allowed")
        }
        if rl.AllowTPM("test-key", 100, 51) {
            t.Fatal("101 total tokens over 100 tpm should be denied")
        }
    })
}

func TestRemainingRPM(t *testing.T) {
    t.Parallel()

    t.Run("full_remaining_when_no_requests", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if got := rl.RemainingRPM("key", 10); got != 10 {
            t.Fatalf("expected 10 remaining, got %d", got)
        }
    })

    t.Run("decrements_after_requests", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        rl.AllowRPM("key", 10)
        rl.AllowRPM("key", 10)
        rl.AllowRPM("key", 10)
        if got := rl.RemainingRPM("key", 10); got != 7 {
            t.Fatalf("expected 7 remaining, got %d", got)
        }
    })

    t.Run("zero_when_exhausted", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        for i := 0; i < 5; i++ {
            rl.AllowRPM("key", 5)
        }
        if got := rl.RemainingRPM("key", 5); got != 0 {
            t.Fatalf("expected 0 remaining, got %d", got)
        }
    })

    t.Run("rpm_le_zero_returns_negative_one", func(t *testing.T) {
        t.Parallel()
        rl := NewRateLimiter()
        if got := rl.RemainingRPM("key", 0); got != -1 {
            t.Fatalf("expected -1 for rpm=0, got %d", got)
        }
        if got := rl.RemainingRPM("key", -5); got != -1 {
            t.Fatalf("expected -1 for rpm=-5, got %d", got)
        }
    })
}

func TestRateLimitMiddleware(t *testing.T) {
    okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })

    t.Run("master_key_bypasses", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            GlobalRPM:      1,
            KeyEnforcement: false,
        }
        middleware := RateLimit(cfg, rl, nil)

        for i := 0; i < 5; i++ {
            req := httptest.NewRequest(http.MethodGet, "/test", nil)
            ctx := context.WithValue(req.Context(), IsMasterKeyKey, true)
            req = req.WithContext(ctx)
            rec := httptest.NewRecorder()
            middleware(okHandler).ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                t.Fatalf("master key request %d should pass, got %d", i+1, rec.Code)
            }
        }
    })

    t.Run("disabled_config_passes_through", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:   false,
            GlobalRPM: 1,
        }
        middleware := RateLimit(cfg, rl, nil)

        for i := 0; i < 5; i++ {
            req := httptest.NewRequest(http.MethodGet, "/test", nil)
            rec := httptest.NewRecorder()
            middleware(okHandler).ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                t.Fatalf("disabled rate limit request %d should pass, got %d", i+1, rec.Code)
            }
        }
    })

    t.Run("key_enforcement_denies_without_key_config", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: true,
        }
        middleware := RateLimit(cfg, rl, nil)

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusTooManyRequests {
            t.Fatalf("expected 429 without key config when enforcement on, got %d", rec.Code)
        }
    })

    t.Run("key_enforcement_allows_with_key_config", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: true,
        }
        middleware := RateLimit(cfg, rl, nil)

        keyCfg := &config.AuthKeyConfig{
            Key:  "sk-test1234567890",
            Name: "test-user",
            RPM:  10,
            TPM:  1000,
        }

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx := context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200 with valid key config, got %d", rec.Code)
        }
    })

    t.Run("429_on_rpm_exceeded", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: false,
        }
        middleware := RateLimit(cfg, rl, nil)

        keyCfg := &config.AuthKeyConfig{
            Key:  "sk-test1234567890",
            Name: "limited-user",
            RPM:  2,
            TPM:  0,
        }

        for i := 0; i < 2; i++ {
            req := httptest.NewRequest(http.MethodGet, "/test", nil)
            ctx := context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
            req = req.WithContext(ctx)
            rec := httptest.NewRecorder()
            middleware(okHandler).ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                t.Fatalf("request %d should pass, got %d", i+1, rec.Code)
            }
        }

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx := context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusTooManyRequests {
            t.Fatalf("3rd request should get 429, got %d", rec.Code)
        }
        if got := rec.Header().Get("Retry-After"); got != "1" {
            t.Fatalf("expected Retry-After=1, got %q", got)
        }
        if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
            t.Fatalf("expected X-RateLimit-Remaining=0, got %q", got)
        }
    })

    t.Run("429_on_tpm_exceeded", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: false,
        }
        tokFn := func(ctx context.Context) int { return 60 }
        middleware := RateLimit(cfg, rl, tokFn)

        keyCfg := &config.AuthKeyConfig{
            Key:  "sk-tpm-test1234567",
            Name: "tpm-user",
            RPM:  100,
            TPM:  100,
        }

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx := context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("first request (60 tokens) should pass, got %d", rec.Code)
        }

        req = httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx = context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec = httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusTooManyRequests {
            t.Fatalf("second request (120 total tokens) should get 429, got %d", rec.Code)
        }
    })

    t.Run("no_key_config_no_enforcement_passes", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: false,
        }
        middleware := RateLimit(cfg, rl, nil)

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("no key config with enforcement off should pass, got %d", rec.Code)
        }
    })

    t.Run("remaining_header_set", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: false,
        }
        middleware := RateLimit(cfg, rl, nil)

        keyCfg := &config.AuthKeyConfig{
            Key:  "sk-remaining-test12",
            Name: "remaining-user",
            RPM:  10,
            TPM:  0,
        }

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx := context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        if got := rec.Header().Get("X-RateLimit-Remaining"); got != "9" {
            t.Fatalf("expected X-RateLimit-Remaining=9, got %q", got)
        }
    })

    t.Run("uses_key_prefix_when_name_empty", func(t *testing.T) {
        rl := NewRateLimiter()
        cfg := &config.RateLimitConfig{
            Enabled:        true,
            KeyEnforcement: false,
        }
        middleware := RateLimit(cfg, rl, nil)

        keyCfg := &config.AuthKeyConfig{
            Key: "sk-shortkey123456",
            RPM: 1,
            TPM: 0,
        }

        req := httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx := context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("first request should pass, got %d", rec.Code)
        }

        req = httptest.NewRequest(http.MethodGet, "/test", nil)
        ctx = context.WithValue(req.Context(), AuthKeyConfigKey, keyCfg)
        req = req.WithContext(ctx)
        rec = httptest.NewRecorder()
        middleware(okHandler).ServeHTTP(rec, req)
        if rec.Code != http.StatusTooManyRequests {
            t.Fatalf("second request should get 429 when name empty, got %d", rec.Code)
        }
    })
}
