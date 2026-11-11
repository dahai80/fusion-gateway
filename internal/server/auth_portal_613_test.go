package server

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "math/big"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #613: the portal code exchange returned the identity token pair as-is and
// the SPA stored identity access_token in the shell-session slot —
// withShellSession HMAC verification then 401'd every shell call. The
// exchange must establish a real shell session: shell session token as
// access_token, HttpOnly cookie set. Acceptance is the full HTTP chain
// (issue code with a verified identity token → exchange → GET
// /api/v1/shell/me with the returned access_token → 200), not a direct
// handler unit test (gw#606 false-green lesson).

const portalRedirect613 = "http://127.0.0.1:11432/"

type portalUpstream613 struct {
    ts  *httptest.Server
    key *rsa.PrivateKey
}

func newPortalUpstream613(t *testing.T) *portalUpstream613 {
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
    ts := httptest.NewServer(mux)
    t.Cleanup(ts.Close)
    return &portalUpstream613{ts: ts, key: key}
}

func (u *portalUpstream613) sign(t *testing.T, sub, role, tid string) string {
    t.Helper()
    tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
        "sub": sub, "tid": tid, "role": role, "exp": time.Now().Add(time.Hour).Unix(),
    })
    tok.Header["kid"] = "test-kid"
    signed, err := tok.SignedString(u.key)
    if err != nil {
        t.Fatalf("sign: %v", err)
    }
    return signed
}

func portalServer613(t *testing.T, up *portalUpstream613) (*Server, http.Handler) {
    t.Helper()
    s := newShellTestServer(t, false)
    s.cfg.Config.AuthPortal = config.AuthPortalConfig{
        Enabled:         true,
        IdentityURL:     up.ts.URL,
        RedirectURIs:    []string{portalRedirect613},
        CodeTTLSecs:     60,
        RatePerMin:      1000,
    }
    mux := http.NewServeMux()
    s.registerShellRoutes(mux)
    s.registerAuthPortalRoutes(mux)
    return s, mux
}

func postJSON613(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
    req.RemoteAddr = "127.0.0.1:51000"
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    return rec
}

func Test613_ExchangeEstablishesShellSession_FullChain(t *testing.T) {
    up := newPortalUpstream613(t)
    _, h := portalServer613(t, up)

    tok := up.sign(t, "usr_portal", "tenant_admin", "default")
    issue := postJSON613(t, h, "/auth/api/code",
        `{"access_token":`+jsonString613(tok)+`,"redirect_uri":`+jsonString613(portalRedirect613)+`}`)
    if issue.Code != http.StatusOK {
        t.Fatalf("issue code: %d %s", issue.Code, issue.Body.String())
    }
    var granted struct {
        Code string `json:"code"`
    }
    if err := json.Unmarshal(issue.Body.Bytes(), &granted); err != nil || granted.Code == "" {
        t.Fatalf("issue response: %v %s", err, issue.Body.String())
    }

    ex := postJSON613(t, h, "/auth/api/code/exchange",
        `{"code":`+jsonString613(granted.Code)+`,"redirect_uri":`+jsonString613(portalRedirect613)+`}`)
    if ex.Code != http.StatusOK {
        t.Fatalf("exchange: %d %s", ex.Code, ex.Body.String())
    }
    var pair map[string]any
    if err := json.Unmarshal(ex.Body.Bytes(), &pair); err != nil {
        t.Fatalf("exchange decode: %v", err)
    }
    accessToken, _ := pair["access_token"].(string)
    identityToken, _ := pair["identity_access_token"].(string)
    // Shell frontend contract: access_token goes into the shell-session
    // slot, so it MUST be a shell session token, not the identity RS256 JWT.
    if accessToken == "" {
        t.Fatal("exchange must return access_token")
    }
    if identityToken != tok {
        t.Fatalf("identity token must stay available under identity_access_token, body: %s", ex.Body.String())
    }
    // HttpOnly session cookie must ride the exchange response like the SSO
    // path so iframe navigation authenticates.
    var cookie *http.Cookie
    for _, c := range ex.Result().Cookies() {
        if c.Name == shellSessionCookieName {
            cookie = c
        }
    }
    if cookie == nil {
        t.Fatalf("exchange must set %s cookie, headers: %v", shellSessionCookieName, ex.Header())
    }
    if !cookie.HttpOnly {
        t.Fatal("session cookie must be HttpOnly")
    }

    // Full-chain acceptance: the returned access_token must authenticate
    // the shell session plane.
    me := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    me.Header.Set("Authorization", "Bearer "+accessToken)
    me.RemoteAddr = "127.0.0.1:51001"
    meRec := httptest.NewRecorder()
    h.ServeHTTP(meRec, me)
    if meRec.Code != http.StatusOK {
        t.Fatalf("shell /me with exchange access_token: %d %s", meRec.Code, meRec.Body.String())
    }
    // Tenant role mapped from the identity claims, origin identity.
    var meBody map[string]any
    _ = json.Unmarshal(meRec.Body.Bytes(), &meBody)
    t.Logf("me body: %s", meRec.Body.String())
}

func Test613_ExchangeTamperedGrant_NotExchangedForSession(t *testing.T) {
    // A grant minted from an expired/invalid identity token never reaches
    // issue (verify at issue time), so the session-mint path only ever sees
    // verified tokens; the redirect binding must still hold after the mint.
    up := newPortalUpstream613(t)
    _, h := portalServer613(t, up)
    tok := up.sign(t, "usr_portal", "user", "default")
    issue := postJSON613(t, h, "/auth/api/code",
        `{"access_token":`+jsonString613(tok)+`,"redirect_uri":`+jsonString613(portalRedirect613)+`}`)
    var granted struct {
        Code string `json:"code"`
    }
    _ = json.Unmarshal(issue.Body.Bytes(), &granted)
    ex := postJSON613(t, h, "/auth/api/code/exchange",
        `{"code":`+jsonString613(granted.Code)+`,"redirect_uri":"http://evil.example/"}`)
    if ex.Code != http.StatusForbidden {
        t.Fatalf("redirect mismatch must stay 403, got %d %s", ex.Code, ex.Body.String())
    }
}

func jsonString613(s string) string {
    b, _ := json.Marshal(s)
    return string(b)
}
