package middleware

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "math/big"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
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
    return func() TryQuotaSettings {
        return TryQuotaSettings{
            Enabled:          enabled,
            Budget:           budget,
            Algorithm:        "hs256",
            Secret:           tqTestSecret,
            EstimateFallback: 1024,
        }
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
    // Empty algorithm + empty secret = not provisioned: pass through. (An
    // empty secret now resolves to rs256 mode, but with no token present the
    // gate still 401s — the pass-through applies to the hs256-without-secret
    // profile only, asserted via the explicit algorithm below.)
    h := TryQuota(func() TryQuotaSettings {
        return TryQuotaSettings{Enabled: true, Budget: 1000, Algorithm: "hs256", EstimateFallback: 1024}
    }, nil, nil)(
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

// #528: a failed upstream call (status >= 400) produced no provider usage —
// the admission estimate must NOT be charged, or metered spend diverges from
// actual provider billing on every failure.
func TestTryQuotaFinalizeSkipsEstimateOnFailure(t *testing.T) {
    meter := tryquota.NewStore("")
    for _, status := range []int{400, 401, 429, 500, 502} {
        entry := &store.RequestLog{TryQuotaSub: "u1", TryQuotaEst: 100, StatusCode: status}
        TryQuotaFinalize(entry, meter)
    }
    if got := meter.UsedToday("u1", time.Now()); got != 0 {
        t.Fatalf("failed calls must not charge the estimate, got %d", got)
    }
}

// #528: real upstream usage is still metered even when the call failed — the
// provider billed for those tokens.
func TestTryQuotaFinalizeRealTokensOnFailureStillMetered(t *testing.T) {
    meter := tryquota.NewStore("")
    entry := &store.RequestLog{TryQuotaSub: "u1", TryQuotaEst: 100, TotalTokens: 55, StatusCode: 400}
    TryQuotaFinalize(entry, meter)
    if got := meter.UsedToday("u1", time.Now()); got != 55 {
        t.Fatalf("used = %d, want 55 (real usage billed despite failure)", got)
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

// --- #524: RS256 / JWKS mode ---

// tqMintRS256 signs an RS256 token with a kid header (identity-style).
func tqMintRS256(t *testing.T, key *rsa.PrivateKey, kid string, sub string, exp time.Time) string {
    t.Helper()
    tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": sub, "exp": exp.Unix()})
    tok.Header["kid"] = kid
    str, err := tok.SignedString(key)
    if err != nil {
        t.Fatalf("mint RS256: %v", err)
    }
    return str
}

func TestTryQuotaRS256Mode(t *testing.T) {
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        t.Fatalf("rsa keygen: %v", err)
    }
    jwk := map[string]interface{}{
        "kty": "RSA",
        "use": "sig",
        "alg": "RS256",
        "kid": "kid-live",
        "n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
        "e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
    }
    var mu sync.Mutex
    keys := []map[string]interface{}{jwk}
    jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        mu.Lock()
        defer mu.Unlock()
        _ = json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})
    }))
    defer jwksSrv.Close()

    meter := tryquota.NewStore("")
    src := func() TryQuotaSettings {
        return TryQuotaSettings{
            Enabled:          true,
            Budget:           100000,
            Algorithm:        "rs256",
            IdentityURL:      jwksSrv.URL,
            EstimateFallback: 1024,
        }
    }
    ran := false
    h := tqHandler(src, meter, func(w http.ResponseWriter, r *http.Request) { ran = true })

    // Valid RS256 identity token: gate passes, handler runs.
    tok := tqMintRS256(t, key, "kid-live", "user-rs", time.Now().Add(time.Hour))
    rec := tqRoundTrip(t, h, tok)
    if rec.Code != http.StatusOK {
        t.Fatalf("RS256 token code = %d, want 200, body %s", rec.Code, rec.Body.String())
    }
    if !ran {
        t.Fatalf("handler must run for a valid RS256 token")
    }

    // Alg-confusion: HS256 token in rs256 mode must 401.
    hsTok := tqMint(t, "user-rs", time.Now().Add(time.Hour))
    rec = tqRoundTrip(t, h, hsTok)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("HS256 token in rs256 mode code = %d, want 401", rec.Code)
    }

    // Quota enforcement still keys on the RS256 sub.
    meter.Record("user-rs", 100001, tryquota.CallDetail{}, time.Now())
    rec = tqRoundTrip(t, h, tok)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("over-budget RS256 user code = %d, want 429", rec.Code)
    }
}

// Default algorithm resolution: empty jwt_algorithm + empty jwt_secret
// resolves to rs256 (the deployed identity signs RS256); a set secret
// keeps the hs256 test/local profile.
func TestResolveAlgorithm(t *testing.T) {
    cases := []struct {
        alg, secret, want string
    }{
        {"", "", "rs256"},
        {"", "s", "hs256"},
        {"rs256", "s", "rs256"},
        {"hs256", "", "hs256"},
        {"RS256", "", "rs256"},
        {"bogus", "", ""},
    }
    for _, c := range cases {
        got := resolveAlgorithm(TryQuotaSettings{Algorithm: c.alg, Secret: c.secret})
        if got != c.want {
            t.Fatalf("resolveAlgorithm(%q,%q) = %q, want %q", c.alg, c.secret, got, c.want)
        }
    }
}

