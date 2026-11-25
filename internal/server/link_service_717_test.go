package server

// #717: fusion-link service-ticket plane tests. Covers the full contract:
// credential exchange (loopback-only, constant-time), separate token space
// (a session token never validates as a ticket and vice versa), route
// allowlist, E_AUTH on expiry, hot-reload disable, and credential_file
// fail-closed behavior.

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

const linkTestCred = "link-test-credential-0123456789abcdef"

func linkTestServer(t *testing.T, enabled bool) *Server {
    t.Helper()
    s := &Server{}
    s.shellSessions = shell.NewManager([]byte("test-secret"), time.Hour)
    s.shellSessionSecret = []byte("test-secret")
    s.cfg = &config.ConfigSnapshot{Config: config.Config{
        Shell: config.ShellConfig{
            Link: config.ShellLinkConfig{
                Enabled:    enabled,
                Credential: linkTestCred,
                TicketTTL:  time.Minute,
                Tenant:     "t1",
                Modules:    []string{"cowork"},
            },
        },
    }}
    return s
}

func linkMintReq(t *testing.T, s *Server, remote, body string) *httptest.ResponseRecorder {
    t.Helper()
    mux := http.NewServeMux()
    s.registerShellRoutes(mux)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/link/ticket", strings.NewReader(body))
    req.RemoteAddr = remote
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, req)
    return rec
}

