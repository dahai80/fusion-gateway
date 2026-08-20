package adapter

import (
    "context"
    "io"
    "net"
    "net/http"
    "net/http/httptest"
    "os"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// udsSocketPath returns a short unix-socket path under the OS temp dir root.
// macOS caps unix socket path length (~104 bytes SUN_LEN); t.TempDir() nests
// under a long test-name subdir and exceeds the cap, so we keep it shallow.
func udsSocketPath(t *testing.T) string {
    t.Helper()
    f, err := os.CreateTemp("", "fg-uds-*.sock")
    if err != nil {
        t.Fatalf("create temp socket file: %v", err)
    }
    name := f.Name()
    f.Close()
    os.Remove(name)
    t.Cleanup(func() { os.Remove(name) })
    return name
}

// TestTransportForBackend_TCP returns a cloned transport tuned for the local
// hot path: MaxIdleConnsPerHost must be 64, not the Go default of 2.
func TestTransportForBackend_TCP(t *testing.T) {
    rt := TransportForBackend(config.BackendConfig{BaseURL: "http://127.0.0.1:11434"})
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport for TCP backend, got %T", rt)
    }
    if tpt.MaxIdleConnsPerHost != 64 {
        t.Errorf("MaxIdleConnsPerHost: want 64, got %d", tpt.MaxIdleConnsPerHost)
    }
    if tpt.MaxIdleConns != 100 {
        t.Errorf("MaxIdleConns: want 100, got %d", tpt.MaxIdleConns)
    }
}

// TestTransportForBackend_UDS returns a transport whose DialContext dials the
// configured unix socket. We assert by actually dialing a temp listener.
func TestTransportForBackend_UDS(t *testing.T) {
    sock := udsSocketPath(t)

    ln, err := net.Listen("unix", sock)
    if err != nil {
        t.Fatalf("listen unix: %v", err)
    }
    defer ln.Close()

    accepted := make(chan struct{})
    go func() {
        c, err := ln.Accept()
        if err != nil {
            close(accepted)
            return
        }
        c.Close()
        close(accepted)
    }()

    rt := TransportForBackend(config.BackendConfig{
        BaseURL:    "http://unix/",
        SocketPath: sock,
    })
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport for UDS backend, got %T", rt)
    }
    if tpt.DialContext == nil {
        t.Fatal("DialContext must be set for UDS backend")
    }
    if tpt.MaxIdleConnsPerHost != 64 {
        t.Errorf("MaxIdleConnsPerHost: want 64, got %d", tpt.MaxIdleConnsPerHost)
    }

    conn, err := tpt.DialContext(context.Background(), "tcp", "ignored:1234")
    if err != nil {
        t.Fatalf("DialContext failed: %v", err)
    }
    defer conn.Close()

    select {
    case <-accepted:
        // server-side accepted the UDS connection
    case <-time.After(2 * time.Second):
        t.Fatal("timed out waiting for server to accept UDS connection")
    }
}

// TestTransportForBackend_RoundTrip does a full HTTP round-trip over UDS:
// hand-start an http.Server on a unix listener, then use a client built with
// TransportForBackend to GET from it via the dummy http://unix/ host.
func TestTransportForBackend_RoundTrip(t *testing.T) {
    sock := udsSocketPath(t)

    ln, err := net.Listen("unix", sock)
    if err != nil {
        t.Fatalf("listen unix: %v", err)
    }
    defer ln.Close()

    srv := &http.Server{
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path != "/v1/models" {
                t.Errorf("unexpected path: %s", r.URL.Path)
            }
            w.Header().Set("Content-Type", "application/json")
            io.WriteString(w, `{"data":[]}`)
        }),
    }
    go srv.Serve(ln)
    defer srv.Close()

    rt := TransportForBackend(config.BackendConfig{
        BaseURL:    "http://unix/",
        SocketPath: sock,
    })
    client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

    resp, err := client.Get("http://unix/v1/models")
    if err != nil {
        t.Fatalf("GET over UDS failed: %v", err)
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    if resp.StatusCode != http.StatusOK {
        t.Errorf("status: want 200, got %d", resp.StatusCode)
    }
    if string(body) != `{"data":[]}` {
        t.Errorf("body: want {\"data\":[]}, got %s", body)
    }
}

// TestTransportForBackend_TCPRoundTrip sanity-checks the TCP path still serves
// a real httptest server end-to-end (guards against the clone breaking defaults).
func TestTransportForBackend_TCPRoundTrip(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        io.WriteString(w, "ok")
    }))
    defer srv.Close()

    rt := TransportForBackend(config.BackendConfig{BaseURL: srv.URL})
    client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

    resp, err := client.Get(srv.URL + "/healthz")
    if err != nil {
        t.Fatalf("GET over TCP failed: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Errorf("status: want 200, got %d", resp.StatusCode)
    }
}
