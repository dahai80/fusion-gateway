package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

func newShellTestServer(t *testing.T, withBootstrap bool) *Server {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Shell.Enabled = true
    cfg.Config.Shell.Session.Secret = "test-shell-secret-at-least-32-chars!"
    cfg.Config.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    cfg.Config.Shell.Modules = map[string]config.ShellModuleConfig{
        "fusion-mlx": {Upstream: "http://127.0.0.1:11434"},
    }
    s := &Server{cfg: cfg}
    s.setupShell()
    if withBootstrap {
        if s.shellBootstrap == nil {
            t.Fatal("bootstrap admin expected when no users configured")
        }
    } else {
        cfg.Config.Shell.Session.Users = []config.ShellUser{
            {Username: "admin", Password: "s3cret", Roles: []string{"admin"}},
            {Username: "alice", Password: "alicepw", Roles: []string{"user"}, Modules: []string{"fusion-mlx"}},
        }
        s.setupShell()
        if s.shellBootstrap != nil {
            t.Fatal("bootstrap admin must not exist when users configured")
        }
    }
    return s
}

func loginShell(t *testing.T, s *Server, username, password string) (int, map[string]any) {
    t.Helper()
    body, _ := json.Marshal(map[string]string{"username": username, "password": password})
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session", strings.NewReader(string(body)))
    w := httptest.NewRecorder()
    s.handleShellSessionIssue(w, req)
    var resp map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &resp)
    return w.Code, resp
}

func TestShellSession_LoginSuccessAndMe(t *testing.T) {
    s := newShellTestServer(t, false)
    code, resp := loginShell(t, s, "admin", "s3cret")
    if code != http.StatusOK {
        t.Fatalf("login code = %d body=%v", code, resp)
    }
    token, _ := resp["token"].(string)
    if token == "" {
        t.Fatalf("no token in response: %v", resp)
    }
    if resp["tenant_id"] != "local" {
        t.Fatalf("tenant_id = %v", resp["tenant_id"])
    }

    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withShellSession(s.handleShellMe)(w, req)
    var me map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &me)
    if me["user_id"] != "admin" {
        t.Fatalf("me = %v", me)
    }
    perms, _ := me["permissions"].([]any)
    if len(perms) != 1 || perms[0] != "*" {
        t.Fatalf("admin permissions = %v", perms)
    }
}

func TestShellSession_LoginFailures(t *testing.T) {
    s := newShellTestServer(t, false)
    code, _ := loginShell(t, s, "admin", "wrong")
    if code != http.StatusUnauthorized {
        t.Fatalf("wrong password code = %d", code)
    }
    code, _ = loginShell(t, s, "ghost", "x")
    if code != http.StatusUnauthorized {
        t.Fatalf("unknown user code = %d", code)
    }
    // Malformed body.
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session", strings.NewReader("not json"))
    w := httptest.NewRecorder()
    s.handleShellSessionIssue(w, req)
    if w.Code != http.StatusBadRequest {
        t.Fatalf("malformed body code = %d", w.Code)
    }
}

func TestShellSession_BootstrapAdmin(t *testing.T) {
    s := newShellTestServer(t, true)
    code, resp := loginShell(t, s, "admin", s.shellBootstrap.password)
    if code != http.StatusOK {
        t.Fatalf("bootstrap login code = %d body=%v", code, resp)
    }
    if resp["must_change_password"] != true {
        t.Fatalf("bootstrap must_change_password = %v", resp["must_change_password"])
    }
}

func TestShellSession_LogoutRevokes(t *testing.T) {
    s := newShellTestServer(t, false)
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)

    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session/logout", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withShellSession(s.handleShellSessionLogout)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("logout code = %d", w.Code)
    }

    req2 := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req2.Header.Set("Authorization", "Bearer "+token)
    w2 := httptest.NewRecorder()
    s.withShellSession(s.handleShellMe)(w2, req2)
    if w2.Code != http.StatusUnauthorized {
        t.Fatalf("post-logout me code = %d, want 401", w2.Code)
    }
}

