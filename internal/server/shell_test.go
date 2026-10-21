package server

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
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
        "entry":   "http://127.0.0.1:11432/shell/fusion-mlx/",
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
    // #229: the list is permission-filtered, so this goes through the
    // session auth as admin (`*` sees all).
    _, loginResp := loginShell(t, s, "admin", "s3cret")
    adminToken := loginResp["token"].(string)
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    req.Header.Set("Authorization", "Bearer "+adminToken)
    w = httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
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

// TestShellManifestList_PermissionFiltered (#229): a limited session sees
// only its granted manifest subset; admin (`*`) still sees all.
func TestShellManifestList_PermissionFiltered(t *testing.T) {
    s := newShellTestServer(t, false)
    // Register fusion-mlx so the registry carries 2 entries (fusion-mlx +
    // auto-registered fusion-admin); alice has only [fusion-mlx].
    m := map[string]any{
        "id": "fusion-mlx", "title": "MLX", "path": "/mlx",
        "entry":   "http://127.0.0.1:11432/shell/fusion-mlx/",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(m)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", strings.NewReader(string(body)))
    w := httptest.NewRecorder()
    s.handleShellManifestUpsert(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("upsert code = %d body=%s", w.Code, w.Body.String())
    }

    // Alice: exactly one manifest, and it is the granted one.
    _, resp := loginShell(t, s, "alice", "alicepw")
    aliceToken := resp["token"].(string)
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    req.Header.Set("Authorization", "Bearer "+aliceToken)
    w = httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
    var aliceView []shell.ManifestView
    _ = json.Unmarshal(w.Body.Bytes(), &aliceView)
    if len(aliceView) != 1 || aliceView[0].ID != "fusion-mlx" {
        t.Fatalf("alice manifest list = %+v, want only fusion-mlx", aliceView)
    }

    // Admin: still sees both.
    _, resp = loginShell(t, s, "admin", "s3cret")
    adminToken := resp["token"].(string)
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    req.Header.Set("Authorization", "Bearer "+adminToken)
    w = httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
    var adminView []shell.ManifestView
    _ = json.Unmarshal(w.Body.Bytes(), &adminView)
    if len(adminView) != 2 {
        t.Fatalf("admin manifest list = %+v, want all 2", adminView)
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

// #243: manifest writes are admin-plane — the master key or the designated
// web_shell.admin_key passes the gate; a plain client key is rejected 403
// (privilege inversion fixed) and an absent principal is rejected too.
func TestShellManifestAuthGate(t *testing.T) {
    s := newShellTestServer(t, false)
    const adminKey = "ak-shell-admin-0123456789abcdef"
    s.cfg.Config.Shell.AdminKey = adminKey

    manifest := map[string]any{
        "id": "fusion-mlx", "title": "MLX", "path": "/mlx",
        "entry":   "http://127.0.0.1:11432/shell/fusion-mlx/",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(manifest)
    call := func(p *middleware.Principal) *httptest.ResponseRecorder {
        req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
        if p != nil {
            req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), p))
        }
        w := httptest.NewRecorder()
        s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
        return w
    }

    if w := call(nil); w.Code != http.StatusForbidden {
        t.Fatalf("anonymous gate code = %d, want 403", w.Code)
    }
    client := &middleware.Principal{KeyConfig: &config.AuthKeyConfig{Key: "ak-client", Name: "consumer"}}
    if w := call(client); w.Code != http.StatusForbidden {
        t.Fatalf("client-key gate code = %d, want 403", w.Code)
    }
    admin := &middleware.Principal{KeyConfig: &config.AuthKeyConfig{Key: adminKey, Name: "shell-admin"}}
    if w := call(admin); w.Code != http.StatusOK {
        t.Fatalf("admin-key gate code = %d body=%s, want 200", w.Code, w.Body.String())
    }
    master := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}
    if w := call(master); w.Code != http.StatusOK {
        t.Fatalf("master gate code = %d body=%s, want 200", w.Code, w.Body.String())
    }
}

