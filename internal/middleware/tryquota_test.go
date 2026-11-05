package middleware

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
    "github.com/fusion-gateway/fusion-gateway/internal/tryquota"
    "github.com/golang-jwt/jwt/v5"
)

const tqTestSecret = "test-secret"

func tqMint(t *testing.T, sub string, exp time.Time) string {
    t.Helper()
    tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
        "sub": sub,
        "exp": exp.Unix(),
    })
    str, err := tok.SignedString([]byte(tqTestSecret))
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    return str
}

func tqCfgSrc(enabled bool, budget int) TryQuotaConfigSource {
    return func() (bool, int, string, int) {
        return enabled, budget, tqTestSecret, 1024
    }
}

// tqHandler builds the middleware with a per-request-entry requestlog ctx,
// mirroring the real chain (withMiddleware creates the entry before auth).
func tqHandler(cfgSrc TryQuotaConfigSource, meter *tryquota.Store, next http.HandlerFunc) http.Handler {
    return TryQuota(cfgSrc, meter, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        next(w, r)
    }))
}

func tqRoundTrip(t *testing.T, h http.Handler, token string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
    entry := &store.RequestLog{}
    req = WithRequestLogContext(req, entry)
    if token != "" {
        req.Header.Set("X-Fusion-Identity-Token", token)
    }
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    return rec
}

func tqDecodeQuota(t *testing.T, body []byte) (string, map[string]interface{}) {
    t.Helper()
    var out map[string]interface{}
    if err := json.Unmarshal(body, &out); err != nil {
        t.Fatalf("body not json: %v (%q)", err, body)
    }
    detail, _ := out["detail"].(string)
    quota, _ := out["quota"].(map[string]interface{})
    if quota == nil {
        t.Fatalf("missing quota object in %q", body)
    }
    return detail, quota
}

func TestGateAppliesScoping(t *testing.T) {
    yes := []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/messages", "/v1/rerank", "/v1/images/generations", "/v1/audio/transcriptions"}
    no := []string{"/v1/models", "/admin/api/try-quota/usage", "/admin/api/keys", "/healthz", "/v1/stream/abc/events", "/gateway/v1/browser/nodes", "/v1/moderations", "/v1/batches"}
    for _, p := range yes {
        if !gateApplies(p) {
            t.Fatalf("gateApplies(%q) = false, want true", p)
        }
    }
    for _, p := range no {
        if gateApplies(p) {
            t.Fatalf("gateApplies(%q) = true, want false", p)
        }
    }
}

// Live-verification regression: enabling the gate must not 401 the admin
// surface (admin JWT carries no identity token).
func TestTryQuotaAdminPathUntouched(t *testing.T) {
    called := false
    h := tqHandler(tqCfgSrc(true, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        called = true
    })
    req := httptest.NewRequest(http.MethodGet, "/admin/api/try-quota/usage", nil)
    h.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatalf("admin path must bypass the identity gate")
    }
}

func TestTryQuotaDisabledPassesThrough(t *testing.T) {
    called := false
    h := tqHandler(tqCfgSrc(false, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        called = true
    })
    tqRoundTrip(t, h, "")
    if !called {
        t.Fatalf("disabled gate must pass through")
    }
}

func TestTryQuotaEmptySecretPassesThrough(t *testing.T) {
    called := false
    h := TryQuota(func() (bool, int, string, int) { return true, 1000, "", 1024 }, nil, nil)(
        http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
    tqRoundTrip(t, h, "")
    if !called {
        t.Fatalf("empty secret must pass through (metering not provisioned)")
    }
}

func TestTryQuotaMissingToken401(t *testing.T) {
    h := tqHandler(tqCfgSrc(true, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("handler must not run")
    })
    rec := tqRoundTrip(t, h, "")
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
    detail, _ := tqDecodeQuota(t, rec.Body.Bytes())
    if detail != "identity token required" {
        t.Fatalf("detail = %q", detail)
    }
}

func TestTryQuotaInvalidToken401(t *testing.T) {
    h := tqHandler(tqCfgSrc(true, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("handler must not run")
    })
    rec := tqRoundTrip(t, h, "not-a-jwt")
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
}

func TestTryQuotaExpiredToken401(t *testing.T) {
    h := tqHandler(tqCfgSrc(true, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("handler must not run")
    })
    rec := tqRoundTrip(t, h, tqMint(t, "u1", time.Now().Add(-time.Hour)))
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
}

func TestTryQuotaUnderBudgetPassesAndStamps(t *testing.T) {
    meter := tryquota.NewStore("")
    var gotEntry *store.RequestLog
    h := tqHandler(tqCfgSrc(true, 5000), meter, func(w http.ResponseWriter, r *http.Request) {
        gotEntry = GetRequestLog(r.Context())
    })
    rec := tqRoundTrip(t, h, tqMint(t, "u1", time.Now().Add(time.Hour)))
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d, want 200", rec.Code)
    }
    if gotEntry == nil || gotEntry.TryQuotaSub != "u1" {
        t.Fatalf("entry not stamped: %+v", gotEntry)
    }
    if gotEntry.TryQuotaEst != 1024 {
        t.Fatalf("est = %d, want fallback 1024", gotEntry.TryQuotaEst)
    }
}

