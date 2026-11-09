// auth_portal_test.go — one-time code handoff: issue/exchange round trip,
// single-use, TTL, redirect binding, allowlist enforcement, and the identity
// proxy path allowlist.
package server

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "math/big"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func newTestPortal(t *testing.T, redirects []string) (*authPortal, *rsa.PrivateKey) {
    t.Helper()
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        t.Fatalf("rsa key: %v", err)
    }
    pub := key.PublicKey
    mux := http.NewServeMux()
    mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "keys": []map[string]string{{
                "kid": "test-kid", "kty": "RSA", "use": "sig", "alg": "RS256",
                "n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
                "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
            }},
        })
    })
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]string{"echo": r.URL.Path})
    })
    upstream := httptest.NewServer(mux)
    t.Cleanup(upstream.Close)
    target, err := url.Parse(upstream.URL)
    if err != nil {
        t.Fatalf("parse upstream url: %v", err)
    }
    p := &authPortal{
        cfg: config.AuthPortalConfig{
            Enabled:      true,
            RedirectURIs: redirects,
            CodeTTLSecs:  60,
            RatePerMin:   1000,
        },
        identity:   target,
        httpClient: upstream.Client(),
        codes:      make(map[string]*authCodeGrant),
        hits:       make(map[string][]time.Time),
    }
    return p, key
}

func signTestToken(t *testing.T, key *rsa.PrivateKey, exp time.Time) string {
    t.Helper()
    tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": "usr_test", "tid": "default", "role": "user", "exp": exp.Unix(),
    })
    tok.Header["kid"] = "test-kid"
    signed, err := tok.SignedString(key)
    if err != nil {
        t.Fatalf("sign: %v", err)
    }
    return signed
}

func postPortal(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, "/auth/api/x", strings.NewReader(body))
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    return rec
}

func TestAuthPortalRedirectBase(t *testing.T) {
    if got := redirectBase("https://fusion-mlx.cn/login?return=/benchmarks"); got != "https://fusion-mlx.cn/login" {
        t.Fatalf("redirectBase with query: got %q", got)
    }
    if got := redirectBase("http://localhost:3000/login"); got != "http://localhost:3000/login" {
        t.Fatalf("redirectBase plain: got %q", got)
    }
    if redirectBase("not a url") != "" {
        t.Fatalf("redirectBase garbage should be empty")
    }
}

func TestAuthPortalIssueAndExchange(t *testing.T) {
    p, key := newTestPortal(t, []string{"http://localhost:3000/login"})
    token := signTestToken(t, key, time.Now().Add(time.Hour))

    rec := postPortal(t, p.guard(p.handleIssueCode),
        `{"access_token":"`+token+`","refresh_token":"rt","expires_in":900,"redirect_uri":"http://localhost:3000/login?return=/x","username":"tester"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("issue: status %d body %s", rec.Code, rec.Body.String())
    }
    var issued struct {
        Code string `json:"code"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &issued)
    if len(issued.Code) < 32 {
        t.Fatalf("issue: code too short %q", issued.Code)
    }

    rec = postPortal(t, p.guard(p.handleExchangeCode),
        `{"code":"`+issued.Code+`","redirect_uri":"http://localhost:3000/login"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("exchange: status %d body %s", rec.Code, rec.Body.String())
    }
    var grant map[string]string
    _ = json.Unmarshal(rec.Body.Bytes(), &grant)
    if grant["access_token"] != token || grant["refresh_token"] != "rt" {
        t.Fatalf("exchange: token mismatch")
    }

    // Single use: the same code never works twice.
    rec = postPortal(t, p.guard(p.handleExchangeCode),
        `{"code":"`+issued.Code+`","redirect_uri":"http://localhost:3000/login"}`)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("replay: status %d", rec.Code)
    }
}

func TestAuthPortalRedirectBindingAndAllowlist(t *testing.T) {
    p, key := newTestPortal(t, []string{"http://localhost:3000/login"})
    token := signTestToken(t, key, time.Now().Add(time.Hour))

    rec := postPortal(t, p.guard(p.handleIssueCode),
        `{"access_token":"`+token+`","redirect_uri":"https://evil.example/login"}`)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("allowlist: status %d", rec.Code)
    }

    rec = postPortal(t, p.guard(p.handleIssueCode),
        `{"access_token":"`+token+`","redirect_uri":"http://localhost:3000/login"}`)
    var issued struct {
        Code string `json:"code"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &issued)
    rec = postPortal(t, p.guard(p.handleExchangeCode),
        `{"code":"`+issued.Code+`","redirect_uri":"http://localhost:9999/login"}`)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("binding: status %d", rec.Code)
    }
}

