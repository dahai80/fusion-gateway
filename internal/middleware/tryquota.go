package middleware

// #514: try-quota gate middleware. Runs LAST in the chain (after auth/RBAC/
// budget/rate-limit). When try_quota.enabled, every product LLM call must
// carry a valid fusion-identity HS256 access token in the
// X-Fusion-Identity-Token header;
// the metering key is the token's `sub`. Pre-check: used_today(sub) +
// tokenizer estimate vs the user's daily budget → 429 over budget. The sub
// + estimate are stamped onto the request-log entry so the finalize path
// (outer withMiddleware defer, same entry-pointer pattern as
// StampAPIKeyName) can account real usage without any ctx-propagation trap.

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
    "github.com/fusion-gateway/fusion-gateway/internal/tryquota"
)

// TryQuotaConfigSource resolves the current try_quota config (hot-reload
// safe — reads the snapshot, never a stale copy).
type TryQuotaConfigSource func() (enabled bool, budget int, secret string, estimateFallback int)

// exactGatePaths are the single-endpoint LLM routes; prefixGatePaths cover
// sub-path families (/v1/audio/transcriptions, /v1/audio/translations, ...).
var (
    exactGatePaths  = []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/messages", "/v1/rerank"}
    prefixGatePaths = []string{"/v1/images/", "/v1/audio/"}
)

// gateApplies scopes the gate to product LLM call paths only. The gate must
// NOT run on admin/ops surfaces: /admin/api/* requests authenticate with the
// admin JWT and carry no identity token — gating them made every admin
// endpoint 401 the moment try_quota.enabled flipped on (caught in live
// verification). /v1/models and other discovery routes stay ungated too —
// the quota is a usage limit, not an access wall for metadata.
func gateApplies(path string) bool {
    for _, p := range exactGatePaths {
        if path == p {
            return true
        }
    }
    for _, p := range prefixGatePaths {
        if strings.HasPrefix(path, p) {
            return true
        }
    }
    return false
}

// TryQuota builds the gate middleware. store may be nil (metering disabled
// server-side; the gate itself still runs when enabled).
func TryQuota(cfgSrc TryQuotaConfigSource, meter *tryquota.Store, estimate func(r *http.Request) int) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !gateApplies(r.URL.Path) {
                next.ServeHTTP(w, r)
                return
            }
            enabled, budget, secret, estFallback := cfgSrc()
            if !enabled || secret == "" {
                next.ServeHTTP(w, r)
                return
            }
            // Admin/master-key principals are operational traffic, not
            // product users — never metered, never gated.
            if IsMasterKey(r.Context()) {
                next.ServeHTTP(w, r)
                return
            }
            token := identityToken(r)
            if token == "" {
                writeTryQuotaError(w, http.StatusUnauthorized, "identity token required",
                    0, budget, 0)
                return
            }
            sub, err := tryquota.VerifyToken(token, secret, time.Now())
            if err != nil {
                slog.Info("try-quota: identity token rejected", "error", err)
                writeTryQuotaError(w, http.StatusUnauthorized, "invalid identity token",
                    0, budget, 0)
                return
            }
            if b, ok := meter.BudgetOverride(sub); ok {
                budget = b
            }
            est := estFallback
            if estimate != nil {
                if e := estimate(r); e > 0 {
                    est = e
                }
            }
            used := meter.UsedToday(sub, time.Now())
            if used+est > budget {
                slog.Info("try-quota: daily budget exceeded", "sub", sub, "used", used, "estimate", est, "budget", budget)
                writeTryQuotaError(w, http.StatusTooManyRequests, "daily token quota exceeded",
                    used, budget, est)
                return
            }
            // Stamp the metering key + estimate onto the request-log entry
            // for the finalize-time accounting (requestlog.go).
            if entry := GetRequestLog(r.Context()); entry != nil {
                entry.TryQuotaSub = sub
                entry.TryQuotaEst = est
            }
            next.ServeHTTP(w, r)
        })
    }
}

// identityToken extracts the fusion-identity credential from the dedicated
// X-Fusion-Identity-Token header. It deliberately does NOT read the
// Authorization header: extractAPIKey (auth chain) treats "Authorization:
// Bearer <token>" as the gateway API key with higher precedence than
// X-API-Key, so an identity JWT sent as Bearer would be hashed as an API key
// and rejected with 401 before this middleware ever runs. The dedicated
// header keeps the credential planes separated (gateway key vs identity
// subject); VerifyToken is still the only authority on its value.
func identityToken(r *http.Request) string {
    return strings.TrimSpace(r.Header.Get("X-Fusion-Identity-Token"))
}

// writeTryQuotaError emits the quota error shape used by the fusion-site
// routes ({"detail": ..., "quota": {...}}).
func writeTryQuotaError(w http.ResponseWriter, status int, detail string, used, budget, estimate int) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "detail": detail,
        "quota": map[string]interface{}{
            "used":     used,
            "budget":   budget,
            "estimate": estimate,
        },
    })
}

// TryQuotaFinalize accounts one finished request: real upstream usage when
// the entry carries token counts, the pre-check estimate as the fallback
// (failed/canceled calls still consumed admission). Called from the outer
// withMiddleware defer AFTER FinalizeAndAppendLog.
func TryQuotaFinalize(entry *store.RequestLog, meter *tryquota.Store) {
    if meter == nil || entry == nil || entry.TryQuotaSub == "" {
        return
    }
    tokens := entry.TotalTokens
    if tokens <= 0 {
        tokens = entry.TryQuotaEst
    }
    product := "web"
    if entry.Scenario != "" {
        product = entry.Scenario
    }
    meter.Record(entry.TryQuotaSub, tokens, tryquota.CallDetail{
        Time:    time.Now(),
        Model:   entry.Model,
        Backend: entry.ChannelType,
        Product: product,
        Tokens:  tokens,
    }, time.Now())
    if entry.TryQuotaSub != "" {
        slog.Debug("try-quota: usage accounted", "sub", entry.TryQuotaSub, "tokens", tokens, "model", entry.Model)
    }
}