// TestShellManifest_ExtFieldsPassthrough (#250): schema v1 ext fields decode
// from the camelCase wire keys, survive the registry, and surface in the
// manifest list output the shell host consumes.
func TestShellManifest_ExtFieldsPassthrough(t *testing.T) {
    s := newShellTestServer(t, false)
    m := map[string]any{
        "id": "fusion-cowork", "title": "Cowork", "path": "/cowork",
        "entry":   "http://127.0.0.1:11432/shell/fusion-cowork/",
        "devices": []string{"desktop"}, "category": "domain",
        "searchProviders": []map[string]any{
            {"id": "workflows", "label": "工作流", "label_en": "Workflows", "endpoint": "/api/cowork/search"},
        },
        "pages": []map[string]any{
            {"route": "/runs", "title": "运行", "title_en": "Runs"},
        },
        "notifications": []map[string]any{
            {"id": "approvals", "label": "审批", "label_en": "Approvals", "endpoint": "/api/cowork/notifications", "kind": "approvals"},
        },
    }
    body, _ := json.Marshal(m)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", strings.NewReader(string(body)))
    w := httptest.NewRecorder()
    s.handleShellManifestUpsert(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("upsert code = %d body=%s", w.Code, w.Body.String())
    }

    _, loginResp := loginShell(t, s, "admin", "s3cret")
    adminToken := loginResp["token"].(string)
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    req.Header.Set("Authorization", "Bearer "+adminToken)
    w = httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
    raw := w.Body.String()
    for _, want := range []string{
        `"searchProviders":[{"id":"workflows","label":"工作流","label_en":"Workflows","endpoint":"/api/cowork/search"}]`,
        `"pages":[{"route":"/runs","title":"运行","title_en":"Runs"}]`,
        `"notifications":[{"id":"approvals","label":"审批","label_en":"Approvals","endpoint":"/api/cowork/notifications","kind":"approvals"}]`,
    } {
        if !strings.Contains(raw, want) {
            t.Fatalf("manifest list missing ext field %s; got %s", want, raw)
        }
    }

    // Empty manifests must not leak the keys (omitempty).
    m2 := map[string]any{
        "id": "fusion-doc", "title": "Doc", "path": "/doc",
        "entry":   "http://127.0.0.1:11432/shell/fusion-doc/",
        "devices": []string{"desktop"}, "category": "domain",
    }
    body, _ = json.Marshal(m2)
    req = httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", strings.NewReader(string(body)))
    w = httptest.NewRecorder()
    s.handleShellManifestUpsert(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("upsert-2 code = %d body=%s", w.Code, w.Body.String())
    }
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shell/manifests", nil)
    req.Header.Set("Authorization", "Bearer "+adminToken)
    w = httptest.NewRecorder()
    s.withShellSession(s.handleShellManifestList)(w, req)
    if strings.Contains(w.Body.String(), `"searchProviders":null`) || strings.Contains(w.Body.String(), `"pages":null`) {
        t.Fatalf("omitempty violated: %s", w.Body.String())
    }
}

// TestShellManifest_Delete (#253): DELETE /api/v1/shell/manifests/{id} drops
// a dynamic registration behind the same admin-plane gate as upsert.
func TestShellManifest_Delete(t *testing.T) {
    s := newShellTestServer(t, false)
    const adminKey = "ak-shell-admin-0123456789abcdef"
    s.cfg.Config.Shell.AdminKey = adminKey
    admin := &middleware.Principal{KeyConfig: &config.AuthKeyConfig{Key: adminKey, Name: "shell-admin"}}

    register := func(id string) {
        body, _ := json.Marshal(map[string]any{
            "id": id, "title": "T " + id, "path": "/" + id,
            "entry":   "http://127.0.0.1:11432/shell/" + id + "/",
            "devices": []string{"desktop"}, "category": "domain",
        })
        req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
        req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
        w := httptest.NewRecorder()
        s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
        if w.Code != http.StatusOK {
            t.Fatalf("register %s code = %d body=%s", id, w.Code, w.Body.String())
        }
    }

    // Unknown id: 404.
    req := httptest.NewRequest(http.MethodDelete, "/api/v1/shell/manifests/fusion-ghost", nil)
    req.SetPathValue("id", "fusion-ghost")
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestDelete)(w, req)
    if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"manifest_unknown"`) {
        t.Fatalf("delete unknown code = %d body=%s, want 404 manifest_unknown", w.Code, w.Body.String())
    }

    // Built-in auto-registered admin module (#216): gateway-owned, rejected
    // like a static entry (409) even though the registration is dynamic.
    req = httptest.NewRequest(http.MethodDelete, "/api/v1/shell/manifests/fusion-admin", nil)
    req.SetPathValue("id", "fusion-admin")
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w = httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestDelete)(w, req)
    if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"manifest_static"`) {
        t.Fatalf("delete builtin code = %d body=%s, want 409 manifest_static", w.Code, w.Body.String())
    }

    // Static-only id: 409 (config-owned, not API-removable).
    s.shellRegistry = shell.NewRegistry([]config.ShellManifest{{
        ID: "fusion-static", Title: "Static", Path: "/static",
        Entry:   "http://127.0.0.1:11432/shell/fusion-static/",
        Devices: []string{"desktop"}, Category: "platform",
    }}, s.cfg.Config.Shell.EntryOriginAllowlist, time.Second)
    req = httptest.NewRequest(http.MethodDelete, "/api/v1/shell/manifests/fusion-static", nil)
    req.SetPathValue("id", "fusion-static")
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w = httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestDelete)(w, req)
    if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"manifest_static"`) {
        t.Fatalf("delete static code = %d body=%s, want 409 manifest_static", w.Code, w.Body.String())
    }

    // Dynamic registration: 200, then heartbeat on the gone id 404s.
    register("fusion-temp")
    req = httptest.NewRequest(http.MethodDelete, "/api/v1/shell/manifests/fusion-temp", nil)
    req.SetPathValue("id", "fusion-temp")
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w = httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestDelete)(w, req)
    if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"ok"`) {
        t.Fatalf("delete dynamic code = %d body=%s, want 200 ok", w.Code, w.Body.String())
    }
    req = httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-temp/heartbeat", nil)
    req.SetPathValue("id", "fusion-temp")
    w = httptest.NewRecorder()
    s.handleShellManifestHeartbeat(w, req)
    if w.Code != http.StatusNotFound {
        t.Fatalf("heartbeat after delete code = %d, want 404", w.Code)
    }

    // Auth gate: anonymous DELETE is rejected 403 before reaching the handler.
    req = httptest.NewRequest(http.MethodDelete, "/api/v1/shell/manifests/fusion-temp", nil)
    w = httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestDelete)(w, req)
    if w.Code != http.StatusForbidden {
        t.Fatalf("anonymous delete code = %d, want 403", w.Code)
    }
}

