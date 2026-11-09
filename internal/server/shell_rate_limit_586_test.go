package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// Issue #586: the shell chat plane enforced the tenant model policy but had
// NO usage controls — a logged-in shell user could drive inference without
// limit (70 parallel requests, all 200). The /v1 key plane's RPM/TPM
// middleware explicitly skips shell-session principals (#216), so the gate
// must live on the shell path itself, keyed by shell user + tenant.

func rateLimitTestServer(t *testing.T, rl config.ShellRateLimitConfig) *Server {
    t.Helper()
    s := newTestServer()
    s.cfg.Config.Shell.RateLimit = rl
    s.modelPolicies = modelpolicy.NewStore()
    if err := s.modelPolicies.SetPolicy("local", modelpolicy.Policy{
        UserAllowlist: []string{"local-small"},
    }); err != nil {
        t.Fatalf("SetPolicy: %v", err)
    }
    return s
}

func shellChatPost586(s *Server, tenant, user string) *httptest.ResponseRecorder {
    ctx := config.WithSnapshot(context.Background(), s.cfg)
    ctx = withShellClaims(ctx, shell.SessionClaims{Sub: user, TenantID: tenant})
    body := `{"model":"local-small","messages":[{"role":"user","content":"hi"}],"stream":false}`
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/chat/completions", strings.NewReader(body))
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)
    return rec
}

func TestShellChat_RPMExceeded429(t *testing.T) {
    s := rateLimitTestServer(t, config.ShellRateLimitConfig{Enabled: true, RPM: 2})
    // First two requests pass the rate gate (they proceed past it; whatever
    // happens downstream — routing error etc. — the status must not be 429).
    for i := 0; i < 2; i++ {
        if code := shellChatPost586(s, "local", "user-1").Code; code == http.StatusTooManyRequests {
            t.Fatalf("request %d below RPM cap must not be throttled", i+1)
        }
    }
    rec := shellChatPost586(s, "local", "user-1")
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("3rd request over rpm=2 must be 429 (#586), got %d: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "rate_limit_error") {
        t.Fatalf("429 body must use rate_limit_error type, got: %s", rec.Body.String())
    }
}

func TestShellChat_TPMExceeded429(t *testing.T) {
    s := rateLimitTestServer(t, config.ShellRateLimitConfig{Enabled: true, TPM: 5})
    // "hi" counts as 1 token — 8 requests x1 token exceeds tpm=5 mid-flight.
    sawThrottle := false
    for i := 0; i < 8; i++ {
        code := shellChatPost586(s, "local", "user-1").Code
        if code == http.StatusTooManyRequests {
            sawThrottle = true
            break
        }
    }
    if !sawThrottle {
        t.Fatalf("requests over tpm=5 must eventually 429 (#586)")
    }
}

func TestShellChat_DailyTokenQuota402(t *testing.T) {
    s := rateLimitTestServer(t, config.ShellRateLimitConfig{Enabled: true, DailyTokens: 3})
    // The tokenizer may count "hi" as more than one token, so assert the
    // contract (the cap eventually denies with 402) rather than an exact
    // request index.
    sawQuota := false
    for i := 0; i < 8; i++ {
        code := shellChatPost586(s, "local", "user-1").Code
        if code == http.StatusPaymentRequired {
            sawQuota = true
            break
        }
    }
    if !sawQuota {
        t.Fatalf("requests over daily_tokens=3 must eventually 402 (#586)")
    }
    rec := shellChatPost586(s, "local", "user-1")
    if rec.Code != http.StatusPaymentRequired {
        t.Fatalf("denial must persist within the day, got %d: %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "insufficient_quota") {
        t.Fatalf("402 body must use insufficient_quota type (OpenAI convention), got: %s", rec.Body.String())
    }
}

func TestShellChat_RateLimitDisabledUnlimited(t *testing.T) {
    s := rateLimitTestServer(t, config.ShellRateLimitConfig{Enabled: false, RPM: 1})
    for i := 0; i < 5; i++ {
        if code := shellChatPost586(s, "local", "user-1").Code; code == http.StatusTooManyRequests || code == http.StatusPaymentRequired {
            t.Fatalf("disabled rate limit must never throttle, request %d got %d", i+1, code)
        }
    }
}

func TestShellChat_RateLimitPerUserIsolated(t *testing.T) {
    s := rateLimitTestServer(t, config.ShellRateLimitConfig{Enabled: true, RPM: 1})
    // user-1 burns their single slot; user-2 must still pass the gate.
    shellChatPost586(s, "local", "user-1")
    if code := shellChatPost586(s, "local", "user-2").Code; code == http.StatusTooManyRequests {
        t.Fatalf("per-user isolation violated: user-2 throttled by user-1's spend")
    }
    if code := shellChatPost586(s, "local", "user-1").Code; code != http.StatusTooManyRequests {
        t.Fatalf("user-1 over rpm=1 must be 429, got %d", code)
    }
}

func TestShellRateLimit_DailyTokensResetPerDay(t *testing.T) {
    rl := newShellDailyTokens()
    key := "user-1@local"
    if !rl.allow(key, 2, 2) {
        t.Fatal("fresh window must allow the first spend")
    }
    if rl.allow(key, 2, 1) {
        t.Fatal("spend over the daily cap must be denied")
    }
    rl.rollDay("2099-01-02")
    if !rl.allow(key, 2, 1) {
        t.Fatal("new day must start with a fresh window")
    }
}
