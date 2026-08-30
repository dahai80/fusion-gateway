package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

// captureLog swaps the default slog handler for a buffered one so tests can
// assert on log lines (ReadErrorBody logs at Warn on a read fault). Returns a
// restore func that yields the captured buffer.
func captureLog() func() *strings.Builder {
    var b strings.Builder
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})))
    return func() *strings.Builder {
        slog.SetDefault(prev)
        return &b
    }
}

// errReader returns a read fault on every Read, simulating an upstream IO
// error or a closed connection mid-body. Close is a no-op.
type errReader struct{}

func (errReader) Read(p []byte) (int, error) { return 0, errors.New("simulated read fault") }
func (errReader) Close() error               { return nil }

func TestExportedConstantsMatchDefaults(t *testing.T) {
    if DefaultMaxConnsPerHost != 16 {
        t.Fatalf("DefaultMaxConnsPerHost=%d, want 16", DefaultMaxConnsPerHost)
    }
    if DefaultResponseHeaderTimeout != 30*time.Second {
        t.Fatalf("DefaultResponseHeaderTimeout=%s, want 30s", DefaultResponseHeaderTimeout)
    }
}

func TestLimitResponseReaderCapsAt10MiB(t *testing.T) {
    // infiniteReader never returns EOF — so the only thing that can stop the
    // read is the LimitReader cap. Confirms the wrapper enforces maxResponseBytes.
    var n atomic.Int64
    r := LimitResponseReader(readerFunc(func(p []byte) (int, error) {
        for i := range p {
            p[i] = 'x'
        }
        n.Add(int64(len(p)))
        return len(p), nil
    }))
    b, err := io.ReadAll(r)
    if err != nil {
        t.Fatalf("ReadAll: %v", err)
    }
    if len(b) != maxResponseBytes {
        t.Fatalf("capped read len=%d, want %d (10 MiB)", len(b), maxResponseBytes)
    }
    if got := n.Load(); got != int64(maxResponseBytes) {
        t.Fatalf("reader yielded %d bytes, want exactly %d", got, maxResponseBytes)
    }
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestReadErrorBody_ReadsCappedBody(t *testing.T) {
    body := `{"error":{"message":"upstream bad","type":"provider_error"}}`
    resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
    got := ReadErrorBody(resp)
    if string(got) != body {
        t.Fatalf("ReadErrorBody = %q, want %q", got, body)
    }
}

func TestReadErrorBody_ReadFaultLogsWarnAndReturnsNil(t *testing.T) {
    restore := captureLog()
    resp := &http.Response{Body: io.NopCloser(errReader{})}
    got := ReadErrorBody(resp)
    if got != nil {
        t.Fatalf("want nil on read fault, got %d bytes", len(got))
    }
    logs := restore().String()
    if !strings.Contains(logs, "failed to read capped upstream error body") {
        t.Fatalf("expected Warn log on read fault, got: %s", logs)
    }
    if !strings.Contains(logs, "read fault") {
        t.Fatalf("expected error context in log, got: %s", logs)
    }
}

func TestReadErrorBody_EmptyBodyReturnsEmpty(t *testing.T) {
    resp := &http.Response{Body: io.NopCloser(strings.NewReader(""))}
    got := ReadErrorBody(resp)
    if len(got) != 0 {
        t.Fatalf("want empty slice for empty body, got %d bytes", len(got))
    }
}

func TestTruncateForError_ShortPassthrough(t *testing.T) {
    in := []byte("hello upstream error")
    got := TruncateForError(in)
    if got != string(in) {
        t.Fatalf("short body should pass through, got %q want %q", got, in)
    }
}

func TestTruncateForError_ExactBoundaryPassthrough(t *testing.T) {
    in := make([]byte, 512)
    for i := range in {
        in[i] = 'a'
    }
    got := TruncateForError(in)
    if len(got) != 512 {
        t.Fatalf("boundary body should pass through untruncated, got len %d want 512", len(got))
    }
}

func TestTruncateForError_LongTruncates(t *testing.T) {
    in := make([]byte, 600)
    for i := range in {
        in[i] = 'b'
    }
    got := TruncateForError(in)
    if !strings.HasPrefix(got, strings.Repeat("b", 512)) {
        t.Fatalf("truncated body should keep leading 512 bytes, got prefix len %d", len(got))
    }
    if !strings.Contains(got, "(truncated 88 bytes)") {
        t.Fatalf("truncated body should report dropped byte count, got %q", got)
    }
}

func TestResolveResponseHeaderTimeout_ConfigOverride(t *testing.T) {
    cfg := config.BackendConfig{ResponseHeaderTimeout: 5 * time.Second}
    if got := resolveResponseHeaderTimeout(cfg); got != 5*time.Second {
        t.Fatalf("override not honored: got %s want 5s", got)
    }
}

func TestResolveResponseHeaderTimeout_DefaultApplied(t *testing.T) {
    cfg := config.BackendConfig{}
    if got := resolveResponseHeaderTimeout(cfg); got != defaultResponseHeaderTimeout {
        t.Fatalf("default not applied: got %s want %s", got, defaultResponseHeaderTimeout)
    }
}

func TestResolveMaxConnsPerHost_ConfigOverride(t *testing.T) {
    restore := captureLog()
    defer restore()
    cfg := config.BackendConfig{MaxConnsPerHost: 32, BaseURL: "http://upstream"}
    if got := resolveMaxConnsPerHost(cfg); got != 32 {
        t.Fatalf("override not honored: got %d want 32", got)
    }
    if logs := restore().String(); strings.Contains(logs, "applying default") {
        t.Fatalf("explicit cap should NOT log default-applied, got: %s", logs)
    }
}

func TestResolveMaxConnsPerHost_DefaultAppliedAndLogs(t *testing.T) {
    restore := captureLog()
    cfg := config.BackendConfig{BaseURL: "http://upstream"}
    got := resolveMaxConnsPerHost(cfg)
    logs := restore().String()
    if got != defaultMaxConnsPerHost {
        t.Fatalf("default not applied: got %d want %d", got, defaultMaxConnsPerHost)
    }
    if !strings.Contains(logs, "applying default max_conns_per_host") {
        t.Fatalf("default application should log Info, got: %s", logs)
    }
    if !strings.Contains(logs, "http://upstream") {
        t.Fatalf("log should name the backend, got: %s", logs)
    }
}

func TestResolveMaxConnsPerHost_NegativeFallsBackToDefault(t *testing.T) {
    restore := captureLog()
    defer restore()
    cfg := config.BackendConfig{MaxConnsPerHost: -1, BaseURL: "http://upstream"}
    if got := resolveMaxConnsPerHost(cfg); got != defaultMaxConnsPerHost {
        t.Fatalf("negative should fall back to default: got %d want %d", got, defaultMaxConnsPerHost)
    }
}

func TestResolveMaxIdleConnsPerHost_ConfigOverride(t *testing.T) {
    cfg := config.BackendConfig{MaxIdleConnsPerHost: 128}
    if got := resolveMaxIdleConnsPerHost(cfg); got != 128 {
        t.Fatalf("override not honored: got %d want 128", got)
    }
}

func TestResolveMaxIdleConnsPerHost_DefaultApplied(t *testing.T) {
    cfg := config.BackendConfig{}
    if got := resolveMaxIdleConnsPerHost(cfg); got != defaultMaxIdleConnsPerHost {
        t.Fatalf("default not applied: got %d want %d", got, defaultMaxIdleConnsPerHost)
    }
}

func TestTransportForBackend_TCPDefaultsApplied(t *testing.T) {
    restore := captureLog()
    defer restore()
    rt := TransportForBackend(config.BackendConfig{BaseURL: "http://upstream.local"})
    tr, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("TCP path must return *http.Transport, got %T", rt)
    }
    if tr.MaxConnsPerHost != 16 {
        t.Errorf("MaxConnsPerHost=%d, want default 16", tr.MaxConnsPerHost)
    }
    if tr.MaxIdleConnsPerHost != 64 {
        t.Errorf("MaxIdleConnsPerHost=%d, want default 64", tr.MaxIdleConnsPerHost)
    }
    if tr.MaxIdleConns != 100 {
        t.Errorf("MaxIdleConns=%d, want 100", tr.MaxIdleConns)
    }
    if tr.IdleConnTimeout != 90*time.Second {
        t.Errorf("IdleConnTimeout=%s, want 90s", tr.IdleConnTimeout)
    }
    if tr.ResponseHeaderTimeout != 30*time.Second {
        t.Errorf("ResponseHeaderTimeout=%s, want 30s", tr.ResponseHeaderTimeout)
    }
    if tr.TLSHandshakeTimeout != 10*time.Second {
        t.Errorf("TLSHandshakeTimeout=%s, want 10s", tr.TLSHandshakeTimeout)
    }
    if tr.DialContext == nil {
        t.Error("DialContext must be set (R5 dial timeout), got nil")
    }
    logs := restore().String()
    if !strings.Contains(logs, "TCP backend timeouts applied") {
        t.Fatalf("TCP path should log applied timeouts, got: %s", logs)
    }
}

