package server

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// #679: the shell module proxy translates a validated fusion_shell_session
// into signed identity headers (X-Fusion-Shell-User / -Ts / -Sig) on the
// upstream hop, and never forwards client-supplied identity headers.

// capturedHeaders records what one upstream request actually received.
type capturedHeaders struct {
    mu     sync.Mutex
    header http.Header
    got    chan struct{}
}

func newCaptureUpstream(t *testing.T) (*httptest.Server, *capturedHeaders) {
    t.Helper()
    cap := &capturedHeaders{got: make(chan struct{}, 8)}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        cap.mu.Lock()
        cap.header = r.Header.Clone()
        cap.mu.Unlock()
        cap.got <- struct{}{}
        w.WriteHeader(http.StatusOK)
    }))
    t.Cleanup(srv.Close)
    return srv, cap
}

func (c *capturedHeaders) wait(t *testing.T) http.Header {
    t.Helper()
    select {
    case <-c.got:
    case <-time.After(5 * time.Second):
        t.Fatal("upstream never received the proxied request")
    }
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.header
}

func newIdentityTestServer(t *testing.T, upstream string) *Server {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Shell.Enabled = true
    cfg.Config.Shell.Session.Secret = "test-shell-secret-at-least-32-chars!"
    cfg.Config.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    cfg.Config.Shell.Modules = map[string]config.ShellModuleConfig{
        "fusion-site": {Upstream: upstream},
    }
    cfg.Config.Shell.Session.Users = []config.ShellUser{
        {Username: "alice", Password: "alicepw", Roles: []string{"user"}, Modules: []string{"fusion-site"}},
    }
    s := &Server{cfg: cfg}
    s.setupShell()
    return s
}

func identityProxyRequest(s *Server, path, token string, extra http.Header) *httptest.ResponseRecorder {
    req := httptest.NewRequest(http.MethodGet, path, nil)
    switch {
    case strings.HasPrefix(token, "fm-"):
        req.Header.Set("Authorization", "Bearer "+token)
    case token != "":
        req.AddCookie(&http.Cookie{Name: shellSessionCookieName, Value: token})
    }
    for k, vs := range extra {
        for _, v := range vs {
            req.Header.Set(k, v)
        }
    }
    w := httptest.NewRecorder()
    s.withShellProxyAuth(s.shellProxy.ServeHTTP)(w, req)
    return w
}

func expectedIdentitySig(t *testing.T, s *Server, user, path, ts string) string {
    t.Helper()
    mac := hmac.New(sha256.New, s.shellSessionSecret)
    mac.Write([]byte(user + "\n" + path + "\n" + ts))
    return hex.EncodeToString(mac.Sum(nil))
}

// Valid session: upstream sees the gateway-signed identity triple, and the
// signature verifies against the persisted shell session secret.
func TestShellIdentity_ValidSessionInjectsSignedHeaders(t *testing.T) {
    upstream, cap := newCaptureUpstream(t)
    s := newIdentityTestServer(t, upstream.URL)
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)

    path := "/shell/fusion-site/factory/new"
    w := identityProxyRequest(s, path, token, nil)
    if w.Code != http.StatusOK {
        t.Fatalf("proxied request code = %d body=%s", w.Code, w.Body.String())
    }
    h := cap.wait(t)
    user := h.Get(shell.ShellUserHeader)
    ts := h.Get(shell.ShellTsHeader)
    sig := h.Get(shell.ShellSigHeader)
    if user != "alice" {
        t.Fatalf("upstream X-Fusion-Shell-User = %q, want alice", user)
    }
    if ts == "" {
        t.Fatal("upstream X-Fusion-Shell-Ts missing")
    }
    if want := expectedIdentitySig(t, s, user, path, ts); sig != want {
        t.Fatalf("sig mismatch: got %q want %q", sig, want)
    }
    if h.Get("Cookie") != "" {
        t.Fatal("session cookie leaked upstream")
    }
}

// Anonymous request (no cookie, no token): rejected at the gateway, the
// upstream is never reached.
func TestShellIdentity_NoCookieRejected(t *testing.T) {
    upstream, cap := newCaptureUpstream(t)
    s := newIdentityTestServer(t, upstream.URL)

    w := identityProxyRequest(s, "/shell/fusion-site/factory/new", "", nil)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("anonymous request code = %d, want 401 body=%s", w.Code, w.Body.String())
    }
    select {
    case <-cap.got:
        t.Fatal("upstream received an anonymous request")
    case <-time.After(200 * time.Millisecond):
    }
}

