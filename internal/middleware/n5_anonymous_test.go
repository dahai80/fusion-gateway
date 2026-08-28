package middleware

// N5 (audit) test: the anonymous (no API key) path through RateLimit was
// fully unlimited. With anonymous_rpm set, requests from one IP past the cap
// must get 429; requests with a different IP must still pass; authenticated
// keys must be unaffected.

import (
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestN5_AnonymousRPMExceeded: 3 requests from one IP with anonymous_rpm=2 →
// the 3rd gets 429.
func TestN5_AnonymousRPMExceeded(t *testing.T) {
    cfg := &config.RateLimitConfig{Enabled: true, KeyEnforcement: false, AnonymousRPM: 2}
    rl := NewRateLimiter()
    var passed int
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        passed++
        w.WriteHeader(http.StatusOK)
    }))
    for i := 0; i < 3; i++ {
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        req.RemoteAddr = "10.0.0.5:1234"
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        if i < 2 && rec.Code != http.StatusOK {
            t.Fatalf("req %d: want 200 (under cap), got %d", i, rec.Code)
        }
        if i == 2 && rec.Code != http.StatusTooManyRequests {
            t.Fatalf("req %d (over cap): want 429, got %d", i, rec.Code)
        }
    }
    if passed != 2 {
        t.Errorf("N5: handler invoked %d times, want 2 (cap=2, 3rd rejected)", passed)
    }
}

// TestN5_AnonymousDifferentIPsIndependent: two IPs each under their own cap
// must not share a counter — a per-IP cap must not become a global cap.
func TestN5_AnonymousDifferentIPsIndependent(t *testing.T) {
    cfg := &config.RateLimitConfig{Enabled: true, KeyEnforcement: false, AnonymousRPM: 2}
    rl := NewRateLimiter()
    var passed int
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        passed++
        w.WriteHeader(http.StatusOK)
    }))
    for _, ip := range []string{"10.0.0.1:1", "10.0.0.2:1", "10.0.0.1:1", "10.0.0.2:1"} {
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        req.RemoteAddr = ip
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("ip %s: want 200 (each IP under its own cap), got %d", ip, rec.Code)
        }
    }
    if passed != 4 {
        t.Errorf("N5: handler invoked %d times, want 4 (two IPs x 2 each)", passed)
    }
}

// TestN5_AnonymousZeroDisablesCap: anonymous_rpm=0 preserves the unlimited
// back-compat behavior — the anonymous path passes through with no 429.
func TestN5_AnonymousZeroDisablesCap(t *testing.T) {
    cfg := &config.RateLimitConfig{Enabled: true, KeyEnforcement: false, AnonymousRPM: 0}
    rl := NewRateLimiter()
    var passed int
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        passed++
        w.WriteHeader(http.StatusOK)
    }))
    for i := 0; i < 10; i++ {
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        req.RemoteAddr = "10.0.0.9:1"
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("req %d: anonymous_rpm=0 must be unlimited, got %d", i, rec.Code)
        }
    }
    if passed != 10 {
        t.Errorf("N5: anonymous_rpm=0 passed %d/10", passed)
    }
}

// TestN5_AuthenticatedKeyUnaffected: an authenticated request (principal with
// a KeyConfig) must take the keyed path, NOT the anonymous per-IP path — the
// anonymous cap must not bleed into authenticated traffic.
func TestN5_AuthenticatedKeyUnaffected(t *testing.T) {
    cfg := &config.RateLimitConfig{Enabled: true, KeyEnforcement: false, AnonymousRPM: 1}
    rl := NewRateLimiter()
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "paid", RPM: 100}}
    var passed int
    handler := RateLimit(cfg, rl, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        passed++
        w.WriteHeader(http.StatusOK)
    }))
    for i := 0; i < 5; i++ {
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        req.RemoteAddr = "10.0.0.7:1"
        ctx := ContextWithPrincipal(req.Context(), p)
        req = req.WithContext(ctx)
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("auth req %d: must use keyed RPM (100), not anonymous (1); got %d", i, rec.Code)
        }
    }
    if passed != 5 {
        t.Errorf("N5: authenticated path passed %d/5", passed)
    }
}

func init() {
    slog.Default()
}