func TestShellSession_MissingTokenEnvelope(t *testing.T) {
    s := newShellTestServer(t, false)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    w := httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d", w.Code)
    }
    if !strings.Contains(w.Body.String(), `"code":"session_required"`) {
        t.Fatalf("body = %s", w.Body.String())
    }
}

func TestShellManifest_UpsertListHeartbeat(t *testing.T) {
    s := newShellTestServer(t, false)
    // Session-issued token is NOT valid for fg-key auth — but the upsert
    // handler is exercised directly (wrap happens at registration).
    m := map[string]any{
        "id": "fusion-mlx", "title": "MLX", "path": "/mlx",
        "entry": "http://127.0.0.1:11432/shell/fusion-mlx/",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(m)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", strings.NewReader(string(body)))
    w := httptest.NewRecorder()
    s.handleShellManifestUpsert(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("upsert code = %d body=%s", w.Code, w.Body.String())
    }

    // Whitelist rejection (A7).
    m["id"] = "fusion-evil"
    m["entry"] = "http://evil.example:99/shell/fusion-evil/"
    body, _ = json.Marshal(m)
    req = httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", strings.NewReader(string(body)))
    w = httptest.NewRecorder()
    s.handleShellManifestUpsert(w, req)
    if w.Code != http.StatusForbidden {
        t.Fatalf("non-whitelisted origin code = %d, want 403", w.Code)
    }

    // Heartbeat on unknown id.
    req = httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-ghost/heartbeat", nil)
    w = httptest.NewRecorder()
    s.handleShellManifestHeartbeat(w, req)
    if w.Code != http.StatusNotFound {
        t.Fatalf("unknown heartbeat code = %d", w.Code)
    }

    // List shows the registered entry online (bare array — shell host shape).
    // The built-in fusion-admin manifest (#216) is also auto-registered.
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    w = httptest.NewRecorder()
    s.handleShellManifestList(w, req)
    var listResp []shell.ManifestView
    _ = json.Unmarshal(w.Body.Bytes(), &listResp)
    if len(listResp) != 2 {
        t.Fatalf("manifest list = %+v", listResp)
    }
    for _, v := range listResp {
        if v.ID == "fusion-mlx" && (v.Status != shell.StatusOnline || v.Unreachable) {
            t.Fatalf("fusion-mlx = %+v, want online", v)
        }
    }
}

func TestShellProxy_RoutesThroughSessionAuth(t *testing.T) {
    s := newShellTestServer(t, false)
    // Dead upstream so the proxy response is deterministic (a live local
    // engine would answer with its own auth error).
    s.cfg.Config.Shell.Modules["fusion-mlx"] = config.ShellModuleConfig{Upstream: "http://127.0.0.1:1"}
    s.setupShell()
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)

    // Alice has fusion-mlx permission — but auth wrap only checks token
    // validity at this layer; the upstream is dead so the envelope is
    // backend_unavailable (503), proving the request reached the proxy.
    req := httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withShellSession(s.shellProxy.ServeHTTP)(w, req)
    if w.Code != http.StatusServiceUnavailable {
        t.Fatalf("proxy through auth code = %d body=%s", w.Code, w.Body.String())
    }
    if !strings.Contains(w.Body.String(), `"module_id":"fusion-mlx"`) {
        t.Fatalf("envelope = %s", w.Body.String())
    }
}

func TestShellDisabled_NoRoutes(t *testing.T) {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    s := &Server{cfg: cfg}
    s.setupShell()
    if s.shellSessions != nil || s.shellRegistry != nil || s.shellProxy != nil {
        t.Fatal("disabled shell must leave subsystem nil (inert)")
    }
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    w := httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
    if w.Code != http.StatusNotFound {
        t.Fatalf("disabled shell code = %d, want 404 envelope", w.Code)
    }
}

func TestShellSession_TTLFromConfig(t *testing.T) {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Shell.Enabled = true
    cfg.Config.Shell.Session.Secret = "test-shell-secret-at-least-32-chars!"
    cfg.Config.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    cfg.Config.Shell.Session.TTL = 2 * time.Hour
    s := &Server{cfg: cfg}
    s.setupShell()
    token, err := s.shellSessions.Issue(shell.SessionClaims{Sub: "u"})
    if err != nil {
        t.Fatalf("issue: %v", err)
    }
    claims, err := s.shellSessions.Validate(token)
    if err != nil {
        t.Fatalf("validate: %v", err)
    }
    remaining := time.Until(time.Unix(claims.Exp, 0))
    if remaining <= time.Hour || remaining > 2*time.Hour+time.Minute {
        t.Fatalf("expiry not from config TTL: %s", remaining)
    }
}

