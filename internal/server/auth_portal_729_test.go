package server

// #729: module entry deep links (e.g. /office) were 403 on login because the
// redirect allowlist was exact-match only. An allowlist entry ending in `/*`
// now covers every same-origin module path under that prefix.

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func portal729(redirects []string) *authPortal {
    return &authPortal{cfg: config.AuthPortalConfig{RedirectURIs: redirects}}
}

func Test729_AllowlistedRedirectGlobPrefix(t *testing.T) {
    p := portal729([]string{
        "http://127.0.0.1:11432/",
        "http://127.0.0.1:11432/*",
        "https://fusion-mlx.cn/login",
    })
    cases := []struct {
        name string
        raw  string
        want bool
    }{
        {"office deep link via glob", "http://127.0.0.1:11432/office", true},
        {"cowork deep link via glob", "http://127.0.0.1:11432/cowork/space-1", true},
        {"root still exact", "http://127.0.0.1:11432/", true},
        {"glob entry covers bare origin path", "http://127.0.0.1:11432/login", true},
        {"other origin never matches glob", "http://evil.example.com/office", false},
        {"other port never matches glob", "http://127.0.0.1:9999/office", false},
        {"other scheme never matches glob", "https://127.0.0.1:11432/office", false},
        {"exact entry outside glob path", "https://fusion-mlx.cn/login", true},
        {"unlisted path on exact-only entry", "https://fusion-mlx.cn/office", false},
        {"path outside prefix stays denied", "http://127.0.0.1:11432.evil.com/office", false},
    }
    for _, tc := range cases {
        if _, ok := p.allowlistedRedirect(tc.raw); ok != tc.want {
            t.Fatalf("%s: got %v want %v", tc.name, ok, tc.want)
        }
    }
}

func Test729_AllowlistedRedirectGlobSubPath(t *testing.T) {
    // Narrow glob: only a subtree, siblings stay denied.
    p := portal729([]string{"http://localhost:3000/shell/*"})
    if _, ok := p.allowlistedRedirect("http://localhost:3000/shell/fusion-site/login"); !ok {
        t.Fatal("subtree path must match")
    }
    if _, ok := p.allowlistedRedirect("http://localhost:3000/office"); ok {
        t.Fatal("sibling path outside glob prefix must stay denied")
    }
}