func TestAuthPortalRejectsBadTokens(t *testing.T) {
    p, key := newTestPortal(t, []string{"http://localhost:3000/login"})

    expired := signTestToken(t, key, time.Now().Add(-time.Minute))
    rec := postPortal(t, p.guard(p.handleIssueCode),
        `{"access_token":"`+expired+`","redirect_uri":"http://localhost:3000/login"}`)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expired: status %d", rec.Code)
    }

    rec = postPortal(t, p.guard(p.handleIssueCode),
        `{"access_token":"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.forged","redirect_uri":"http://localhost:3000/login"}`)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("forged: status %d", rec.Code)
    }
}

func TestAuthPortalProxyAllowlist(t *testing.T) {
    p, _ := newTestPortal(t, []string{"http://localhost:3000/login"})
    h := p.guard(p.handleIdentityProxy)

    // Non-allowlisted generic path: 404 (postPortal posts to /auth/api/x).
    rec := postPortal(t, h, `{"username":"u","password":"p"}`)
    if rec.Code != http.StatusNotFound {
        t.Fatalf("proxy non-allowlisted path: status %d", rec.Code)
    }

    for _, path := range []string{
        "/auth/api/identity/api/v1/admin/tenants/provision",
        "/auth/api/identity/api/v1/users",
    } {
        req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
        rec := httptest.NewRecorder()
        h.ServeHTTP(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("proxy allowlist %s: status %d", path, rec.Code)
        }
    }

    // Allowlisted login path proxies through to the upstream echo.
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(`{"u":1}`))
    rec2 := httptest.NewRecorder()
    h.ServeHTTP(rec2, req)
    if rec2.Code != http.StatusOK {
        t.Fatalf("proxy login: status %d", rec2.Code)
    }
}

func TestAuthPortalProxyPasswordRecoveryAllowlist(t *testing.T) {
    p, _ := newTestPortal(t, []string{"http://localhost:3000/login"})
    h := p.guard(p.handleIdentityProxy)

    for _, path := range []string{
        "/auth/api/identity/api/v1/auth/password/forgot",
        "/auth/api/identity/api/v1/auth/password/reset",
    } {
        req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"u@example.com"}`))
        rec := httptest.NewRecorder()
        h.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("proxy %s: status %d", path, rec.Code)
        }
        // GET must stay refused — credential-shaped POST surface only.
        getReq := httptest.NewRequest(http.MethodGet, path, nil)
        getRec := httptest.NewRecorder()
        h.ServeHTTP(getRec, getReq)
        if getRec.Code != http.StatusNotFound {
            t.Fatalf("GET %s must be refused, got %d", path, getRec.Code)
        }
    }
}

// #590: the portal login proxy must lock out a source after repeated failed
// logins (same policy as the shell plane, #587): 5 failures inside the
// window lock both the IP and the username key, the lockout 429s even a
// correct password (no oracle during lockout), and a successful login
// clears the counters.
func TestAuthPortalLoginLockout(t *testing.T) {
    loginFails := 5
    mux := http.NewServeMux()
    mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
        if loginFails > 0 {
            loginFails--
            w.WriteHeader(http.StatusUnauthorized)
            _ = json.NewEncoder(w).Encode(map[string]string{"code": "invalid_credentials"})
            return
        }
        w.WriteHeader(http.StatusOK)
        _ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
    })
    upstream := httptest.NewServer(mux)
    t.Cleanup(upstream.Close)
    target, err := url.Parse(upstream.URL)
    if err != nil {
        t.Fatalf("parse upstream url: %v", err)
    }
    p := &authPortal{
        cfg: config.AuthPortalConfig{
            Enabled:    true,
            RatePerMin: 1000,
        },
        identity:      target,
        httpClient:    upstream.Client(),
        codes:         make(map[string]*authCodeGrant),
        hits:          make(map[string][]time.Time),
        loginThrottle: newShellLoginThrottle(),
    }
    login := func(body string) *httptest.ResponseRecorder {
        req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(body))
        rec := httptest.NewRecorder()
        p.handleIdentityProxy(rec, req)
        return rec
    }

    for i := 0; i < 5; i++ {
        rec := login(`{"username":"alice","password":"wrong"}`)
        if rec.Code != http.StatusUnauthorized {
            t.Fatalf("attempt %d: want 401, got %d", i+1, rec.Code)
        }
    }
    // 6th attempt: locked out before the upstream is consulted — even a
    // correct password must not reveal anything.
    rec := login(`{"username":"alice","password":"correct"}`)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("locked attempt: want 429, got %d", rec.Code)
    }
    if body := rec.Body.String(); !strings.Contains(body, "rate_limited") {
        t.Fatalf("lockout envelope: want rate_limited, got %s", body)
    }
    // A different username from the same IP is still inside the lockout —
    // the IP key engages too.
    rec = login(`{"username":"bob","password":"correct"}`)
    if rec.Code != http.StatusTooManyRequests {
        t.Fatalf("ip-keyed lockout: want 429, got %d", rec.Code)
    }

    // Success clears both keys.
    p.loginThrottle.recordSuccess("192.0.2.1", "alice")
    rec = login(`{"username":"alice","password":"correct"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("post-clear login: want 200, got %d", rec.Code)
    }
}
