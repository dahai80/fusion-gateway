package httpx

// transport_h2_test.go — #386: a wedged pooled HTTP/2 connection multiplexed
// every subsequent request onto a dead conn until the 30s header timeout
// fired, for 17+ minutes, while fresh connections to the same upstream
// succeeded. The transport must (1) health-check h2 connections via
// ReadIdleTimeout+PingTimeout so wedged conns are closed and replaced, and
// (2) support a per-backend force_http1 escape hatch that pins a flaky
// upstream to HTTP/1.1, where a dead conn fails fast instead of silently
// multiplexing.

import (
    "crypto/tls"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestTransportForBackend_HTTP2HealthCheckConfigured: the TCP transport's
// internal http2.Transport must carry ReadIdleTimeout/PingTimeout — without
// them a half-dead conn stays pooled forever (restart was the only recovery).
func TestTransportForBackend_HTTP2HealthCheckConfigured(t *testing.T) {
    // transportForBackend hands out the internal h2 transport the factory
    // wired in: x/net ConfigureTransports refuses a second call on the same
    // http.Transport, so a read-back through the RoundTripper is impossible.
    _, h2t := transportForBackend(config.BackendConfig{BaseURL: "https://cloud.example", Enabled: true})
    if h2t == nil {
        t.Fatalf("h2 transport = nil, want configured ReadIdleTimeout/PingTimeout")
    }
    if h2t.ReadIdleTimeout != DefaultHTTP2ReadIdleTimeout {
        t.Errorf("ReadIdleTimeout = %v, want %v", h2t.ReadIdleTimeout, DefaultHTTP2ReadIdleTimeout)
    }
    if h2t.PingTimeout != DefaultHTTP2PingTimeout {
        t.Errorf("PingTimeout = %v, want %v", h2t.PingTimeout, DefaultHTTP2PingTimeout)
    }
}

// newH2TLSServer starts a TLS test server with HTTP/2 enabled.
func newH2TLSServer(t *testing.T) *httptest.Server {
    t.Helper()
    srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    srv.EnableHTTP2 = true
    srv.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
    srv.StartTLS()
    t.Cleanup(srv.Close)
    return srv
}

// trustServerCert relaxes TLS verification on a factory-built transport so
// tests can talk to the httptest self-signed cert. The h2 settings installed
// by the factory are untouched — only the verification mode changes.
func trustServerCert(t *testing.T, rt http.RoundTripper) {
    t.Helper()
    ht, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("transport = %T, want *http.Transport", rt)
    }
    if ht.TLSClientConfig == nil {
        ht.TLSClientConfig = &tls.Config{}
    }
    ht.TLSClientConfig.InsecureSkipVerify = true
}

// TestTransportForBackend_HTTP2UsedByDefault: with h2 available upstream, a
// default backend transport negotiates HTTP/2.
func TestTransportForBackend_HTTP2UsedByDefault(t *testing.T) {
    srv := newH2TLSServer(t)
    rt := TransportForBackend(config.BackendConfig{BaseURL: srv.URL, Enabled: true})
    trustServerCert(t, rt)
    client := &http.Client{Transport: rt}
    resp, err := client.Get(srv.URL)
    if err != nil {
        t.Fatalf("request: %v", err)
    }
    resp.Body.Close()
    if resp.ProtoMajor != 2 {
        t.Fatalf("proto = %s, want HTTP/2", resp.Proto)
    }
}

// TestTransportForBackend_ForceHTTP1DisablesH2: force_http1=true pins the
// backend to HTTP/1.1 even when the upstream offers h2 — the operator escape
// hatch for an upstream whose h2 conns wedge behind an LB.
func TestTransportForBackend_ForceHTTP1DisablesH2(t *testing.T) {
    srv := newH2TLSServer(t)
    rt, h2t := transportForBackend(config.BackendConfig{BaseURL: srv.URL, Enabled: true, ForceHTTP1: true})
    if h2t != nil {
        t.Fatalf("force_http1 h2 transport = non-nil, want nil")
    }
    trustServerCert(t, rt)
    client := &http.Client{Transport: rt}
    resp, err := client.Get(srv.URL)
    if err != nil {
        t.Fatalf("request: %v", err)
    }
    resp.Body.Close()
    if resp.ProtoMajor != 1 {
        t.Fatalf("proto = %s, want HTTP/1.1 (force_http1)", resp.Proto)
    }
}