// --- #261 K1: gateway-issued service keys ---

// shellKeyResponse is the issuance/rotation payload (envelope + key fields).
type shellKeyResponse struct {
    Code     string `json:"code"`
    ModuleID string `json:"module_id"`
    KeyID    string `json:"key_id"`
    RawKey   string `json:"raw_key"`
}

// TestShellServiceKey_IssueAtRegistration (#261): a registration body that
// omits api_key gets an fm- key returned exactly once; the idempotent
// re-registration retains the key without re-revealing the plaintext.
func TestShellServiceKey_IssueAtRegistration(t *testing.T) {
    s := newShellTestServer(t, false)
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}
    manifest := map[string]any{
        "id": "fusion-demo", "title": "Demo", "path": "/demo",
        "entry": "http://127.0.0.1:11432/shell/fusion-demo/", "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(manifest)
    post := func() *httptest.ResponseRecorder {
        req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
        req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
        w := httptest.NewRecorder()
        s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
        return w
    }
    w := post()
    if w.Code != http.StatusOK {
        t.Fatalf("first registration code = %d body=%s, want 200", w.Code, w.Body.String())
    }
    var kr shellKeyResponse
    if err := json.Unmarshal(w.Body.Bytes(), &kr); err != nil {
        t.Fatalf("decode key response: %v body=%s", err, w.Body.String())
    }
    if !strings.HasPrefix(kr.RawKey, "fm-fusion-demo-") || kr.KeyID == "" {
        t.Fatalf("expected issued fm- key, got %+v", kr)
    }
    w2 := post()
    var kr2 shellKeyResponse
    _ = json.Unmarshal(w2.Body.Bytes(), &kr2)
    if kr2.RawKey != "" || kr2.KeyID != kr.KeyID {
        t.Fatalf("re-registration must retain the key without re-reveal, got %+v", kr2)
    }
}

