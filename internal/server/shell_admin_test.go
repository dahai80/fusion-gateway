package server

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// shellAdminRequest issues one E6 admin-surface request with the given
// session token. newShellTestServer(t, false) provides admin / alice (user).
func shellAdminRequest(t *testing.T, s *Server, method, path, token, body string) (int, map[string]any) {
    t.Helper()
    var req *http.Request
    if body == "" {
        req = httptest.NewRequest(method, path, nil)
    } else {
        req = httptest.NewRequest(method, path, strings.NewReader(body))
    }
    if token != "" {
        req.Header.Set("Authorization", "Bearer "+token)
    }
    w := httptest.NewRecorder()
    s.muxForShellAdmin(t).ServeHTTP(w, req)
    var resp map[string]any
    if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
        resp = map[string]any{"_raw": w.Body.String()}
    }
    return w.Code, resp
}

// muxForShellAdmin builds a ServeMux with just the E6 routes so handler
// tests exercise the same method+path patterns production registers.
func (s *Server) muxForShellAdmin(t *testing.T) *http.ServeMux {
    t.Helper()
    mux := http.NewServeMux()
    s.registerShellAdminRoutes(mux)
    return mux
}

func TestShellAdmin_AdaptersVaultRoundTrip(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token, _ := login["token"].(string)

    code, resp := shellAdminRequest(t, s, http.MethodPut, "/api/v1/shell/adapters/wechat", token,
        `{"credentials":{"access_token":"tok-abcd1234"},"rate_limit_rps":5,"jitter_ms":100,"enabled":true}`)
    if code != http.StatusOK || resp["saved"] != true {
        t.Fatalf("put = %d %v", code, resp)
    }
    code, resp = shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/adapters", token, "")
    if code != http.StatusOK {
        t.Fatalf("list = %d %v", code, resp)
    }
    adapters, _ := resp["adapters"].([]any)
    if len(adapters) != 1 {
        t.Fatalf("adapters = %v", resp)
    }
    row, _ := adapters[0].(map[string]any)
    if row["platform"] != "wechat" || row["configured"] != true {
        t.Fatalf("row = %v", row)
    }
    // Plaintext must never round-trip.
    if bytes.Contains([]byte(mustJSON(t, resp)), []byte("tok-abcd1234")) {
        t.Fatal("adapter credential plaintext leaked via list API")
    }
    code, resp = shellAdminRequest(t, s, http.MethodPost, "/api/v1/shell/adapters/wechat/test", token, "")
    if code != http.StatusOK || resp["ok"] != true {
        t.Fatalf("test = %d %v", code, resp)
    }
}

func TestShellAdmin_AdaptersNonAdminForbidden(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "alice", "alicepw")
    token, _ := login["token"].(string)
    code, resp := shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/adapters", token, "")
    if code != http.StatusForbidden {
        t.Fatalf("non-admin adapters = %d %v", code, resp)
    }
}

func TestShellAdmin_ConnectorsPutListTest(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token, _ := login["token"].(string)

    // Unresolvable credential_ref surfaces as a stored problem.
    code, resp := shellAdminRequest(t, s, http.MethodPut, "/api/v1/shell/connectors/crm", token,
        `{"kind":"http","operations":["fetch"],"credential_ref":"ghost","enabled":true}`)
    if code != http.StatusOK || resp["saved"] != true {
        t.Fatalf("put = %d %v", code, resp)
    }
    problems, _ := resp["problems"].([]any)
    if len(problems) != 1 {
        t.Fatalf("problems = %v", resp)
    }
    code, resp = shellAdminRequest(t, s, http.MethodPost, "/api/v1/shell/connectors/crm/test", token, `{}`)
    if code != http.StatusOK || resp["ok"] != false {
        t.Fatalf("test = %d %v", code, resp)
    }
    // Fix the ref after storing the vault entry: problem clears.
    _, _ = shellAdminRequest(t, s, http.MethodPut, "/api/v1/shell/adapters/ghost", token,
        `{"credentials":{"token":"x"},"enabled":true}`)
    code, resp = shellAdminRequest(t, s, http.MethodPost, "/api/v1/shell/connectors/crm/test", token, `{"operation":"fetch"}`)
    if code != http.StatusOK || resp["ok"] != true {
        t.Fatalf("test after fix = %d %v", code, resp)
    }
}

func TestShellAdmin_ConfigOverrides(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token, _ := login["token"].(string)

    code, resp := shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/config/pricing_policies", token, "")
    if code != http.StatusOK {
        t.Fatalf("get = %d %v", code, resp)
    }
    if mustJSON(t, resp["defaults"]) != "{}" || mustJSON(t, resp["overrides"]) != "{}" {
        t.Fatalf("unconfigured = %v", resp)
    }
    code, resp = shellAdminRequest(t, s, http.MethodPut, "/api/v1/shell/config/pricing_policies", token,
        `{"currency":"CNY"}`)
    if code != http.StatusOK {
        t.Fatalf("put = %d %v", code, resp)
    }
    code, resp = shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/config/pricing_policies", token, "")
    if code != http.StatusOK {
        t.Fatalf("get2 = %d %v", code, resp)
    }
    overrides, _ := resp["overrides"].(map[string]any)
    if overrides["currency"] != "CNY" {
        t.Fatalf("override not effective: %v", resp)
    }
}