// A valid session plus forged identity headers: the validated claims win,
// the forged values never reach the upstream.
func TestShellIdentity_ValidSessionOverridesForgedHeaders(t *testing.T) {
    upstream, cap := newCaptureUpstream(t)
    s := newIdentityTestServer(t, upstream.URL)
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)

    forged := http.Header{}
    forged.Set(shell.ShellUserHeader, "admin")
    forged.Set(shell.ShellSigHeader, "forged-sig")
    forged.Set(shell.ShellTsHeader, "999")
    path := "/shell/fusion-site/factory/new"
    w := identityProxyRequest(s, path, token, forged)
    if w.Code != http.StatusOK {
        t.Fatalf("proxied request code = %d body=%s", w.Code, w.Body.String())
    }
    h := cap.wait(t)
    if h.Get(shell.ShellUserHeader) != "alice" {
        t.Fatalf("upstream user = %q, want validated alice (forged admin must not win)", h.Get(shell.ShellUserHeader))
    }
    if want := expectedIdentitySig(t, s, "alice", path, h.Get(shell.ShellTsHeader)); h.Get(shell.ShellSigHeader) != want {
        t.Fatalf("sig mismatch: got %q want %q", h.Get(shell.ShellSigHeader), want)
    }
}

// Invalid/expired session on the proxy route is rejected at the gateway —
// the request never reaches the upstream at all.
func TestShellIdentity_InvalidSessionRejected(t *testing.T) {
    upstream, cap := newCaptureUpstream(t)
    s := newIdentityTestServer(t, upstream.URL)

    w := identityProxyRequest(s, "/shell/fusion-site/factory/new", "bogus.token", nil)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("invalid session code = %d, want 401 body=%s", w.Code, w.Body.String())
    }
    select {
    case <-cap.got:
        t.Fatal("upstream received a request for an invalid session")
    case <-time.After(200 * time.Millisecond):
    }
}

// Module service-key plane (#530): the key authenticates the module itself,
// no user identity is injected — the upstream keeps its own gate.
func TestShellIdentity_ServiceKeyPlaneInjectsNothing(t *testing.T) {
    upstream, cap := newCaptureUpstream(t)
    s := newIdentityTestServer(t, upstream.URL)
    raw := selfHealRegister(t, s, "fusion-site")

    w := identityProxyRequest(s, "/shell/fusion-site/factory/new", raw, nil)
    if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
        t.Fatalf("valid fm- key rejected: %d %s", w.Code, w.Body.String())
    }
    h := cap.wait(t)
    if h.Get(shell.ShellUserHeader) != "" || h.Get(shell.ShellSigHeader) != "" {
        t.Fatalf("identity headers injected on service-key plane: user=%q sig=%q",
            h.Get(shell.ShellUserHeader), h.Get(shell.ShellSigHeader))
    }
}

// A valid module service key with client-forged identity headers: the forged
// headers are stripped, nothing identity-shaped reaches the upstream.
func TestShellIdentity_ServiceKeyPlaneStripsForgedHeaders(t *testing.T) {
    upstream, cap := newCaptureUpstream(t)
    s := newIdentityTestServer(t, upstream.URL)
    raw := selfHealRegister(t, s, "fusion-site")

    forged := http.Header{}
    forged.Set(shell.ShellUserHeader, "admin")
    forged.Set(shell.ShellSigHeader, "deadbeef")
    forged.Set(shell.ShellTsHeader, "123")
    w := identityProxyRequest(s, "/shell/fusion-site/factory/new", raw, forged)
    if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
        t.Fatalf("valid fm- key rejected: %d %s", w.Code, w.Body.String())
    }
    h := cap.wait(t)
    if h.Get(shell.ShellUserHeader) != "" || h.Get(shell.ShellSigHeader) != "" || h.Get(shell.ShellTsHeader) != "" {
        t.Fatalf("forged identity headers forwarded upstream: user=%q sig=%q ts=%q",
            h.Get(shell.ShellUserHeader), h.Get(shell.ShellSigHeader), h.Get(shell.ShellTsHeader))
    }
}