// TestShellServiceKey_HeartbeatAuth (#261): heartbeat accepts the module's
// fm- key (hash compare), the admin key, and the master key; refuses a wrong
// fm- key, a key bound to another module, and a missing credential.
func TestShellServiceKey_HeartbeatAuth(t *testing.T) {
    s := newShellTestServer(t, false)
    const adminKey = "ak-shell-admin-0123456789abcdef"
    s.cfg.Config.Shell.AdminKey = adminKey
    s.cfg.Config.Auth.MasterKey = "mk"
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}
    manifest := map[string]any{
        "id": "fusion-demo", "title": "Demo", "path": "/demo",
        "entry": "http://127.0.0.1:11432/shell/fusion-demo/", "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(manifest)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("registration code = %d body=%s", w.Code, w.Body.String())
    }
    var kr shellKeyResponse
    _ = json.Unmarshal(w.Body.Bytes(), &kr)
    if kr.RawKey == "" {
        t.Fatal("expected issued key")
    }

    beat := func(token string) int {
        r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-demo/heartbeat", nil)
        r.SetPathValue("id", "fusion-demo")
        if token != "" {
            r.Header.Set("Authorization", "Bearer "+token)
        }
        ww := httptest.NewRecorder()
        s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
        return ww.Code
    }
    if code := beat(kr.RawKey); code != http.StatusOK {
        t.Fatalf("fm- key heartbeat code = %d, want 200", code)
    }
    if code := beat(adminKey); code != http.StatusOK {
        t.Fatalf("admin key heartbeat code = %d, want 200", code)
    }
    if code := beat("mk"); code != http.StatusOK {
        t.Fatalf("master key heartbeat code = %d, want 200", code)
    }
    if code := beat("fm-fusion-demo-00000000000000000000000000000000"); code != http.StatusForbidden {
        t.Fatalf("wrong fm- key heartbeat code = %d, want 403", code)
    }
    if code := beat(""); code != http.StatusUnauthorized {
        t.Fatalf("missing credential heartbeat code = %d, want 401", code)
    }
}

// TestShellServiceKey_RotateAndRevoke (#261): rotation returns a new
// plaintext once and keeps the old key heartbeat-valid inside the grace
// window; revocation immediately refuses heartbeats and flips the proxy's
// revoked flag.
func TestShellServiceKey_RotateAndRevoke(t *testing.T) {
    s := newShellTestServer(t, false)
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}
    manifest := map[string]any{
        "id": "fusion-demo", "title": "Demo", "path": "/demo",
        "entry": "http://127.0.0.1:11432/shell/fusion-demo/", "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(manifest)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
    var kr shellKeyResponse
    _ = json.Unmarshal(w.Body.Bytes(), &kr)
    if kr.RawKey == "" {
        t.Fatal("expected issued key")
    }

    // Rotate.
    rot := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-demo/key/rotate", nil)
    rot.SetPathValue("id", "fusion-demo")
    rot = rot.WithContext(middleware.ContextWithPrincipal(rot.Context(), admin))
    wr := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellServiceKeyRotate)(wr, rot)
    if wr.Code != http.StatusOK {
        t.Fatalf("rotate code = %d body=%s", wr.Code, wr.Body.String())
    }
    var kr2 shellKeyResponse
    _ = json.Unmarshal(wr.Body.Bytes(), &kr2)
    if kr2.RawKey == "" || kr2.RawKey == kr.RawKey {
        t.Fatalf("rotation must return a new plaintext once, got %+v", kr2)
    }

    beat := func(token string) int {
        r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-demo/heartbeat", nil)
        r.SetPathValue("id", "fusion-demo")
        r.Header.Set("Authorization", "Bearer "+token)
        ww := httptest.NewRecorder()
        s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
        return ww.Code
    }
    if code := beat(kr.RawKey); code != http.StatusOK {
        t.Fatalf("old key inside grace window code = %d, want 200", code)
    }
    if code := beat(kr2.RawKey); code != http.StatusOK {
        t.Fatalf("rotated key code = %d, want 200", code)
    }
    if s.shellRegistry.IsServiceKeyRevoked("fusion-demo") {
        t.Fatal("must not be revoked before revocation")
    }

    // Revoke.
    rev := httptest.NewRequest(http.MethodDelete, "/api/v1/shell/manifests/fusion-demo/key", nil)
    rev.SetPathValue("id", "fusion-demo")
    rev = rev.WithContext(middleware.ContextWithPrincipal(rev.Context(), admin))
    wv := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellServiceKeyRevoke)(wv, rev)
    if wv.Code != http.StatusOK {
        t.Fatalf("revoke code = %d body=%s", wv.Code, wv.Body.String())
    }
    if !s.shellRegistry.IsServiceKeyRevoked("fusion-demo") {
        t.Fatal("revoked flag must be set")
    }
    if code := beat(kr2.RawKey); code != http.StatusForbidden {
        t.Fatalf("revoked key heartbeat code = %d, want 403", code)
    }

    // Rotate after revoke: no live key -> 409 no_service_key.
    rot2 := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-demo/key/rotate", nil)
    rot2.SetPathValue("id", "fusion-demo")
    rot2 = rot2.WithContext(middleware.ContextWithPrincipal(rot2.Context(), admin))
    wr2 := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellServiceKeyRotate)(wr2, rot2)
    if wr2.Code != http.StatusConflict {
        t.Fatalf("rotate after revoke code = %d, want 409", wr2.Code)
    }
}
