package server

import "net/http"

// #674: gateway-served surfaces (/auth/ portal, /shell/ proxy) ship without
// browser-side containment headers — the prod site root sets CSP and
// Referrer-Policy at nginx, but the gateway's own handlers answered bare,
// so the SSO front door had no browser-side containment and leaked full
// URLs (including ?redirect= targets) via Referer. Headers are stamped
// before the handler runs so proxied module responses (ReverseProxy adds
// upstream headers on top, never removes pre-set ones) keep them too.

// portalCSP fits the auth portal page: external script/style assets only,
// the SVG captcha arrives as a data: URI (#603), API calls are same-origin,
// and framing is same-origin only. style-src keeps 'unsafe-inline' because
// the page sets a few style attributes from app.js.
const portalCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'"

// shellCSP is deliberately permissive on sources: /shell/* fronts every
// registered module SPA (varied bundles — inline bootstrap scripts,
// blob:/data: workers), and a strict policy here would break modules the
// gateway does not own. The containment that matters is frame-ancestors
// (same-origin only: the shell host embeds modules in same-origin
// iframes) plus the Referrer-Policy baseline.
const shellCSP = "default-src 'self' 'unsafe-inline' 'unsafe-eval' data: blob:; frame-ancestors 'self'"

// withSecurityHeaders stamps the containment header set onto every
// response before delegating. csp and referrer vary per surface; the rest
// of the baseline is fixed. Callers pass the X-Frame-Options value that
// matches their framing model (DENY for the portal, SAMEORIGIN for /shell/
// where the host embeds modules in same-origin iframes).
func withSecurityHeaders(csp, referrer, frameOptions string, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        h := w.Header()
        h.Set("Content-Security-Policy", csp)
        h.Set("Referrer-Policy", referrer)
        h.Set("X-Content-Type-Options", "nosniff")
        h.Set("X-Frame-Options", frameOptions)
        h.Set("Permissions-Policy", "interest-cohort=()")
        next.ServeHTTP(w, r)
    })
}
