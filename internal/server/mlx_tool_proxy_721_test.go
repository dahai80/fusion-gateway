package server

// #721: the fusion-mlx model-management surface (/v1/merge-adapter,
// /v1/convert/jobs*) must be proxied through the gateway like the rest of
// the convert tool plane — trainer `ft merge` drives POST /v1/merge-adapter
// and a direct :11434 fallback is blocked by design.

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func Test721_ModelManageRoutesRegistered(t *testing.T) {
    s := &Server{}
    mux := http.NewServeMux()
    s.registerMLXToolRoutes(mux)
    // Registration probe: with a nil pool the handler 503s AFTER routing —
    // the assertion is that ServeMux found a route at all (no 404 from the
    // mux itself), which is the gap #721 fixes.
    for _, tc := range []struct{ method, path string }{
        {http.MethodPost, "/v1/merge-adapter"},
        {http.MethodGet, "/v1/convert/jobs"},
        {http.MethodDelete, "/v1/convert/jobs/j-1"},
        {http.MethodPost, "/v1/convert/jobs/j-1/cancel"},
    } {
        req := httptest.NewRequest(tc.method, tc.path, nil)
        // ServeMux.Handler resolves the route without executing the handler
        // (withMiddleware on a zero Server would nil-panic); a "" pattern or
        // the catch-all means the route is missing — the gap #721 fixes.
        _, pattern := mux.Handler(req)
        if pattern == "" || pattern == "/" {
            t.Fatalf("%s %s: no mux route registered (pattern=%q)", tc.method, tc.path, pattern)
        }
    }
}

func Test721_ExistingConvertRoutesStillRegistered(t *testing.T) {
    s := &Server{}
    mux := http.NewServeMux()
    s.registerMLXToolRoutes(mux)
    for _, tc := range []struct{ method, path string }{
        {http.MethodPost, "/v1/convert"},
        {http.MethodPost, "/v1/quantize"},
        {http.MethodGet, "/v1/hardware"},
        {http.MethodPost, "/v1/admin/models/m-1/load"},
    } {
        req := httptest.NewRequest(tc.method, tc.path, nil)
        _, pattern := mux.Handler(req)
        if pattern == "" || pattern == "/" {
            t.Fatalf("%s %s: regression — existing route lost", tc.method, tc.path)
        }
    }
}
