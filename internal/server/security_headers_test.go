package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

// #674 AC: /auth/* and /shell/* responses carry CSP + Referrer-Policy plus
// the containment baseline, stamped before the handler runs (proxied module
// responses keep them — ReverseProxy adds upstream headers on top).
func Test674_SecurityHeadersStamped(t *testing.T) {
    inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })
    h := withSecurityHeaders(portalCSP, "no-referrer", "DENY", inner)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/", nil))
    hdr := rec.Header()
    checks := map[string]string{
        "Content-Security-Policy": portalCSP,
        "Referrer-Policy":         "no-referrer",
        "X-Content-Type-Options":  "nosniff",
        "X-Frame-Options":         "DENY",
        "Permissions-Policy":      "interest-cohort=()",
    }
    for k, want := range checks {
        if got := hdr.Get(k); got != want {
            t.Fatalf("%s: got %q, want %q", k, got, want)
        }
    }
}

// #674: portal CSP must keep admitting what the page actually uses — SVG
// captcha as data: URI (#603), inline style attributes, same-origin API
// calls and framing.
func Test674_PortalCSPFitsPortal(t *testing.T) {
    for _, need := range []string{
        "img-src 'self' data:",
        "style-src 'self' 'unsafe-inline'",
        "script-src 'self'",
        "frame-ancestors 'self'",
    } {
        if !strings.Contains(portalCSP, need) {
            t.Fatalf("portalCSP missing %q: %s", need, portalCSP)
        }
    }
}
