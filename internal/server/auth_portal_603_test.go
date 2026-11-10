package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "testing"
    "time"
)

// Issue #603: the auth portal login was the remaining brute-force entry
// point — the shell and site logins require a CAPTCHA, /auth/ did not.
// The portal login proxy now verifies the same single-use challenge BEFORE
// the lockout counter and the upstream credential check: a captcha failure
// never feeds the #590 lock counter and never reaches identity.

func portal603(t *testing.T, upstream *url.URL) (*Server, *authPortal) {
    t.Helper()
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Captcha = config.ShellCaptchaConfig{Enabled: true, TTL: 5 * time.Minute}
    s.setupShell()
    p := &authPortal{
        identity:      upstream,
        httpClient:    newIdentityHTTPClient(),
        loginThrottle: newShellLoginThrottle(),
        captcha:       s.shellCaptcha,
    }
    return s, p
}

func portal603Login(p *authPortal, body map[string]string) *httptest.ResponseRecorder {
    raw, _ := json.Marshal(body)
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(string(raw)))
    w := httptest.NewRecorder()
    p.handlePortalLogin(w, req, "/api/v1/auth/login")
    return w
}

func Test603_LoginWithoutCaptchaRejectedBeforeCredentials(t *testing.T) {
    // Upstream must NEVER see the request when the challenge is missing.
    reached := false
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        reached = true
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()
    u, _ := url.Parse(srv.URL)
    _, p := portal603(t, u)
    w := portal603Login(p, map[string]string{"username": "admin", "password": "x"})
    if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "captcha_missing") {
        t.Fatalf("missing challenge must 400 captcha_missing, got %d %s", w.Code, w.Body.String())
    }
    if reached {
        t.Fatal("captcha failure must not reach identity")
    }
}

func Test603_LoginWrongCaptchaBurnsChallengeNotLockCounter(t *testing.T) {
    reached := false
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        reached = true
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()
    u, _ := url.Parse(srv.URL)
    _, p := portal603(t, u)
    id, _ := p.captcha.issue()
    w := portal603Login(p, map[string]string{
        "username": "admin", "password": "x",
        "captcha_id": id, "captcha_code": "WRONG",
    })
    if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "captcha_mismatch") {
        t.Fatalf("wrong challenge must 400 captcha_mismatch, got %d %s", w.Code, w.Body.String())
    }
    if reached {
        t.Fatal("captcha failure must not reach identity")
    }
    // The wrong attempt consumed the challenge (single-use), but did NOT
    // feed the #590 lockout counter: the same source may still submit a
    // fresh challenge + correct credentials.
    id2, code2 := p.captcha.issue()
    w2 := portal603Login(p, map[string]string{
        "username": "admin", "password": "x",
        "captcha_id": id2, "captcha_code": code2,
    })
    if w2.Code != http.StatusOK || !reached {
        t.Fatalf("valid challenge must forward upstream, got %d (reached=%v) %s", w2.Code, reached, w2.Body.String())
    }
    if ok, _ := p.loginThrottle.allowed("ip:9.9.9.9", "user:admin"); !ok {
        t.Fatal("sanity: throttle must be permissive for a fresh source")
    }
}

func Test603_CaptchaEndpointNoStoreEnabledShape(t *testing.T) {
    _, p := portal603(t, nil)
    req := httptest.NewRequest(http.MethodGet, "/auth/api/captcha", nil)
    w := httptest.NewRecorder()
    p.handleAuthPortalCaptcha(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("challenge code = %d", w.Code)
    }
    if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
        t.Fatalf("challenge must be no-store, got %q", cc)
    }
    var resp map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &resp)
    if resp["enabled"] != true || resp["captcha_id"] == "" || resp["image"] == "" {
        t.Fatalf("challenge shape incomplete: %v", resp)
    }
    // Disabled plane degrades to {"enabled":false}.
    _, p2 := portal603(t, nil)
    p2.captcha = nil
    w2 := httptest.NewRecorder()
    p2.handleAuthPortalCaptcha(w2, httptest.NewRequest(http.MethodGet, "/auth/api/captcha", nil))
    var resp2 map[string]any
    _ = json.Unmarshal(w2.Body.Bytes(), &resp2)
    if resp2["enabled"] != false {
        t.Fatalf("nil store must answer enabled=false, got %v", resp2)
    }
}
