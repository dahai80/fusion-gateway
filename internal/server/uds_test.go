package server

import (
    "context"
    "io"
    "net"
    "net/http"
    "os"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// udsPath returns a short shallow unix-socket path under the OS temp dir root.
// macOS caps unix socket path length (~104 bytes SUN_LEN); t.TempDir() nests
// under a long test-name subdir and exceeds the cap.
func udsPath(t *testing.T) string {
    t.Helper()
    f, err := os.CreateTemp("", "fg-srv-uds-*.sock")
    if err != nil {
        t.Fatalf("create temp socket file: %v", err)
    }
    name := f.Name()
    f.Close()
    os.Remove(name)
    t.Cleanup(func() { os.Remove(name) })
    return name
}

// TestListenUnix_StaleSocketRemoved asserts a leftover file at the socket path
// from a previous unclean shutdown is removed before the new bind succeeds.
func TestListenUnix_StaleSocketRemoved(t *testing.T) {
    s := newTestServer()
    sock := udsPath(t)

    // Plant a stale file at the socket path (simulates a leftover inode from a
    // crash; net.Listen would refuse to bind over an existing file).
    if err := os.WriteFile(sock, []byte("stale"), 0666); err != nil {
        t.Fatalf("plant stale file: %v", err)
    }
    if _, err := os.Stat(sock); err != nil {
        t.Fatalf("stale file not present after plant: %v", err)
    }

    uds := &config.UnixSocketConfig{Enabled: true, Path: sock, Mode: 0660}
    ln, err := s.listenUnix(uds)
    if err != nil {
        t.Fatalf("listenUnix should remove stale file and rebind: %v", err)
    }
    defer ln.Close()
}

// TestListenUnix_Permissions asserts the configured mode (default 0660) is
// applied to the socket file.
func TestListenUnix_Permissions(t *testing.T) {
    s := newTestServer()
    sock := udsPath(t)

    uds := &config.UnixSocketConfig{Enabled: true, Path: sock, Mode: 0660}
    ln, err := s.listenUnix(uds)
    if err != nil {
        t.Fatalf("listenUnix: %v", err)
    }
    defer ln.Close()

    info, err := os.Stat(sock)
    if err != nil {
        t.Fatalf("stat socket: %v", err)
    }
    if info.Mode().Perm() != 0660 {
        t.Errorf("socket mode: want 0660, got %04o", info.Mode().Perm())
    }
}

// TestListenUnix_DefaultMode asserts Mode==0 falls back to 0660.
func TestListenUnix_DefaultMode(t *testing.T) {
    s := newTestServer()
    sock := udsPath(t)

    uds := &config.UnixSocketConfig{Enabled: true, Path: sock, Mode: 0}
    ln, err := s.listenUnix(uds)
    if err != nil {
        t.Fatalf("listenUnix: %v", err)
    }
    defer ln.Close()

    info, err := os.Stat(sock)
    if err != nil {
        t.Fatalf("stat socket: %v", err)
    }
    if info.Mode().Perm() != 0660 {
        t.Errorf("default socket mode: want 0660, got %04o", info.Mode().Perm())
    }
}

// TestShutdown_UnlinksSocket asserts Shutdown removes the socket file so the
// next start isn't blocked by a stale inode.
func TestShutdown_UnlinksSocket(t *testing.T) {
    s := newTestServer()
    sock := udsPath(t)

    uds := &config.UnixSocketConfig{Enabled: true, Path: sock, Mode: 0660}
    ln, err := s.listenUnix(uds)
    if err != nil {
        t.Fatalf("listenUnix: %v", err)
    }
    s.unixListener = ln
    s.cfg.Config.Server.UnixSocket = uds
    // A real httpServer is needed for Shutdown (it calls httpServer.Shutdown).
    s.httpServer = &http.Server{Handler: http.NewServeMux()}

    if _, err := os.Stat(sock); err != nil {
        t.Fatalf("socket should exist before shutdown: %v", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := s.Shutdown(ctx); err != nil {
        t.Fatalf("shutdown: %v", err)
    }

    if _, err := os.Stat(sock); !os.IsNotExist(err) {
        t.Errorf("socket should be unlinked after shutdown, stat err: %v", err)
    }
}

// TestServe_RoundTripOverUDS asserts the inbound UDS listener serves real HTTP:
// build a tiny mux + httpServer, serve on a unix listener, GET over the socket.
func TestServe_RoundTripOverUDS(t *testing.T) {
    s := newTestServer()
    sock := udsPath(t)

    uds := &config.UnixSocketConfig{Enabled: true, Path: sock, Mode: 0660}
    ln, err := s.listenUnix(uds)
    if err != nil {
        t.Fatalf("listenUnix: %v", err)
    }
    defer ln.Close()

    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        io.WriteString(w, "ok")
    })
    s.httpServer = &http.Server{Handler: mux}

    serveErr := make(chan error, 1)
    go func() {
        serveErr <- s.serve(ln)
    }()

    // Dial the unix socket and GET /healthz.
    client := &http.Client{
        Timeout: 2 * time.Second,
        Transport: &http.Transport{
            DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
                d := net.Dialer{}
                return d.DialContext(ctx, "unix", sock)
            },
        },
    }
    resp, err := client.Get("http://unix/healthz")
    if err != nil {
        t.Fatalf("GET over UDS: %v", err)
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    if resp.StatusCode != http.StatusOK {
        t.Errorf("status: want 200, got %d", resp.StatusCode)
    }
    if string(body) != "ok" {
        t.Errorf("body: want ok, got %s", body)
    }

    s.httpServer.Close()
    <-serveErr // serve returns after Close
}
