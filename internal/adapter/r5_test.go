package adapter

// R5 guard test: TransportForBackend must set ResponseHeaderTimeout so a stuck
// upstream (connected but never writes headers) fails FAST at the transport
// layer, not at the full Client.Timeout. Revert ResponseHeaderTimeout to 0 on
// the TCP path → the request hangs until Client.Timeout and the elapsed-time
// assertion fails.

import (
    "io"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestR5_TCP_ResponseHeaderTimeoutFailsFast starts an upstream whose handler
// sleeps well beyond the configured ResponseHeaderTimeout before writing any
// headers. With R5 in place the transport aborts at ~ResponseHeaderTimeout and
// the client sees a timeout error; WITHOUT R5 the request would hang until the
// much larger Client.Timeout. We assert elapsed < Client.Timeout/2 to prove the
// transport-layer cap fired, not the client-layer one.
func TestR5_TCP_ResponseHeaderTimeoutFailsFast(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Sleep far beyond the configured header timeout AND beyond the
        // client timeout we will assert against. If ResponseHeaderTimeout is
        // honored, the request never waits this long.
        time.Sleep(3 * time.Second)
        io.WriteString(w, "too-late")
    }))
    defer srv.Close()

    // 1s header cap — the stuck upstream must fail at ~1s, not 10s.
    const headerTO = 1 * time.Second
    rt := TransportForBackend(config.BackendConfig{
        BaseURL:              srv.URL,
        ResponseHeaderTimeout: headerTO,
    })
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport, got %T", rt)
    }
    if tpt.ResponseHeaderTimeout != headerTO {
        t.Fatalf("R5: ResponseHeaderTimeout not applied, want %s, got %s", headerTO, tpt.ResponseHeaderTimeout)
    }

    // Client.Timeout deliberately large (10s) so that the ONLY thing that can
    // cut the request short is the transport's ResponseHeaderTimeout. If R5 is
    // reverted (timeout=0), the request hangs the full 10s and the assertion
    // elapsed < Client.Timeout/2 fails.
    client := &http.Client{Transport: rt, Timeout: 10 * time.Second}

    start := time.Now()
    resp, err := client.Get(srv.URL + "/v1/models")
    elapsed := time.Since(start)

    if err == nil {
        if resp != nil {
            resp.Body.Close()
        }
        t.Fatalf("R5: request to stuck upstream succeeded after %s — ResponseHeaderTimeout not enforced", elapsed)
    }
    // Must fail well before Client.Timeout (10s). The header cap is 1s; allow
    // generous slack (4s) for scheduler/jitter but still prove it was NOT the
    // 10s client timeout that fired.
    if elapsed >= 4*time.Second {
        t.Fatalf("R5: request took %s — hung until near Client.Timeout instead of failing at ResponseHeaderTimeout=%s", elapsed, headerTO)
    }
}

// TestR5_TCP_DefaultResponseHeaderTimeout verifies the factory applies a
// non-zero default when config omits response_header_timeout (the common case).
// A default of 0 would mean NO cap = the original R5 bug.
func TestR5_TCP_DefaultResponseHeaderTimeout(t *testing.T) {
    rt := TransportForBackend(config.BackendConfig{BaseURL: "http://127.0.0.1:11434"})
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport, got %T", rt)
    }
    if tpt.ResponseHeaderTimeout <= 0 {
        t.Fatalf("R5: default ResponseHeaderTimeout must be > 0, got %s — stuck upstream would hang full Client.Timeout", tpt.ResponseHeaderTimeout)
    }
    // N2: defaultResponseHeaderTimeout moved to httpx as the exported
    // DefaultResponseHeaderTimeout; adapter/transport.go re-exports it.
    if tpt.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
        t.Errorf("R5: default ResponseHeaderTimeout: want %s, got %s", DefaultResponseHeaderTimeout, tpt.ResponseHeaderTimeout)
    }
    // R5 also tightened dial + TLS handshake; assert they are non-zero too.
    if tpt.DialContext == nil {
        t.Errorf("R5: DialContext must be set (dial timeout cap)")
    }
    if tpt.TLSHandshakeTimeout <= 0 {
        t.Errorf("R5: TLSHandshakeTimeout must be > 0, got %s", tpt.TLSHandshakeTimeout)
    }
}

// TestR5_UDS_ResponseHeaderTimeoutApplied mirrors the above for the UDS path,
// which previously had NO dial timeout and NO ResponseHeaderTimeout at all — a
// dead socket could hang indefinitely.
func TestR5_UDS_ResponseHeaderTimeoutApplied(t *testing.T) {
    const headerTO = 2 * time.Second
    rt := TransportForBackend(config.BackendConfig{
        BaseURL:              "http://unix/",
        SocketPath:           "/tmp/r5-uds-probe.sock",
        ResponseHeaderTimeout: headerTO,
    })
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport for UDS, got %T", rt)
    }
    if tpt.ResponseHeaderTimeout != headerTO {
        t.Fatalf("R5: UDS ResponseHeaderTimeout not applied, want %s, got %s", headerTO, tpt.ResponseHeaderTimeout)
    }
    if tpt.TLSHandshakeTimeout <= 0 {
        t.Errorf("R5: UDS TLSHandshakeTimeout must be > 0, got %s", tpt.TLSHandshakeTimeout)
    }
    if tpt.DialContext == nil {
        t.Fatalf("R5: UDS DialContext must be set (dial timeout cap)")
    }
}
