package server

// #517: shell SSO ticket exchange tests. The identity upstream is an
// httptest server returning the fusion-identity TokenResponse shape; the
// access token is a hand-built JWT (payload-only — the gateway decodes
// claims without verification by design).

import (
    "encoding/base64"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

func ssoJWT(t *testing.T, sub, tid, role string) string {
    t.Helper()
    payload, _ := json.Marshal(map[string]string{"sub": sub, "tid": tid, "role": role})
    return "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
}

// ssoTestServer wires a Server with shell sessions enabled and an identity
// upstream that answers per the given handler.
func ssoTestServer(t *testing.T, identity http.HandlerFunc) (*Server, *httptest.Server) {
    t.Helper()
    idSrv := httptest.NewServer(identity)
    t.Cleanup(idSrv.Close)
    s := &Server{}
    s.shellSessions = shell.NewManager([]byte("test-secret"), time.Hour)
    s.cfg = &config.ConfigSnapshot{Config: config.Config{
        Shell: config.ShellConfig{IdentityURL: idSrv.URL},
    }}
    return s, idSrv
}

func ssoPost(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
    t.Helper()
    mux := http.NewServeMux()
    s.registerShellRoutes(mux)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session/sso", strings.NewReader(body))
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, req)
    return rec
}

func TestShellSSOTicketExchanged(t *testing.T) {
    exchCalls := 0
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/ticket/exchange" {
            w.WriteHeader(http.StatusNotFound)
            return
        }
        var body struct {
            Ticket string `json:"ticket"`
        }
        _ = json.NewDecoder(r.Body).Decode(&body)
        if body.Ticket != "good-ticket" {
            w.WriteHeader(http.StatusUnauthorized)
            _ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid or expired ticket"})
            return
        }
        exchCalls++
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token":  ssoJWT(t, "user-9", "tenant-7", "member"),
            "refresh_token": "r",
            "token_type":    "Bearer",
            "expires_in":    3600,
        })
    })
    rec := ssoPost(t, s, `{"ticket":"good-ticket"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
    }
    if exchCalls != 1 {
        t.Fatalf("exchange calls = %d", exchCalls)
    }
    var out struct {
        Token         string   `json:"token"`
        IdentityToken string   `json:"identity_token"`
        Username      string   `json:"username"`
        Roles         []string `json:"roles"`
        TenantID      string   `json:"tenant_id"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
        t.Fatalf("body: %v", err)
    }
    if out.Token == "" || out.IdentityToken == "" {
        t.Fatalf("missing tokens: %+v", out)
    }
    if out.Username != "user-9" || out.TenantID != "tenant-7" {
        t.Fatalf("identity mapping wrong: %+v", out)
    }
    // Identity "member" maps to shell viewer — never elevated.
    if len(out.Roles) != 1 || out.Roles[0] != "viewer" {
        t.Fatalf("roles = %v, want [viewer]", out.Roles)
    }
    // Session token validates with the mapped claims.
    claims, err := s.shellSessions.Validate(out.Token)
    if err != nil {
        t.Fatalf("validate: %v", err)
    }
    if claims.Sub != "user-9" || claims.TenantID != "tenant-7" {
        t.Fatalf("session claims = %+v", claims)
    }
    if len(claims.Modules) != 1 || claims.Modules[0] != "*" {
        t.Fatalf("modules = %v, want default [\"*\"]", claims.Modules)
    }
    // Session cookie set for iframe navigation (#220 parity).
    if len(rec.Result().Cookies()) == 0 {
        t.Fatalf("no session cookie set")
    }
}

func TestShellSSOAdminRoleMaps(t *testing.T) {
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token":  ssoJWT(t, "boss", "t1", "admin"),
            "refresh_token": "r",
            "expires_in":    60,
        })
    })
    rec := ssoPost(t, s, `{"ticket":"t"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d", rec.Code)
    }
    var out struct {
        Roles []string `json:"roles"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &out)
    if len(out.Roles) != 1 || out.Roles[0] != "admin" {
        t.Fatalf("roles = %v, want [admin]", out.Roles)
    }
}

