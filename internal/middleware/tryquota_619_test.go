package middleware

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
    "github.com/fusion-gateway/fusion-gateway/internal/tryquota"
)

// #619: with try_quota enabled, a RECOGNIZED static API key (the auth chain
// already validated it and stamped Principal.KeyConfig) carrying no identity
// JWT was 401'd "identity token required" — with an empty master key there
// is no exempt path, so every local daemon LLM call broke. Machine callers
// must pass and be METERED under a stable key-derived sub ("key:<name>"),
// not silently unmetered. Unauthenticated requests still 401.

func tq619RoundTrip(t *testing.T, h http.Handler, keyName string) (*httptest.ResponseRecorder, *store.RequestLog) {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
    entry := &store.RequestLog{}
    req = WithRequestLogContext(req, entry)
    if keyName != "" {
        req = req.WithContext(ContextWithPrincipal(req.Context(), &Principal{
            KeyConfig: &config.AuthKeyConfig{Name: keyName},
        }))
    }
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    return rec, entry
}

func Test619_StaticKeyWithoutIdentityToken_MetersAndPasses(t *testing.T) {
    ran := false
    h := tqHandler(tqCfgSrc(true, 100000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        ran = true
        w.WriteHeader(http.StatusOK)
    })
    rec, entry := tq619RoundTrip(t, h, "agent-studio-daemon")
    if !ran {
        t.Fatalf("static-key caller must pass the gate, got %d %s", rec.Code, rec.Body.String())
    }
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d, want 200", rec.Code)
    }
    // Metering must still happen: the entry is stamped with the machine sub.
    if entry.TryQuotaSub != "key:agent-studio-daemon" {
        t.Fatalf("TryQuotaSub = %q, want key:agent-studio-daemon", entry.TryQuotaSub)
    }
    if entry.TryQuotaEst <= 0 {
        t.Fatalf("estimate must be stamped, got %d", entry.TryQuotaEst)
    }
}

func Test619_NoPrincipalNoToken_Still401(t *testing.T) {
    h := tqHandler(tqCfgSrc(true, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("unauthenticated caller must not pass")
    })
    rec, _ := tq619RoundTrip(t, h, "")
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
    detail, _ := tqDecodeQuota(t, rec.Body.Bytes())
    if detail != "identity token required" {
        t.Fatalf("detail = %q", detail)
    }
}

func Test619_BudgetStillEnforcedForKeySub(t *testing.T) {
    meter := tryquota.NewStore("")
    meter.Record("key:daemon", 1000, tryquota.CallDetail{}, time.Now())
    h := tqHandler(tqCfgSrc(true, 1000), meter, func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("over-budget key caller must not pass")
    })
    rec, _ := tq619RoundTrip(t, h, "daemon")
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("code = %d, want 429 over budget", rec.Code)
    }
}
