package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #682 (identity#90/#91): the portal identity-proxy plane must reach the
// new password reset-request alias and the bearer-authenticated account
// lifecycle routes — regression: through-gateway 404 while direct identity
// answered.

func new682Portal(t *testing.T, handler http.HandlerFunc) *authPortal {
    t.Helper()
    upstream := httptest.NewServer(handler)
    t.Cleanup(upstream.Close)
    target, err := url.Parse(upstream.URL)
    if err != nil {
        t.Fatalf("parse upstream url: %v", err)
    }
    return &authPortal{
        cfg: config.AuthPortalConfig{
            Enabled:      true,
            RedirectURIs: []string{"http://localhost:3000/login"},
            CodeTTLSecs:  60,
            RatePerMin:   1000,
        },
        identity:   target,
        httpClient: upstream.Client(),
        codes:      make(map[string]*authCodeGrant),
        hits:       make(map[string][]time.Time),
    }
}

func post682(t *testing.T, p *authPortal, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
    for k, v := range hdr {
        req.Header.Set(k, v)
    }
    rec := httptest.NewRecorder()
    p.handleIdentityProxy(rec, req)
    return rec
}

func Test682_ResetRequestAllowlisted(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/password/reset-request" {
            t.Errorf("upstream path = %q", r.URL.Path)
        }
        _ = json.NewEncoder(w).Encode(map[string]bool{"sent": true})
    })
    rec := post682(t, p, "/auth/api/identity/api/v1/auth/password/reset-request",
        `{"email":"ghost@example.com"}`, nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("reset-request through gateway: got %d %s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), `"sent":true`) {
        t.Fatalf("identity response not passed through: %s", rec.Body.String())
    }
}

func Test682_AccountExportForwardsBearer(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/account/export" || r.Method != http.MethodGet {
            t.Errorf("upstream saw %s %s", r.Method, r.URL.Path)
        }
        if r.Header.Get("Authorization") != "Bearer tok-123" {
            w.WriteHeader(http.StatusUnauthorized)
            return
        }
        _ = json.NewEncoder(w).Encode(map[string]string{"user": "usr_test"})
    })
    req := httptest.NewRequest(http.MethodGet, "/auth/api/identity/api/v1/auth/account/export", nil)
    req.Header.Set("Authorization", "Bearer tok-123")
    rec := httptest.NewRecorder()
    p.handleIdentityProxy(rec, req)
    if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "usr_test") {
        t.Fatalf("bearer account export must reach identity: %d %s", rec.Code, rec.Body.String())
    }
}

func Test682_AccountExportWithoutBearerIs401Passthrough(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Authorization") != "" {
            t.Error("no inbound Authorization was set")
        }
        w.WriteHeader(http.StatusUnauthorized)
    })
    req := httptest.NewRequest(http.MethodGet, "/auth/api/identity/api/v1/auth/account/export", nil)
    rec := httptest.NewRecorder()
    p.handleIdentityProxy(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("missing bearer must pass through identity's 401, got %d", rec.Code)
    }
}

func Test682_ClientCannotSmuggleForwardedAuthHeader(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("X-Fusion-Forwarded-Authorization") != "" {
            t.Error("client-supplied hop header leaked upstream")
        }
        if r.Header.Get("Authorization") != "" {
            w.WriteHeader(http.StatusNotImplemented)
            return
        }
        _ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
    })
    // A client-supplied X-Fusion-Forwarded-Authorization on a
    // body-credential route must be cleared, never applied upstream.
    rec := post682(t, p, "/auth/api/identity/api/v1/auth/password/forgot",
        `{"email":"a@example.com"}`,
        map[string]string{"X-Fusion-Forwarded-Authorization": "Bearer forged"})
    if rec.Code != http.StatusOK {
        t.Fatalf("forgot with smuggled header: got %d %s", rec.Code, rec.Body.String())
    }
    // And on the bearer plane itself, the real Authorization (not a client
    // hop header) is what gets forwarded.
    p2 := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        if got := r.Header.Get("Authorization"); got != "Bearer real" {
            t.Errorf("upstream Authorization = %q, want the caller's real header", got)
            w.WriteHeader(http.StatusUnauthorized)
            return
        }
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/account/delete", strings.NewReader(`{}`))
    req.Header.Set("X-Fusion-Forwarded-Authorization", "Bearer forged")
    req.Header.Set("Authorization", "Bearer real")
    rec2 := httptest.NewRecorder()
    p2.handleIdentityProxy(rec2, req)
    if rec2.Code != http.StatusOK {
        t.Fatalf("real Authorization must win over a client hop header: %d", rec2.Code)
    }
}

func Test682_UnknownPathStill404(t *testing.T) {
    p := new682Portal(t, func(w http.ResponseWriter, r *http.Request) {
        t.Error("unknown path must not reach identity")
    })
    rec := post682(t, p, "/auth/api/identity/api/v1/auth/password/reset2", `{}`, nil)
    if rec.Code != http.StatusNotFound {
        t.Fatalf("unknown path: got %d, want 404", rec.Code)
    }
}
