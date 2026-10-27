package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func newShellCfg(t *testing.T) *config.ConfigSnapshot {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Shell.Enabled = true
    cfg.Config.Shell.Session.Secret = "test-shell-secret-at-least-32-chars!"
    cfg.Config.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    return cfg
}

func shellRequest(t *testing.T, s *Server, token, method, path, body string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(method, path, strings.NewReader(body))
    if token != "" {
        req.Header.Set("Authorization", "Bearer "+token)
    }
    w := httptest.NewRecorder()
    s.muxShellTest(w, req)
    return w
}

// dispatch a request through withShellSession + requireShellAdmin chain as
// registered on the mux, without needing a live mux instance.
func (s *Server) muxShellTest(w *httptest.ResponseRecorder, r *http.Request) {
    // httptest.NewRequest doesn't route through ServeMux, so the {id} path
    // parameter the real mux would bind is injected via SetPathValue here.
    id := ""
    if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/shell/users/"); ok {
        rest = strings.TrimSuffix(rest, "/password-reset")
        id = strings.Trim(rest, "/")
    }
    r.SetPathValue("id", id)
    switch {
    case strings.HasPrefix(r.URL.Path, "/api/v1/shell/users") && r.Method == http.MethodGet:
        s.withShellSession(requireShellAdmin(s.handleShellUserList))(w, r)
    case r.Method == http.MethodPost && r.URL.Path == "/api/v1/shell/users":
        s.withShellSession(requireShellAdmin(s.handleShellUserCreate))(w, r)
    case strings.HasPrefix(r.URL.Path, "/api/v1/shell/users/") && r.Method == http.MethodPatch:
        s.withShellSession(requireShellAdmin(s.handleShellUserUpdate))(w, r)
    case strings.HasPrefix(r.URL.Path, "/api/v1/shell/users/") && r.Method == http.MethodDelete:
        s.withShellSession(requireShellAdmin(s.handleShellUserDelete))(w, r)
    case strings.HasPrefix(r.URL.Path, "/api/v1/shell/users/") && strings.HasSuffix(r.URL.Path, "/password-reset") && r.Method == http.MethodPost:
        s.withShellSession(requireShellAdmin(s.handleShellUserPasswordReset))(w, r)
    case r.Method == http.MethodPost && r.URL.Path == "/api/v1/password":
        s.withShellSession(s.handleShellPasswordChange)(w, r)
    default:
        w.WriteHeader(http.StatusNotFound)
    }
}

func decodeJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
    t.Helper()
    var v T
    if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
        t.Fatalf("decode %s: %v", w.Body.String(), err)
    }
    return v
}