func TestTryQuotaOverBudget429(t *testing.T) {
    meter := tryquota.NewStore("")
    meter.Record("u1", 900, tryquota.CallDetail{}, time.Now())
    h := tqHandler(tqCfgSrc(true, 1000), meter, func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("handler must not run")
    })
    rec := tqRoundTrip(t, h, tqMint(t, "u1", time.Now().Add(time.Hour)))
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("code = %d, want 429", rec.Code)
    }
    detail, quota := tqDecodeQuota(t, rec.Body.Bytes())
    if detail != "daily token quota exceeded" {
        t.Fatalf("detail = %q", detail)
    }
    if quota["used"].(float64) != 900 || quota["budget"].(float64) != 1000 {
        t.Fatalf("quota = %+v", quota)
    }
}

func TestTryQuotaBudgetOverrideWins(t *testing.T) {
    meter := tryquota.NewStore("")
    meter.Record("u1", 5, tryquota.CallDetail{}, time.Now())
    meter.SetBudgetOverride("u1", 10)
    h := tqHandler(tqCfgSrc(true, 100000), meter, func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("override budget 10 must reject (5 used + 1024 est > 10)")
    })
    rec := tqRoundTrip(t, h, tqMint(t, "u1", time.Now().Add(time.Hour)))
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("code = %d, want 429", rec.Code)
    }
}

func TestTryQuotaMasterKeyBypass(t *testing.T) {
    meter := tryquota.NewStore("")
    called := false
    h := tqHandler(tqCfgSrc(true, 0), meter, func(w http.ResponseWriter, r *http.Request) {
        called = true
    })
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ContextWithPrincipal(req.Context(), &Principal{IsMaster: true}))
    h.ServeHTTP(httptest.NewRecorder(), req)
    if !called {
        t.Fatalf("master key must bypass gate")
    }
}

func TestTryQuotaFinalizeUsesRealTokens(t *testing.T) {
    meter := tryquota.NewStore("")
    now := time.Now()
    entry := &store.RequestLog{
        TryQuotaSub: "u1",
        TryQuotaEst: 100,
        TotalTokens: 777,
        Model:       "glm-5",
        Scenario:    "question-gen",
    }
    TryQuotaFinalize(entry, meter)
    if got := meter.UsedToday("u1", now); got != 777 {
        t.Fatalf("used = %d, want 777 (real tokens beat estimate)", got)
    }
}

func TestTryQuotaFinalizeEstimateFallback(t *testing.T) {
    meter := tryquota.NewStore("")
    now := time.Now()
    entry := &store.RequestLog{TryQuotaSub: "u1", TryQuotaEst: 100}
    TryQuotaFinalize(entry, meter)
    if got := meter.UsedToday("u1", now); got != 100 {
        t.Fatalf("used = %d, want 100 (estimate fallback)", got)
    }
}

func TestTryQuotaFinalizeNoopGuards(t *testing.T) {
    meter := tryquota.NewStore("")
    // Nil meter, nil entry, empty sub — all no-ops, no panic.
    TryQuotaFinalize(nil, meter)
    TryQuotaFinalize(&store.RequestLog{}, meter)
    TryQuotaFinalize(&store.RequestLog{TryQuotaSub: "u1"}, nil)
    if got := meter.UsedToday("u1", time.Now()); got != 0 {
        t.Fatalf("empty-sub entry must not meter, got %d", got)
    }
}

func TestTryQuotaAuthorizationHeaderIgnored(t *testing.T) {
    h := tqHandler(tqCfgSrc(true, 1000), tryquota.NewStore(""), func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("handler must not run")
    })
    // The Authorization header is the gateway-key plane (extractAPIKey reads
    // Bearer first) — it must never satisfy the identity gate, even when it
    // carries a structurally valid JWT.
    tok := tqMint(t, "u1", time.Now().Add(time.Hour))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req.Header.Set("Authorization", "Bearer "+tok)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401 (Authorization is not the identity plane)", rec.Code)
    }
}
