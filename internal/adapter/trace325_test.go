package adapter

// #325 test: X-Fusion-Trace must be in the fusion passthrough set so the
// business trace id survives the gateway → upstream hop. InitRequestLog
// seeds a gateway uuid onto the inbound header when the client omits it, so
// WithFusionHeaders always captures a value on the /v1/* path.

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func Test325_TraceInPassthroughSet(t *testing.T) {
    found := false
    for _, h := range fusionPassthroughHeaders {
        if h == "X-Fusion-Trace" {
            found = true
            break
        }
    }
    if !found {
        t.Fatal("#325: X-Fusion-Trace missing from fusionPassthroughHeaders")
    }
}

func Test325_TracePropagatedEndToEnd(t *testing.T) {
    inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    inbound.Header.Set("X-Fusion-Trace", "01J8TRACEBUSINESS01")

    ctx := WithFusionHeaders(inbound.Context(), inbound)

    upstream, _ := http.NewRequest(http.MethodPost, "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(ctx, upstream)

    if got := upstream.Header.Get("X-Fusion-Trace"); got != "01J8TRACEBUSINESS01" {
        t.Errorf("#325: outbound X-Fusion-Trace = %q, want 01J8TRACEBUSINESS01", got)
    }
}

func Test325_TraceAbsentNotInjected(t *testing.T) {
    inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    ctx := WithFusionHeaders(inbound.Context(), inbound)

    upstream, _ := http.NewRequest(http.MethodPost, "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(ctx, upstream)

    if got := upstream.Header.Get("X-Fusion-Trace"); got != "" {
        t.Errorf("#325: empty trace must not be injected, got %q", got)
    }
}