func TestTransportForBackend_TCPConfigOverridesHonored(t *testing.T) {
    restore := captureLog()
    defer restore()
    rt := TransportForBackend(config.BackendConfig{
        BaseURL:               "http://upstream.local",
        MaxConnsPerHost:       48,
        MaxIdleConnsPerHost:   200,
        ResponseHeaderTimeout: 15 * time.Second,
    })
    tr, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("TCP path must return *http.Transport, got %T", rt)
    }
    if tr.MaxConnsPerHost != 48 {
        t.Errorf("MaxConnsPerHost=%d, want configured 48", tr.MaxConnsPerHost)
    }
    if tr.MaxIdleConnsPerHost != 200 {
        t.Errorf("MaxIdleConnsPerHost=%d, want configured 200", tr.MaxIdleConnsPerHost)
    }
    if tr.ResponseHeaderTimeout != 15*time.Second {
        t.Errorf("ResponseHeaderTimeout=%s, want configured 15s", tr.ResponseHeaderTimeout)
    }
    if logs := restore().String(); strings.Contains(logs, "applying default max_conns_per_host") {
        t.Fatalf("explicit cap should NOT log default-applied, got: %s", logs)
    }
}

func TestTransportForBackend_UDSPathReturnsTransport(t *testing.T) {
    restore := captureLog()
    defer restore()
    rt := TransportForBackend(config.BackendConfig{
        BaseURL:    "http://unix",
        SocketPath: "/tmp/fake-uds.sock",
    })
    tr, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("UDS path must return *http.Transport, got %T", rt)
    }
    if tr.MaxConnsPerHost != 16 {
        t.Errorf("UDS MaxConnsPerHost=%d, want default 16", tr.MaxConnsPerHost)
    }
    if tr.MaxIdleConnsPerHost != 64 {
        t.Errorf("UDS MaxIdleConnsPerHost=%d, want default 64", tr.MaxIdleConnsPerHost)
    }
    if tr.ResponseHeaderTimeout != 30*time.Second {
        t.Errorf("UDS ResponseHeaderTimeout=%s, want 30s", tr.ResponseHeaderTimeout)
    }
    if tr.DialContext == nil {
        t.Fatal("UDS DialContext must be set, got nil")
    }
    logs := restore().String()
    if !strings.Contains(logs, "building UDS transport for backend") {
        t.Fatalf("UDS path should log build, got: %s", logs)
    }
    if !strings.Contains(logs, "/tmp/fake-uds.sock") {
        t.Fatalf("UDS log should name the socket path, got: %s", logs)
    }
}