func TestShellUsers_CRUDAndGuards(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token := login["token"].(string)

    // Create a user.
    w := shellRequest(t, s, token, http.MethodPost, "/api/v1/shell/users",
        `{"username":"bob","display_name":"Bob","password":"bobpassword","permissions":["fusion-mlx"]}`)
    if w.Code != http.StatusCreated {
        t.Fatalf("create code = %d body=%s", w.Code, w.Body.String())
    }
    row := decodeJSON[shellAdminRow](t, w)
    if row.UserID != "bob" || row.Roles[0] != "user" || row.Permissions[0] != "fusion-mlx" {
        t.Fatalf("created row = %+v", row)
    }

    // Duplicate create rejected.
    w = shellRequest(t, s, token, http.MethodPost, "/api/v1/shell/users",
        `{"username":"bob","password":"bobpassword2","permissions":[]}`)
    if w.Code != http.StatusConflict {
        t.Fatalf("duplicate create code = %d", w.Code)
    }

    // Bob can log in (password hashed at create).
    code, _ := loginShell(t, s, "bob", "bobpassword")
    if code != http.StatusOK {
        t.Fatalf("bob login code = %d", code)
    }

    // List shows config-seeded + created users.
    w = shellRequest(t, s, token, http.MethodGet, "/api/v1/shell/users", "")
    if w.Code != http.StatusOK {
        t.Fatalf("list code = %d", w.Code)
    }
    rows := decodeJSON[[]shellAdminRow](t, w)
    if len(rows) != 3 {
        t.Fatalf("list rows = %+v", rows)
    }

    // Non-admin denied (EI9: server-side role check).
    _, alice := loginShell(t, s, "alice", "alicepw")
    w = shellRequest(t, s, alice["token"].(string), http.MethodGet, "/api/v1/shell/users", "")
    if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
        t.Fatalf("non-admin list code = %d body=%s", w.Code, w.Body.String())
    }

    // Grant update (permissions patch).
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/bob", `{"permissions":["fusion-mlx","fusion-kb"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("patch code = %d body=%s", w.Code, w.Body.String())
    }
    if row := decodeJSON[shellAdminRow](t, w); len(row.Permissions) != 2 {
        t.Fatalf("patched permissions = %+v", row)
    }

    // Password reset mints a usable temp password + forces change.
    w = shellRequest(t, s, token, http.MethodPost, "/api/v1/shell/users/bob/password-reset", "")
    if w.Code != http.StatusOK {
        t.Fatalf("reset code = %d", w.Code)
    }
    temp := decodeJSON[map[string]string](t, w)["temp_password"]
    if temp == "" {
        t.Fatal("empty temp password")
    }
    code, bresp := loginShell(t, s, "bob", temp)
    if code != http.StatusOK || bresp["must_change_password"] != true {
        t.Fatalf("temp login code=%d resp=%v", code, bresp)
    }

    // Self password change (Login.tsx changePw flow) clears the flag in /me.
    _, blogin := loginShell(t, s, "bob", temp)
    btoken := blogin["token"].(string)
    w = shellRequest(t, s, btoken, http.MethodPost, "/api/v1/password", `{"password":"bobnewpassword"}`)
    if w.Code != http.StatusOK {
        t.Fatalf("password change code = %d body=%s", w.Code, w.Body.String())
    }
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req.Header.Set("Authorization", "Bearer "+btoken)
    w2 := httptest.NewRecorder()
    s.withShellSession(s.handleShellMe)(w2, req)
    me := decodeJSON[map[string]any](t, w2)
    if me["must_change_password"] != false || me["user_id"] != "bob" {
        t.Fatalf("me after change = %v", me)
    }
    code, _ = loginShell(t, s, "bob", "bobnewpassword")
    if code != http.StatusOK {
        t.Fatalf("login with new password code = %d", code)
    }

    // Delete user.
    w = shellRequest(t, s, token, http.MethodDelete, "/api/v1/shell/users/bob", "")
    if w.Code != http.StatusNoContent {
        t.Fatalf("delete code = %d", w.Code)
    }
    code, _ = loginShell(t, s, "bob", "bobnewpassword")
    if code != http.StatusUnauthorized {
        t.Fatalf("deleted user login code = %d, want 401", code)
    }
}