// #538: fusion-identity's admin-class role is tenant_admin (its roles table:
// tenant_admin / operator / member / viewer). An identity tenant admin must
// land on shell admin, else no SSO principal can ever reach the
// requireShellAdmin surfaces (PUT /v1/model-policy) for their own tenant.
func TestShellSSOTenantAdminRoleMaps(t *testing.T) {
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token":  ssoJWT(t, "boss", "t1", "tenant_admin"),
            "refresh_token": "r",
            "expires_in":    60,
        })
    })
    rec := ssoPost(t, s, `{"ticket":"t"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d", rec.Code)
    }
    var out struct {
        Roles []string `json:"roles"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &out)
    if len(out.Roles) != 1 || out.Roles[0] != "admin" {
        t.Fatalf("roles = %v, want [admin]", out.Roles)
    }
}

// Non-admin identity roles keep landing on viewer — operator is a
// privileged-but-not-admin identity role and must not gain shell admin.
func TestShellSSOOperatorRoleStaysViewer(t *testing.T) {
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token":  ssoJWT(t, "ops", "t1", "operator"),
            "refresh_token": "r",
            "expires_in":    60,
        })
    })
    rec := ssoPost(t, s, `{"ticket":"t"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d", rec.Code)
    }
    var out struct {
        Roles []string `json:"roles"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &out)
    if len(out.Roles) != 1 || out.Roles[0] != "viewer" {
        t.Fatalf("roles = %v, want [viewer]", out.Roles)
    }
}

func TestShellSSOReplayedTicket401(t *testing.T) {
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        // Identity consumed-ticket semantics: single-use, replay = 401.
        w.WriteHeader(http.StatusUnauthorized)
        _ = json.NewEncoder(w).Encode(map[string]string{"detail": "ticket already used"})
    })
    rec := ssoPost(t, s, `{"ticket":"replayed"}`)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
    var out map[string]any
    _ = json.Unmarshal(rec.Body.Bytes(), &out)
    if out["code"] != "invalid_ticket" {
        t.Fatalf("expected invalid_ticket envelope, got %s", rec.Body.String())
    }
}

func TestShellSSOIdentityDown503Shaped401ForClient(t *testing.T) {
    s, idSrv := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    idSrv.Close() // upstream gone
    rec := ssoPost(t, s, `{"ticket":"t"}`)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401 (client sees invalid_ticket, upstream failure logged)", rec.Code)
    }
}

func TestShellSSOEmptyTicket400(t *testing.T) {
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("identity must not be called")
    })
    rec := ssoPost(t, s, `{}`)
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("code = %d, want 400", rec.Code)
    }
}

func TestShellSSODisabledShell404(t *testing.T) {
    s := &Server{}
    rec := ssoPost(t, s, `{"ticket":"t"}`)
    if rec.Code != http.StatusNotFound {
        t.Fatalf("code = %d, want 404", rec.Code)
    }
}

func TestShellSSOMissingSub401(t *testing.T) {
    s, _ := ssoTestServer(t, func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token":  ssoJWT(t, "", "t1", "member"),
            "refresh_token": "r",
            "expires_in":    60,
        })
    })
    rec := ssoPost(t, s, `{"ticket":"t"}`)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
}

func TestIdentityBaseURLFallbackChain(t *testing.T) {
    if got := identityBaseURL("", ""); got != "http://127.0.0.1:11470" {
        t.Fatalf("default = %q", got)
    }
    if got := identityBaseURL("", "http://portal:1"); got != "http://portal:1" {
        t.Fatalf("portal fallback = %q", got)
    }
    if got := identityBaseURL("http://shell:2", "http://portal:1"); got != "http://shell:2" {
        t.Fatalf("shell override = %q", got)
    }
}