func TestTransportForBackend_UDSDialsAndServesRequest(t *testing.T) {
    // End-to-end: spin an http.Server on a Unix Domain Socket and confirm the
    // UDS transport built by TransportForBackend actually completes a request
    // over it. A SHORT /tmp path is mandatory: macOS sun_len caps a Unix socket
    // path at ~104 bytes, and t.TempDir()'s nested /var/folders/... path
    // overflows that limit (listen unix: file name too long).
    sock := "/tmp/httpx-uds-live.sock"
    defer os.Remove(sock)
    ln, err := net.Listen("unix", sock)
    if err != nil {
        t.Fatalf("listen unix: %v", err)
    }
    defer ln.Close()

    mux := http.NewServeMux()
    mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("uds-pong"))
    })
    srv := &http.Server{Handler: mux}
    go func() { _ = srv.Serve(ln) }()
    defer srv.Shutdown(context.Background())

    client := &http.Client{Transport: TransportForBackend(config.BackendConfig{
        BaseURL:    "http://unix",
        SocketPath: sock,
    })}
    resp, err := client.Get("http://unix/ping")
    if err != nil {
        t.Fatalf("UDS Get: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("UDS status=%d, want 200", resp.StatusCode)
    }
    b, err := io.ReadAll(resp.Body)
    if err != nil {
        t.Fatalf("UDS read body: %v", err)
    }
    if string(b) != "uds-pong" {
        t.Fatalf("UDS body=%q, want uds-pong", b)
    }
}

func TestTransportForBackend_UDSDialTimeoutOnDeadSocket(t *testing.T) {
    // A socket path that exists as a file but has no listener -> DialContext
    // must fail fast (defaultDialTimeout 10s), not hang. A short context keeps
    // the test fast even if the dial were to wait.
    sock := "/tmp/httpx-uds-dead.sock"
    defer os.Remove(sock)
    // Create a non-socket file so the unix dial fails with a connection error
    // immediately rather than ENOENT (still a fast non-nil error — the point
    // is the transport surfaces the dial fault, not the exact errno).
    if f, err := os.Create(sock); err != nil {
        t.Fatalf("seed file: %v", err)
    } else {
        _ = f.Close()
    }
    client := &http.Client{Transport: TransportForBackend(config.BackendConfig{
        BaseURL:    "http://unix",
        SocketPath: sock,
    })}
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/ping", nil)
    _, err := client.Do(req)
    if err == nil {
        t.Fatal("dial to dead socket should fail, got nil error")
    }
}
