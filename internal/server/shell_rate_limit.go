package server

import (
    "log/slog"
    "net/http"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Issue #586: the shell chat plane enforced the tenant model policy but had
// NO usage controls — no RPM, no TPM, no daily quota. The /v1 key-plane
// RateLimit middleware deliberately skips shell-session principals (#216:
// they carry no API key), so the gate lives here, keyed by shell user +
// tenant. Reuses the shared middleware.RateLimiter windows so /v1 and shell
// traffic share one implementation; the daily token window is a separate
// in-memory counter (resets at UTC midnight and on restart — v1 contract,
// documented on the config field).

// shellDailyTokens is the in-memory per-user-per-day token ledger.
type shellDailyTokens struct {
    mu   sync.Mutex
    day  string
    used map[string]int
}

func newShellDailyTokens() *shellDailyTokens {
    return &shellDailyTokens{day: time.Now().UTC().Format("2006-01-02"), used: map[string]int{}}
}

// allow records spend under today's UTC window against dailyCap; false when
// the request would push the user past the cap (nothing recorded on denial —
// a rejected request must not burn quota). The cap is a per-call argument so
// a hot-reloaded config value takes effect without rebuilding the ledger.
func (d *shellDailyTokens) allow(key string, dailyCap, tokens int) bool {
    d.mu.Lock()
    defer d.mu.Unlock()
    today := time.Now().UTC().Format("2006-01-02")
    if today != d.day {
        d.day = today
        d.used = map[string]int{}
    }
    if dailyCap <= 0 {
        return true
    }
    if d.used[key]+tokens > dailyCap {
        return false
    }
    d.used[key] += tokens
    return true
}

// rollDay force-advances the window (test hook; production resets are driven
// by the real clock inside allow).
func (d *shellDailyTokens) rollDay(day string) {
    d.mu.Lock()
    defer d.mu.Unlock()
    d.day = day
    d.used = map[string]int{}
}

// dailyTokens returns the daily ledger, lazily constructing it. Production
// servers build it in New(); the lazy path exists because tests assemble
// Server literals directly. First-request construction racing is benign:
// both candidates are empty ledgers, exactly the post-restart state.
func (s *Server) dailyTokens() *shellDailyTokens {
    if s.shellDaily == nil {
        s.shellDaily = newShellDailyTokens()
    }
    return s.shellDaily
}

// enforceShellRateLimit applies web_shell.rate_limit to a shell-session chat
// request. Always true when the gate is disabled or the caller is not a shell
// session (/v1 key-plane traffic keeps its own middleware). On denial the
// response is written and false returned: 429 rate_limit_error for RPM/TPM,
// 402 insufficient_quota for the daily token cap (the OpenAI convention the
// identity key plane already surfaces to clients).
func (s *Server) enforceShellRateLimit(w http.ResponseWriter, r *http.Request, snap *config.ConfigSnapshot, inputTokens int) bool {
    claims := shellClaimsFrom(r.Context())
    if claims.Sub == "" || snap == nil || !snap.Config.Shell.RateLimit.Enabled {
        return true
    }
    rl := snap.Config.Shell.RateLimit
    tenant := claims.TenantID
    if tenant == "" {
        tenant = "local"
    }
    key := claims.Sub + "@" + tenant

    if !s.rateLimiter.AllowRPM("shell:"+key, rl.RPM) {
        slog.Warn("shell chat rate limited (RPM)", "user", claims.Sub, "tenant", tenant, "rpm", rl.RPM, "path", r.URL.Path)
        w.Header().Set("Retry-After", "1")
        w.Header().Set("X-RateLimit-Remaining", "0")
        http.Error(w, `{"error":{"message":"Shell chat rate limit exceeded (RPM)","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
        return false
    }
    if rl.TPM > 0 && inputTokens > 0 && !s.rateLimiter.AllowTPM("shell:"+key, rl.TPM, inputTokens) {
        // Mirror the key plane's B12 rule: a TPM rejection must not also burn
        // the RPM slot AllowRPM just took.
        s.rateLimiter.RollbackRPM("shell:"+key, rl.RPM)
        slog.Warn("shell chat rate limited (TPM)", "user", claims.Sub, "tenant", tenant, "tpm", rl.TPM, "tokens", inputTokens, "path", r.URL.Path)
        w.Header().Set("Retry-After", "1")
        w.Header().Set("X-RateLimit-Remaining", "0")
        http.Error(w, `{"error":{"message":"Shell chat rate limit exceeded (TPM)","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
        return false
    }
    if rl.DailyTokens > 0 && inputTokens > 0 {
        if !s.dailyTokens().allow(key, rl.DailyTokens, inputTokens) {
            slog.Warn("shell chat daily token quota exceeded", "user", claims.Sub, "tenant", tenant, "daily_tokens", rl.DailyTokens, "tokens", inputTokens, "path", r.URL.Path)
            http.Error(w, `{"error":{"message":"Shell chat daily token quota exceeded","type":"insufficient_quota"}}`, http.StatusPaymentRequired)
            return false
        }
    }
    return true
}