func TestLinkTicketIssueRoundtrip(t *testing.T) {
    s := linkTestServer(t, true)
    rec := linkMintReq(t, s, "127.0.0.1:4001",
        `{"credential":"`+linkTestCred+`","task_id":"task-9"}`)
    if rec.Code != http.StatusOK {
        t.Fatalf("issue: got %d body %s", rec.Code, rec.Body.String())
    }
    var out struct {
        Ticket    string `json:"ticket"`
        TokenType string `json:"token_type"`
        ExpiresIn int    `json:"expires_in"`
        TaskID    string `json:"task_id"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if out.TokenType != "bearer" || out.TaskID != "task-9" || out.ExpiresIn != 60 {
        t.Fatalf("unexpected payload: %+v", out)
    }
    claims, err := shell.ValidateLinkTicket(s.shellSessionSecret, out.Ticket)
    if err != nil {
        t.Fatalf("validate: %v", err)
    }
    if claims.TaskID != "task-9" || claims.Tenant != "t1" || claims.Kind != shell.LinkTicketKind {
        t.Fatalf("unexpected claims: %+v", claims)
    }
}

func TestLinkTicketIssueLoopbackOnly(t *testing.T) {
    s := linkTestServer(t, true)
    rec := linkMintReq(t, s, "10.0.0.8:5000",
        `{"credential":"`+linkTestCred+`","task_id":"t"}`)
    if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "loopback_only") {
        t.Fatalf("non-loopback issue: got %d %s", rec.Code, rec.Body.String())
    }
}

func TestLinkTicketIssueDisabled(t *testing.T) {
    s := linkTestServer(t, false)
    rec := linkMintReq(t, s, "127.0.0.1:4002", `{"credential":"x"}`)
    if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "link_disabled") {
        t.Fatalf("disabled issue: got %d %s", rec.Code, rec.Body.String())
    }
}

func TestLinkTicketIssueBadCredential(t *testing.T) {
    s := linkTestServer(t, true)
    rec := linkMintReq(t, s, "127.0.0.1:4003", `{"credential":"wrong-credential-aaaaaaaaaaaaaa"}`)
    if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_credential") {
        t.Fatalf("bad credential: got %d %s", rec.Code, rec.Body.String())
    }
}

func TestLinkTicketAcceptedOnAllowlistedRoute(t *testing.T) {
    s := linkTestServer(t, true)
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind: shell.LinkTicketKind, Sub: "fusion-link", Tenant: "t1",
        TaskID: "task-1", Modules: []string{"cowork"},
        Exp: time.Now().Add(time.Minute).Unix(),
    })
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    var gotSession shell.SessionClaims
    handler := s.withShellSession(func(w http.ResponseWriter, r *http.Request) {
        gotSession = shellClaimsFrom(r.Context())
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req.RemoteAddr = "127.0.0.1:4004"
    req.Header.Set("Authorization", "Bearer "+ticket)
    rec := httptest.NewRecorder()
    handler(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("ticket use: got %d %s", rec.Code, rec.Body.String())
    }
    if gotSession.Origin != "link" || gotSession.Sub != "fusion-link/task-1" ||
        gotSession.TenantID != "t1" || len(gotSession.Roles) != 1 || gotSession.Roles[0] != "viewer" {
        t.Fatalf("unexpected mapped session: %+v", gotSession)
    }
}

func TestLinkTicketSessionTokenSpaceSeparate(t *testing.T) {
    s := linkTestServer(t, true)
    // A real session token must NOT validate as a link ticket (fall through
    // to the session path), and a link ticket must NOT validate as a session.
    sess, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub: "user-1", Roles: []string{"viewer"}, Exp: time.Now().Add(time.Hour).Unix(),
    })
    if err != nil {
        t.Fatalf("issue session: %v", err)
    }
    if _, err := shell.ValidateLinkTicket(s.shellSessionSecret, sess); err == nil {
        t.Fatal("session token validated as link ticket")
    }
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind: shell.LinkTicketKind, Sub: "fusion-link", TaskID: "t",
        Exp: time.Now().Add(time.Minute).Unix(),
    })
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    if _, err := s.shellSessions.Validate(ticket); err == nil {
        t.Fatal("link ticket validated as session token")
    }
}

func TestLinkTicketRouteAllowlist(t *testing.T) {
    s := linkTestServer(t, true)
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind: shell.LinkTicketKind, Sub: "fusion-link", TaskID: "t",
        Exp: time.Now().Add(time.Minute).Unix(),
    })
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    handler := s.withShellSession(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    // A session-plane write route is NOT on the allowlist.
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session/logout", nil)
    req.RemoteAddr = "127.0.0.1:4005"
    req.Header.Set("Authorization", "Bearer "+ticket)
    rec := httptest.NewRecorder()
    handler(rec, req)
    if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "route_not_allowed") {
        t.Fatalf("non-allowlisted route: got %d %s", rec.Code, rec.Body.String())
    }
    // Non-loopback use of a valid ticket is refused.
    req2 := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req2.RemoteAddr = "10.1.2.3:4006"
    req2.Header.Set("Authorization", "Bearer "+ticket)
    rec2 := httptest.NewRecorder()
    handler(rec2, req2)
    if rec2.Code != http.StatusForbidden || !strings.Contains(rec2.Body.String(), "loopback_only") {
        t.Fatalf("non-loopback use: got %d %s", rec2.Code, rec2.Body.String())
    }
}

func TestLinkTicketExpiryVisibleEAuth(t *testing.T) {
    s := linkTestServer(t, true)
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind: shell.LinkTicketKind, Sub: "fusion-link", TaskID: "t",
        Exp: time.Now().Add(-time.Second).Unix(),
    })
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    handler := s.withShellSession(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req.RemoteAddr = "127.0.0.1:4007"
    req.Header.Set("Authorization", "Bearer "+ticket)
    rec := httptest.NewRecorder()
    handler(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("expired ticket: got %d", rec.Code)
    }
    var env struct {
        Code string `json:"code"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != "E_AUTH" {
        t.Fatalf("expired ticket envelope: %s (%v)", rec.Body.String(), err)
    }
}

func TestLinkTicketHotReloadDisable(t *testing.T) {
    s := linkTestServer(t, true)
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind: shell.LinkTicketKind, Sub: "fusion-link", TaskID: "t",
        Exp: time.Now().Add(time.Minute).Unix(),
    })
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    // Flip the live snapshot: disabled plane must reject tickets.
    s.cfg.Config.Shell.Link.Enabled = false
    handler := s.withShellSession(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/me", nil)
    req.RemoteAddr = "127.0.0.1:4008"
    req.Header.Set("Authorization", "Bearer "+ticket)
    rec := httptest.NewRecorder()
    handler(rec, req)
    if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "E_AUTH") {
        t.Fatalf("disabled plane must fall to session error: got %d %s", rec.Code, rec.Body.String())
    }
}

func TestExpandLinkCredentialFile(t *testing.T) {
    dir := t.TempDir()
    good := filepath.Join(dir, "cred")
    if err := os.WriteFile(good, []byte(linkTestCred+"\n"), 0600); err != nil {
        t.Fatal(err)
    }
    if got := expandLinkCredentialFile(config.ShellLinkConfig{
        CredentialFile: good, Enabled: true,
    }); got != linkTestCred {
        t.Fatalf("good file: got %q", got)
    }
    short := filepath.Join(dir, "short")
    _ = os.WriteFile(short, []byte("too-short"), 0600)
    if got := expandLinkCredentialFile(config.ShellLinkConfig{
        CredentialFile: short, Enabled: true,
    }); got != "" {
        t.Fatalf("short file must disable: got %q", got)
    }
    if got := expandLinkCredentialFile(config.ShellLinkConfig{
        CredentialFile: filepath.Join(dir, "missing"), Enabled: true,
    }); got != "" {
        t.Fatalf("missing file must disable: got %q", got)
    }
    // Explicit Credential wins over file.
    if got := expandLinkCredentialFile(config.ShellLinkConfig{
        Credential: linkTestCred, CredentialFile: good, Enabled: true,
    }); got != "" {
        t.Fatalf("credential must win over file: got %q", got)
    }
}
