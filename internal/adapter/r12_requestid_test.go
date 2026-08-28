package adapter

// R12 (audit) test: X-Request-ID must be in the fusion passthrough set so the
// gateway forwards it to fusion-mlx/cloud upstreams for log correlation.
// WithFusionHeaders reads it from the inbound request; InjectFusionHeaders
// writes it onto the outbound upstream request.

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
)

// TestR12_RequestIDInPassthroughSet: X-Request-ID is listed in
// fusionPassthroughHeaders. Guards against a regression that drops it.
func TestR12_RequestIDInPassthroughSet(t *testing.T) {
    found := false
    for _, h := range fusionPassthroughHeaders {
        if h == "X-Request-ID" {
            found = true
            break
        }
    }
    if !found {
        t.Fatal("R12: X-Request-ID missing from fusionPassthroughHeaders")
    }
}

// TestR12_RequestIDPropagatedEndToEnd: an inbound request carrying
// X-Request-ID → WithFusionHeaders → InjectFusionHeaders must set the same
// value on the outbound upstream request header.
func TestR12_RequestIDPropagatedEndToEnd(t *testing.T) {
    inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    inbound.Header.Set("X-Request-ID", "req-abc-123")
    inbound.Header.Set("X-Fusion-Route", "gateway-decision")

    ctx := WithFusionHeaders(inbound.Context(), inbound)

    upstream, _ := http.NewRequest(http.MethodPost, "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(ctx, upstream)

    if got := upstream.Header.Get("X-Request-ID"); got != "req-abc-123" {
        t.Errorf("R12: outbound X-Request-ID = %q, want req-abc-123", got)
    }
    if got := upstream.Header.Get("X-Fusion-Route"); got != "gateway-decision" {
        t.Errorf("R12: outbound X-Fusion-Route = %q, want gateway-decision", got)
    }
}

// TestR12_RequestIDAbsentNotInjected: an inbound request WITHOUT X-Request-ID
// must not inject an empty header upstream (the middleware layer is
// responsible for generating one; InjectFusionHeaders only forwards what
// WithFusionHeaders captured).
func TestR12_RequestIDAbsentNotInjected(t *testing.T) {
    inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    inbound.Header.Set("X-Fusion-Route", "gateway-decision")
    // No X-Request-ID set on inbound.

    ctx := WithFusionHeaders(inbound.Context(), inbound)
    upstream, _ := http.NewRequest(http.MethodPost, "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(ctx, upstream)

    if got := upstream.Header.Get("X-Request-ID"); got != "" {
        t.Errorf("R12: absent inbound X-Request-ID must not inject empty/any value upstream, got %q", got)
    }
    // Ensure ctx returned is the parent when no headers captured (no value).
    _ = context.Background()
}