func TestShellUsers_LastAdminGuard(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token := login["token"].(string)

    // Disable last admin rejected.
    w := shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/admin", `{"disabled":true}`)
    if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"last_admin"`) {
        t.Fatalf("disable last admin code = %d body=%s", w.Code, w.Body.String())
    }
    // Demote last admin rejected.
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/admin", `{"roles":["user"]}`)
    if w.Code != http.StatusConflict {
        t.Fatalf("demote last admin code = %d", w.Code)
    }
    // Delete last admin rejected.
    w = shellRequest(t, s, token, http.MethodDelete, "/api/v1/shell/users/admin", "")
    if w.Code != http.StatusConflict {
        t.Fatalf("delete last admin code = %d", w.Code)
    }
    // Second admin makes all of the above legal again.
    w = shellRequest(t, s, token, http.MethodPost, "/api/v1/shell/users",
        `{"username":"root2","password":"root2password","permissions":[]}`)
    if w.Code != http.StatusCreated {
        t.Fatalf("second admin create code = %d", w.Code)
    }
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/root2", `{"roles":["admin"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("promote root2 code = %d", w.Code)
    }
    w = shellRequest(t, s, token, http.MethodDelete, "/api/v1/shell/users/admin", "")
    if w.Code != http.StatusNoContent {
        t.Fatalf("delete admin after second admin code = %d", w.Code)
    }
}

func TestShellUsers_BootstrapPasswordRotation(t *testing.T) {
    s := newShellTestServer(t, true)
    code, resp := loginShell(t, s, "admin", s.shellBootstrap.password)
    if code != http.StatusOK || resp["must_change_password"] != true {
        t.Fatalf("bootstrap login code=%d resp=%v", code, resp)
    }
    token := resp["token"].(string)
    // Login shape now carries the full SessionUser (endpoints.ts contract).
    for _, key := range []string{"user_id", "display_name", "roles", "permissions", "token", "must_change_password"} {
        if _, ok := resp[key]; !ok {
            t.Fatalf("login response missing %s: %v", key, resp)
        }
    }
    w := shellRequest(t, s, token, http.MethodPost, "/api/v1/password", `{"password":"rotatedpassword"}`)
    if w.Code != http.StatusOK {
        t.Fatalf("bootstrap rotation code = %d body=%s", w.Code, w.Body.String())
    }
    code, _ = loginShell(t, s, "admin", "rotatedpassword")
    if code != http.StatusOK {
        t.Fatalf("post-rotation login code = %d", code)
    }
}

func TestShellUsers_RoleValidation(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token := login["token"].(string)

    // Target user for role patches.
    w := shellRequest(t, s, token, http.MethodPost, "/api/v1/shell/users",
        `{"username":"carol","password":"carolpassword","permissions":[]}`)
    if w.Code != http.StatusCreated {
        t.Fatalf("create carol code = %d body=%s", w.Code, w.Body.String())
    }

    // PATCH with a phantom role is rejected 400 role_rejected (#359).
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/carol", `{"roles":["nonexistent-role"]}`)
    if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"role_rejected"`) {
        t.Fatalf("phantom role patch code = %d body=%s", w.Code, w.Body.String())
    }
    if !strings.Contains(w.Body.String(), "nonexistent-role") {
        t.Fatalf("rejection must list the unknown role, body=%s", w.Body.String())
    }
    // The account must be untouched by the rejected patch.
    w = shellRequest(t, s, token, http.MethodGet, "/api/v1/shell/users", "")
    rows := decodeJSON[[]shellAdminRow](t, w)
    for _, row := range rows {
        if row.UserID == "carol" && !sameRoles(row.Roles, []string{"user"}) {
            t.Fatalf("carol roles mutated by rejected patch: %+v", row.Roles)
        }
    }

    // Mixed valid + invalid lists the unknown names only.
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/carol", `{"roles":["auditor","ghost-role"]}`)
    if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "ghost-role") {
        t.Fatalf("mixed role patch code = %d body=%s", w.Code, w.Body.String())
    }

    // Built-in roles still assign cleanly.
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/carol", `{"roles":["auditor"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("builtin role patch code = %d body=%s", w.Code, w.Body.String())
    }
    if row := decodeJSON[shellAdminRow](t, w); !sameRoles(row.Roles, []string{"auditor"}) {
        t.Fatalf("carol roles = %+v", row.Roles)
    }

    // Custom roles created through the RoleStore resolve too.
    if err := s.shellRoles.Create("ops", "e2e ops role", []string{"read", "write"}); err != nil {
        t.Fatalf("custom role create: %v", err)
    }
    w = shellRequest(t, s, token, http.MethodPatch, "/api/v1/shell/users/carol", `{"roles":["ops"]}`)
    if w.Code != http.StatusOK {
        t.Fatalf("custom role patch code = %d body=%s", w.Code, w.Body.String())
    }
}

func sameRoles(got, want []string) bool {
    if len(got) != len(want) {
        return false
    }
    for i := range got {
        if got[i] != want[i] {
            return false
        }
    }
    return true
}

func TestShellUsers_StorePersistence(t *testing.T) {
    dir := t.TempDir()
    cfg := newShellCfg(t)
    cfg.Config.Store.DataDir = dir
    s := &Server{cfg: cfg}
    s.setupShell()
    _, resp := loginShell(t, s, "admin", s.shellBootstrap.password)
    token := resp["token"].(string)
    w := shellRequest(t, s, token, http.MethodPost, "/api/v1/shell/users",
        `{"username":"persisted","password":"persistedpw","permissions":["fusion-mlx"]}`)
    if w.Code != http.StatusCreated {
        t.Fatalf("create code = %d", w.Code)
    }

    // Fresh server over the same data dir: created user survives, bootstrap
    // password change persists (must_change cleared on disk).
    w = shellRequest(t, s, token, http.MethodPost, "/api/v1/password", `{"password":"adminrotated2"}`)
    if w.Code != http.StatusOK {
        t.Fatalf("rotation code = %d", w.Code)
    }
    s2 := &Server{cfg: cfg}
    s2.setupShell()
    code, _ := loginShell(t, s2, "persisted", "persistedpw")
    if code != http.StatusOK {
        t.Fatalf("persisted user login code = %d", code)
    }
    code, _ = loginShell(t, s2, "admin", "adminrotated2")
    if code != http.StatusOK {
        t.Fatalf("rotated bootstrap login code = %d", code)
    }
    // Store file must not carry plaintext passwords.
    raw, err := os.ReadFile(filepath.Join(dir, "shell_users.json"))
    if err != nil {
        t.Fatalf("store file: %v", err)
    }
    if strings.Contains(string(raw), "persistedpw") || strings.Contains(string(raw), "adminrotated2") {
        t.Fatal("plaintext password found in store file")
    }
}