func TestShellAdmin_AuditRoleGateAndChain(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    adminToken, _ := login["token"].(string)
    _, alogin := loginShell(t, s, "alice", "alicepw")
    aliceToken, _ := alogin["token"].(string)

    // user role: forbidden; admin: ok and sees the login auth rows.
    if code, _ := shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/audit", aliceToken, ""); code != http.StatusForbidden {
        t.Fatalf("user-role audit access = %d", code)
    }
    code, resp := shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/audit", adminToken, "")
    if code != http.StatusOK {
        t.Fatalf("admin audit = %d %v", code, resp)
    }
    entries, _ := resp["entries"].([]any)
    if len(entries) < 2 {
        t.Fatalf("expected login + read rows, got %v", resp)
    }
    sawAuth := false
    for _, e := range entries {
        row, _ := e.(map[string]any)
        if row["action_class"] == "auth" && row["integrity_ok"] == true {
            sawAuth = true
        }
    }
    if !sawAuth {
        t.Fatalf("no intact auth row in %v", resp)
    }
    // Filter by user narrows to that user's rows only.
    code, resp = shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/audit?user=alice", adminToken, "")
    if code != http.StatusOK {
        t.Fatalf("filtered audit = %d", code)
    }
    entries, _ = resp["entries"].([]any)
    for _, e := range entries {
        row, _ := e.(map[string]any)
        if row["user"] != "alice" {
            t.Fatalf("user filter leaked: %v", row)
        }
    }
}

func TestShellAdmin_RolesLifecycle(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token, _ := login["token"].(string)

    code, resp := shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/roles", token, "")
    if code != http.StatusOK {
        t.Fatalf("list = %d %v", code, resp)
    }
    roles, _ := resp["roles"].([]any)
    if len(roles) != 3 {
        t.Fatalf("builtin roles = %v", resp)
    }
    // Built-in collision: 409.
    code, _ = shellAdminRequest(t, s, http.MethodPost, "/api/v1/shell/roles", token,
        `{"name":"admin","write_classes":["read"]}`)
    if code != http.StatusConflict {
        t.Fatalf("builtin collision = %d", code)
    }
    // Custom role create.
    code, _ = shellAdminRequest(t, s, http.MethodPost, "/api/v1/shell/roles", token,
        `{"name":"approver","description":"approvals only","write_classes":["approval","read"]}`)
    if code != http.StatusOK {
        t.Fatalf("create = %d", code)
    }
    // Assign it to alice, then delete must fail with the reason.
    if s.shellUsers == nil {
        t.Fatal("user store missing")
    }
    if err := s.shellUsers.Update("alice", func(a *shell.UserAccount) {
        a.Roles = []string{"user", "approver"}
    }); err != nil {
        t.Fatalf("assign role: %v", err)
    }
    code, resp = shellAdminRequest(t, s, http.MethodDelete, "/api/v1/shell/roles/approver", token, "")
    if code != http.StatusConflict {
        t.Fatalf("in-use delete = %d %v", code, resp)
    }
    if msg, _ := resp["message"].(string); !strings.Contains(msg, "assigned") {
        t.Fatalf("in-use reason = %v", resp)
    }
    // Unassign, delete succeeds.
    _ = s.shellUsers.Update("alice", func(a *shell.UserAccount) {
        a.Roles = []string{"user"}
    })
    code, _ = shellAdminRequest(t, s, http.MethodDelete, "/api/v1/shell/roles/approver", token, "")
    if code != http.StatusOK {
        t.Fatalf("delete = %d", code)
    }
    // Built-in delete: 409.
    code, _ = shellAdminRequest(t, s, http.MethodDelete, "/api/v1/shell/roles/auditor", token, "")
    if code != http.StatusConflict {
        t.Fatalf("builtin delete = %d", code)
    }
}

func TestShellAdmin_MenuPermissions(t *testing.T) {
    s := newShellTestServer(t, false)
    _, login := loginShell(t, s, "admin", "s3cret")
    token, _ := login["token"].(string)

    code, resp := shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/menu-permissions", token, "")
    if code != http.StatusOK {
        t.Fatalf("get = %d %v", code, resp)
    }
    defaults, _ := resp["defaults"].(map[string]any)
    menus, _ := defaults["menus"].([]any)
    if len(menus) == 0 {
        t.Fatalf("defaults must derive from manifests: %v", resp)
    }
    // Unknown role rejected.
    code, _ = shellAdminRequest(t, s, http.MethodPut, "/api/v1/shell/menu-permissions", token,
        `{"menus":[{"id":"m1","label":"M1","href":"/m1","roles":["ghost"]}]}`)
    if code != http.StatusBadRequest {
        t.Fatalf("unknown role = %d", code)
    }
    code, _ = shellAdminRequest(t, s, http.MethodPut, "/api/v1/shell/menu-permissions", token,
        `{"menus":[{"id":"m1","label":"M1","href":"/m1","roles":["admin","auditor"]}]}`)
    if code != http.StatusOK {
        t.Fatalf("put = %d", code)
    }
    code, resp = shellAdminRequest(t, s, http.MethodGet, "/api/v1/shell/menu-permissions", token, "")
    if code != http.StatusOK {
        t.Fatalf("get2 = %d", code)
    }
    overrides, _ := resp["overrides"].(map[string]any)
    omenus, _ := overrides["menus"].([]any)
    if len(omenus) != 1 {
        t.Fatalf("overrides = %v", resp)
    }
}

func mustJSON(t *testing.T, v any) string {
    t.Helper()
    raw, err := json.Marshal(v)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    return string(raw)
}
