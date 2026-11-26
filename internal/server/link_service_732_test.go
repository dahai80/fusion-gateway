package server

// #732: config-driven extension of the link service-ticket route allowlist
// (web_shell.link.ticket_routes). The built-in shell task APIs stay
// allowlisted; configured entries add dispatch-executable module routes
// ("METHOD /path" exact, or a literal "/*" suffix = path-prefix glob, same
// semantics as #729 redirect_uris). Admin/auth planes are rejected at
// config-validate time and are never reachable via a ticket.

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// link732Mint mints a valid ticket against the test server secret.
func link732Mint(t *testing.T, s *Server, taskID string) string {
    t.Helper()
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind: shell.LinkTicketKind, Sub: "fusion-link", Tenant: "t1",
        TaskID: taskID, Modules: []string{"*"},
        Exp: time.Now().Add(time.Minute).Unix(),
    })
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    return ticket
}

// link732Call drives a ticket through withShellSession at method+path.
func link732Call(t *testing.T, s *Server, ticket, method, path string) *httptest.ResponseRecorder {
    t.Helper()
    handler := s.withShellSession(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(method, path, nil)
    req.RemoteAddr = "127.0.0.1:4004"
    req.Header.Set("Authorization", "Bearer "+ticket)
    rec := httptest.NewRecorder()
    handler(rec, req)
    return rec
}

func Test732_TicketRouteGlobPrefixAllowsOfficeRoutes(t *testing.T) {
    s := linkTestServer(t, true)
    s.cfgMu.Lock()
    s.cfg.Config.Shell.Link.TicketRoutes = []string{"POST /shell/office-api/v1/*"}
    s.cfgMu.Unlock()
    ticket := link732Mint(t, s, "task-732")

    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/xlsx/parse"); rec.Code != http.StatusOK {
        t.Fatalf("glob prefix route: got %d %s", rec.Code, rec.Body.String())
    }
    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/docx/write"); rec.Code != http.StatusOK {
        t.Fatalf("second glob route: got %d %s", rec.Code, rec.Body.String())
    }
}

func Test732_TicketRouteExactMatchAndMethodScope(t *testing.T) {
    s := linkTestServer(t, true)
    s.cfgMu.Lock()
    s.cfg.Config.Shell.Link.TicketRoutes = []string{"POST /shell/office-api/v1/pptx/write"}
    s.cfgMu.Unlock()
    ticket := link732Mint(t, s, "task-732")

    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/pptx/write"); rec.Code != http.StatusOK {
        t.Fatalf("exact route: got %d %s", rec.Code, rec.Body.String())
    }
    // Exact entry must not become a prefix and must not leak to other
    // methods on the same path.
    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/pptx/write/extra"); rec.Code != http.StatusForbidden {
        t.Fatalf("exact entry over-matched: got %d", rec.Code)
    }
    if rec := link732Call(t, s, ticket, http.MethodGet, "/shell/office-api/v1/pptx/write"); rec.Code != http.StatusForbidden {
        t.Fatalf("wrong method accepted: got %d", rec.Code)
    }
}

func Test732_TicketRouteAdminPlaneUnreachable(t *testing.T) {
    s := linkTestServer(t, true)
    // Even if a deployment somehow got a bad entry past validate (e.g. an
    // older binary reloading a newer config), linkRouteAllowed must never
    // open the admin/auth planes: the glob prefix "/admin/" is checked too.
    s.cfgMu.Lock()
    s.cfg.Config.Shell.Link.TicketRoutes = []string{"POST /shell/office-api/v1/*"}
    s.cfgMu.Unlock()
    ticket := link732Mint(t, s, "task-732")

    for _, p := range []string{"/admin/api/config/auth", "/auth/api/code", "/api/v1/shell/login"} {
        if rec := link732Call(t, s, ticket, http.MethodPost, p); rec.Code == http.StatusOK {
            t.Fatalf("ticket reached protected route %s", p)
        }
    }
}

func Test732_TicketRouteNoConfigStillBuiltinOnly(t *testing.T) {
    s := linkTestServer(t, true)
    ticket := link732Mint(t, s, "task-732")

    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/xlsx/parse"); rec.Code != http.StatusForbidden {
        t.Fatalf("unconfigured module route accepted: got %d", rec.Code)
    }
    if rec := link732Call(t, s, ticket, http.MethodGet, "/api/v1/shell/models"); rec.Code != http.StatusOK {
        t.Fatalf("builtin route broke: got %d %s", rec.Code, rec.Body.String())
    }
}

func Test732_TicketRouteHotReload(t *testing.T) {
    s := linkTestServer(t, true)
    ticket := link732Mint(t, s, "task-732")

    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/xlsx/parse"); rec.Code != http.StatusForbidden {
        t.Fatalf("pre-reload: got %d", rec.Code)
    }
    // Simulate a hot-reload commit: mutate the live snapshot like
    // applyReload does, then the next request must see the new allowlist.
    s.cfgMu.Lock()
    s.cfg.Config.Shell.Link.TicketRoutes = []string{"POST /shell/office-api/v1/*"}
    s.cfgMu.Unlock()
    if rec := link732Call(t, s, ticket, http.MethodPost, "/shell/office-api/v1/xlsx/parse"); rec.Code != http.StatusOK {
        t.Fatalf("post-reload: got %d %s", rec.Code, rec.Body.String())
    }
}

func Test732_TicketRouteBuiltinTable(t *testing.T) {
    link := config.ShellLinkConfig{}
    for _, key := range []string{
        "POST /api/v1/shell/chat/completions",
        "GET /api/v1/shell/me",
        "GET /api/v1/shell/models",
        "GET /api/v1/shell/manifests",
        "GET /api/v1/shell/model-policy",
        "GET /api/v1/model-policy",
    } {
        parts := strings.SplitN(key, " ", 2)
        if !linkRouteAllowed(link, parts[0], parts[1]) {
            t.Fatalf("builtin route lost: %s", key)
        }
    }
    if linkRouteAllowed(link, http.MethodDelete, "/api/v1/shell/models") {
        t.Fatal("wrong method on builtin route accepted")
    }
}