func TestTryQuotaSoftLimitWebhookFiresOnce(t *testing.T) {
    var mu sync.Mutex
    var payloads []map[string]interface{}
    hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var p map[string]interface{}
        _ = json.NewDecoder(r.Body).Decode(&p)
        mu.Lock()
        payloads = append(payloads, p)
        mu.Unlock()
        w.WriteHeader(http.StatusOK)
    }))
    defer hook.Close()

    meter := tryquota.NewStore("")
    cfgSrc := func() TryQuotaSettings {
        return TryQuotaSettings{
            Enabled:          true,
            Budget:           1000,
            Algorithm:        "hs256",
            Secret:           tqTestSecret,
            EstimateFallback: 0,
            SoftLimitPercent: 50,
            AlertWebhook:     hook.URL,
        }
    }
    h := tqHandler(cfgSrc, meter, func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })

    tok := tqMint(t, "soft-user", time.Now().Add(time.Hour))
    // First request: no estimate (nil estFn, fallback 0) — usage 0, no
    // crossing yet.
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
    entry := &store.RequestLog{}
    req = WithRequestLogContext(req, entry)
    req.Header.Set("X-Fusion-Identity-Token", tok)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("under-budget request must pass, got %d", rec.Code)
    }

    // Finalize accounts the real usage (600 > 50% of 1000).
    entry.TryQuotaSub = "soft-user"
    entry.TryQuotaEst = 600
    entry.TotalTokens = 600
    TryQuotaFinalize(entry, meter)

    // Second request: the gate sees used=600 over soft=500 — webhook fires.
    req2a := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
    entry2a := &store.RequestLog{}
    req2a = WithRequestLogContext(req2a, entry2a)
    req2a.Header.Set("X-Fusion-Identity-Token", tok)
    rec2a := httptest.NewRecorder()
    h.ServeHTTP(rec2a, req2a)
    if rec2a.Code != http.StatusOK {
        t.Fatalf("crossing request must pass, got %d", rec2a.Code)
    }

    deadline := time.Now().Add(2 * time.Second)
    for time.Now().Before(deadline) {
        mu.Lock()
        n := len(payloads)
        mu.Unlock()
        if n >= 1 {
            break
        }
        time.Sleep(10 * time.Millisecond)
    }
    mu.Lock()
    n := len(payloads)
    var first map[string]interface{}
    if n > 0 {
        first = payloads[0]
    }
    mu.Unlock()
    if n != 1 {
        t.Fatalf("webhook must fire exactly once, got %d", n)
    }
    if first["event"] != "budget.soft_limit" {
        t.Fatalf("unexpected webhook event %v", first["event"])
    }
    if first["sub"] != "soft-user" {
        t.Fatalf("unexpected webhook sub %v", first["sub"])
    }

    // Third crossing request: no second webhook (once per window).
    req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
    entry2 := &store.RequestLog{}
    req2 = WithRequestLogContext(req2, entry2)
    req2.Header.Set("X-Fusion-Identity-Token", tok)
    rec2 := httptest.NewRecorder()
    h.ServeHTTP(rec2, req2)
    if rec2.Code != http.StatusOK {
        t.Fatalf("second request must pass, got %d", rec2.Code)
    }
    time.Sleep(150 * time.Millisecond)
    mu.Lock()
    n = len(payloads)
    mu.Unlock()
    if n != 1 {
        t.Fatalf("webhook must fire once per window, got %d", n)
    }
}

func TestTryQuotaSoftLimitNoWebhookURLStillAlerts(t *testing.T) {
    meter := tryquota.NewStore("")
    cfgSrc := func() TryQuotaSettings {
        return TryQuotaSettings{
            Enabled:          true,
            Budget:           100,
            Algorithm:        "hs256",
            Secret:           tqTestSecret,
            EstimateFallback: 0,
            SoftLimitPercent: 80,
        }
    }
    h := tqHandler(cfgSrc, meter, func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    tok := tqMint(t, "soft2", time.Now().Add(time.Hour))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
    entry := &store.RequestLog{}
    req = WithRequestLogContext(req, entry)
    req.Header.Set("X-Fusion-Identity-Token", tok)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("request must pass, got %d", rec.Code)
    }
    entry.TryQuotaSub = "soft2"
    entry.TryQuotaEst = 90
    TryQuotaFinalize(entry, meter)
    if got := meter.UsedToday("soft2", time.Now()); got != 90 {
        t.Fatalf("usage must be accounted, got %d", got)
    }
}
