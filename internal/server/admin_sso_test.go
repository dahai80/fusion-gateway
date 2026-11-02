package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/admin"
)

// ssoServer wires a shell test server with the admin module enabled — the
// SSO bridge needs a live AdminAuth to issue the JWT cookie (#471).
func ssoServer(t *testing.T) *Server {
    t.Helper()
    s := newShellTestServer(t, false)
    aa, err := admin.NewAdminAuth("test-admin-jwt-secret-32-characters!", map[string]string{"admin": "adminpw"})
    if err != nil {
        t.Fatalf("admin auth: %v", err)
    }
    aa.SetInsecureCookie(true)
    s.adminAuth = aa
    return s
}

// ssoCall posts to the SSO endpoint with the given Authorization header and
// shell session cookie and returns the recorder.
func ssoCall(t *testing.T, s *Server, bearer, sessionCookie string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, "/admin/api/sso", nil)
    if bearer != "" {
        req.Header.Set("Authorization", "Bearer "+bearer)
    }
    if sessionCookie != "" {
        req.AddCookie(&http.Cookie{Name: shellSessionCookieName, Value: sessionCookie})
    }
    w := httptest.NewRecorder()
    s.handleAdminSSO(w, req)
    return w
}

// TestAdminSSO_ShellSessionIssuesAdminCookie: an admin-role shell session
// exchanges for the admin JWT cookie in one round-trip — the embedded
// console iframe must not demand a second login (#471). The issued cookie
// authenticates a withAdminOnly route.
func TestAdminSSO_ShellSessionIssuesAdminCookie(t *testing.T) {
    s := ssoServer(t)
    code, resp := loginShell(t, s, "admin", "s3cret")
    if code != http.StatusOK {
        t.Fatalf("shell login code = %d", code)
    }
    tok, _ := resp["token"].(string)
    if tok == "" {
        t.Fatalf("no shell session token in response: %v", resp)
    }

    w := ssoCall(t, s, tok, "")
    if w.Code != http.StatusOK {
        t.Fatalf("sso code = %d body=%s", w.Code, w.Body.String())
    }
    var adminToken *http.Cookie
    for _, c := range w.Result().Cookies() {
        if c.Name == "admin_token" && c.Value != "" {
            adminToken = c
        }
    }
    if adminToken == nil {
        t.Fatal("SSO must set the admin_token cookie")
    }

    // The issued cookie is a valid admin-module JWT (the /admin/api/*
    // middleware reads it directly) — same contract as /admin/api/login.
    claims, err := s.adminAuth.ValidateToken(adminToken.Value)
    if err != nil || claims == nil {
        t.Fatalf("SSO cookie rejected by admin auth: err=%v claims=%v", err, claims)
    }
    if claims.Role != "admin" || claims.Username != "admin" {
        t.Fatalf("SSO cookie claims = %+v, want admin/admin", claims)
    }
}

// TestAdminSSO_CookieFallback: the shell session cookie alone (no Bearer)
// is accepted — the iframe's first request carries the shared-origin cookie.
func TestAdminSSO_CookieFallback(t *testing.T) {
    s := ssoServer(t)
    code, resp := loginShell(t, s, "admin", "s3cret")
    if code != http.StatusOK {
        t.Fatalf("shell login code = %d", code)
    }
    tok, _ := resp["token"].(string)

    w := ssoCall(t, s, "", tok)
    if w.Code != http.StatusOK {
        t.Fatalf("sso via cookie code = %d body=%s", w.Code, w.Body.String())
    }
    var adminToken *http.Cookie
    for _, c := range w.Result().Cookies() {
        if c.Name == "admin_token" && c.Value != "" {
            adminToken = c
        }
    }
    if adminToken == nil {
        t.Fatal("cookie-fallback SSO must set the admin_token cookie")
    }
}

// TestAdminSSO_NonAdminDenied: a non-admin shell session gets 403 — the SSO
// bridge must never elevate a plain user (same live-store check as
// bridgeShellSession).
func TestAdminSSO_NonAdminDenied(t *testing.T) {
    s := ssoServer(t)
    code, resp := loginShell(t, s, "alice", "alicepw")
    if code != http.StatusOK {
        t.Fatalf("shell login code = %d", code)
    }
    tok, _ := resp["token"].(string)

    w := ssoCall(t, s, tok, "")
    if w.Code != http.StatusForbidden {
        t.Fatalf("non-admin sso code = %d, want 403", w.Code)
    }
    if strings.Contains(w.Body.String(), "admin_token") {
        t.Fatal("non-admin SSO must not set the admin cookie")
    }
}

// TestAdminSSO_NoSessionDenied: no credential at all → 401 (fail-closed);
// a garbage token is likewise refused.
func TestAdminSSO_NoSessionDenied(t *testing.T) {
    s := ssoServer(t)
    if w := ssoCall(t, s, "", ""); w.Code != http.StatusUnauthorized {
        t.Fatalf("anonymous sso code = %d, want 401", w.Code)
    }
    if w := ssoCall(t, s, "not-a-real-token", ""); w.Code != http.StatusUnauthorized {
        t.Fatalf("garbage token sso code = %d, want 401", w.Code)
    }
}

// TestAdminSSO_AdminModuleDisabled: without a configured admin module the
// bridge serves 503 instead of pretending success — the SPA falls back to
// the classic login page.
func TestAdminSSO_AdminModuleDisabled(t *testing.T) {
    s := newShellTestServer(t, false) // no adminAuth wired
    code, resp := loginShell(t, s, "admin", "s3cret")
    if code != http.StatusOK {
        t.Fatalf("shell login code = %d", code)
    }
    tok, _ := resp["token"].(string)
    w := ssoCall(t, s, tok, "")
    if w.Code != http.StatusServiceUnavailable {
        t.Fatalf("disabled admin module sso code = %d, want 503", w.Code)
    }
}
