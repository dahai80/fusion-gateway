package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

// #684 (identity#90/#91): the portal identity-proxy plane must accept the
// /auth-less alias path shape (…/api/v1/password/reset-request) and forward
// it to identity's canonical /api/v1/auth/... route — regression:
// through-gateway 404 while direct identity answered.

func Test684_PasswordResetRequestAliasReachesCanonicalRoute(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/password/reset-request" {
            t.Errorf("upstream path = %q, want canonical /api/v1/auth/...", r.URL.Path)
        }
        _, _ = w.Write([]byte(`{"sent":true}`))
    })
    rec := post682(t, p, "/auth/api/identity/api/v1/password/reset-request",
        `{"email":"ghost@example.com"}`, nil)
    if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sent":true`) {
        t.Fatalf("alias reset-request through gateway: got %d %s", rec.Code, rec.Body.String())
    }
}

func Test684_PasswordForgotAndResetAliases(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/api/v1/auth/password/forgot", "/api/v1/auth/password/reset":
            _, _ = w.Write([]byte(`{"ok":true}`))
        default:
            t.Errorf("upstream path = %q", r.URL.Path)
            w.WriteHeader(http.StatusNotFound)
        }
    })
    for _, path := range []string{
        "/auth/api/identity/api/v1/password/forgot",
        "/auth/api/identity/api/v1/password/reset",
    } {
        rec := post682(t, p, path, `{"email":"a@example.com"}`, nil)
        if rec.Code != http.StatusOK {
            t.Fatalf("%s: got %d %s", path, rec.Code, rec.Body.String())
        }
    }
}

func Test684_AccountExportAliasForwardsBearerAnd401(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/account/export" || r.Method != http.MethodGet {
            t.Errorf("upstream saw %s %s", r.Method, r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer tok-684" {
            w.WriteHeader(http.StatusUnauthorized)
            return
        }
        _, _ = w.Write([]byte(`{"user":"usr_684"}`))
    })
    req := httptest.NewRequest(http.MethodGet, "/auth/api/identity/api/v1/account/export", nil)
    req.Header.Set("Authorization", "Bearer tok-684")
    rec := httptest.NewRecorder()
    p.handleIdentityProxy(rec, req)
    if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "usr_684") {
        t.Fatalf("alias account/export with bearer: got %d %s", rec.Code, rec.Body.String())
    }

    req2 := httptest.NewRequest(http.MethodGet, "/auth/api/identity/api/v1/account/export", nil)
    rec2 := httptest.NewRecorder()
    p.handleIdentityProxy(rec2, req2)
    if rec2.Code != http.StatusUnauthorized {
        t.Fatalf("alias account/export without bearer must pass through 401, got %d", rec2.Code)
    }
}

func Test684_AccountDeleteAlias(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/account/delete" || r.Method != http.MethodPost {
            t.Errorf("upstream saw %s %s", r.Method, r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer tok-684" {
            w.WriteHeader(http.StatusUnauthorized)
            return
        }
        _, _ = w.Write([]byte(`{"purge_at":"2026-11-25"}`))
    })
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/account/delete",
        strings.NewReader(`{"password":"pw"}`))
    req.Header.Set("Authorization", "Bearer tok-684")
    rec := httptest.NewRecorder()
    p.handleIdentityProxy(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("alias account/delete: got %d %s", rec.Code, rec.Body.String())
    }
}

func Test684_AliasesDoNotWidenSurface(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        t.Error("non-allowlisted path must not reach identity")
    })
    for _, path := range []string{
        "/auth/api/identity/api/v1/login",
        "/auth/api/identity/api/v1/admin/tenants/provision",
        "/auth/api/identity/api/v1/password/reset2",
    } {
        rec := post682(t, p, path, `{}`, nil)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("%s: got %d, want 404", path, rec.Code)
        }
    }
}