func TestShellProxy_ModuleRBAC(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Modules["fusion-mlx"] = config.ShellModuleConfig{Upstream: "http://127.0.0.1:1"}
    s.cfg.Config.Shell.Modules["fusion-dead"] = config.ShellModuleConfig{Upstream: "http://127.0.0.1:1"}
    s.setupShell()
    _, resp := loginShell(t, s, "alice", "alicepw")
    token := resp["token"].(string)

    // F-2/S-8: alice's permissions are [fusion-mlx] — fusion-dead must be
    // rejected 403 at the gateway, never reaching the upstream dial.
    req := httptest.NewRequest(http.MethodGet, "/shell/fusion-dead/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+token)
    w := httptest.NewRecorder()
    s.withShellSession(s.withShellModuleAccess(s.shellProxy.ServeHTTP))(w, req)
    if w.Code != http.StatusForbidden {
        t.Fatalf("unauthorized module code = %d, want 403 body=%s", w.Code, w.Body.String())
    }
    if !strings.Contains(w.Body.String(), `"code":"module_forbidden"`) {
        t.Fatalf("envelope = %s", w.Body.String())
    }

    // The permitted module still reaches the proxy (dead upstream = 503).
    req2 := httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/v1/models", nil)
    req2.Header.Set("Authorization", "Bearer "+token)
    w2 := httptest.NewRecorder()
    s.withShellSession(s.withShellModuleAccess(s.shellProxy.ServeHTTP))(w2, req2)
    if w2.Code != http.StatusServiceUnavailable {
        t.Fatalf("permitted module code = %d, want 503 body=%s", w2.Code, w2.Body.String())
    }
}

func TestShellSession_CookieIssuedAndAcceptedOnShellProxy(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Modules["fusion-mlx"] = config.ShellModuleConfig{Upstream: "http://127.0.0.1:1"}
    s.setupShell()

    // Login sets the HttpOnly session cookie (F-1).
    body, _ := json.Marshal(map[string]string{"username": "alice", "password": "alicepw"})
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session", strings.NewReader(string(body)))
    w := httptest.NewRecorder()
    s.handleShellSessionIssue(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("login code = %d", w.Code)
    }
    cookies := w.Result().Cookies()
    var session *http.Cookie
    for _, c := range cookies {
        if c.Name == "fusion_shell_session" {
            session = c
        }
    }
    if session == nil || session.Value == "" || !session.HttpOnly || session.MaxAge <= 0 {
        t.Fatalf("session cookie = %+v", session)
    }

    // Cookie alone (no Authorization header) authenticates on /shell/*.
    req2 := httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/v1/models", nil)
    req2.AddCookie(session)
    w2 := httptest.NewRecorder()
    s.withShellSession(s.withShellModuleAccess(s.shellProxy.ServeHTTP))(w2, req2)
    if w2.Code != http.StatusServiceUnavailable {
        t.Fatalf("cookie-only proxy code = %d, want 503 (proxy reached) body=%s", w2.Code, w2.Body.String())
    }

    // Logout revokes the cookie token: a follow-up request with the same
    // cookie is rejected 401.
    req3 := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session/logout", nil)
    req3.AddCookie(session)
    w3 := httptest.NewRecorder()
    s.withShellSession(s.handleShellSessionLogout)(w3, req3)
    if w3.Code != http.StatusOK {
        t.Fatalf("logout code = %d", w3.Code)
    }
    req4 := httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/v1/models", nil)
    req4.AddCookie(session)
    w4 := httptest.NewRecorder()
    s.withShellSession(s.withShellModuleAccess(s.shellProxy.ServeHTTP))(w4, req4)
    if w4.Code != http.StatusUnauthorized {
        t.Fatalf("post-logout cookie code = %d, want 401", w4.Code)
    }
}
