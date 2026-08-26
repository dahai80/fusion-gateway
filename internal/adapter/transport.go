package adapter

import (
    "context"
    "fmt"
    "io"
    "log/slog"
    "net"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// maxErrorBodyBytes caps how much of an upstream error response body we read
// into memory for surfacing in error messages. Error bodies are diagnostics
// (status text, upstream message), not model output — 1 MiB is far beyond any
// legitimate diagnostic and bounds memory when a misbehaving backend streams
// a huge body on a non-200. Mirrors the SSE line cap (maxLineSize = 1<<20).
const maxErrorBodyBytes = 1 << 20 // 1 MiB

// readErrorBody reads a capped slice of resp.Body for inclusion in an error
// message. It is the bounded replacement for the bare `io.ReadAll(resp.Body)`
// pattern on non-200 paths: an unbounded read lets a hostile or buggy upstream
// exhaust gateway memory by streaming gigabytes on an error status. The cap is
// advisory for diagnostics — truncation only loses trailing error detail,
// never a model payload. Always called on the error path where the body is
// about to be discarded anyway, so Close is the caller's responsibility.
func ReadErrorBody(resp *http.Response) []byte {
    b, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
    if err != nil {
        slog.Debug("failed to read capped upstream error body", "error", err)
        return nil
    }
    return b
}

// TruncateForError trims a body slice for safe embedding in an error string,
// keeping the leading window that usually holds the upstream's own error
// message. Kept separate from ReadErrorBody so callers that already hold a
// slice (e.g. a partially-read buffer) can still bound it.
func TruncateForError(b []byte) string {
    const maxEmbed = 512
    if len(b) <= maxEmbed {
        return string(b)
    }
    return fmt.Sprintf("%s... (truncated %d bytes)", b[:maxEmbed], len(b)-maxEmbed)
}

// TransportForBackend builds an http.RoundTripper tuned for a backend.
//
// When BackendConfig.SocketPath is set, the transport dials a Unix Domain
// Socket instead of TCP — this is the outbound UDS path that lets the gateway
// talk to a local backend (e.g. fusion-mlx launched with --host unix:/path)
// without crossing the TCP stack. base_url is then a dummy host
// (convention: http://unix/); the dialer ignores it and connects to the socket.
//
// When SocketPath is empty, a cloned DefaultTransport is returned. Either way
// the pool is tuned for the local hot path: MaxIdleConns 100,
// MaxIdleConnsPerHost 64 (the Go default of 2 starves a high-QPS local backend
// and forces redials), IdleConnTimeout 90s.
func TransportForBackend(cfg config.BackendConfig) http.RoundTripper {
    if cfg.SocketPath == "" {
        t, ok := http.DefaultTransport.(*http.Transport)
        if !ok {
            slog.Warn("DefaultTransport is not *http.Transport, using fresh transport for TCP backend", "backend", cfg.BaseURL)
            t = &http.Transport{}
        }
        cloned := t.Clone()
        cloned.MaxIdleConns = 100
        cloned.MaxIdleConnsPerHost = 64
        cloned.IdleConnTimeout = 90 * time.Second
        return cloned
    }

    slog.Info("building UDS transport for backend",
        "socket_path", cfg.SocketPath,
        "base_url", cfg.BaseURL,
    )
    return &http.Transport{
        DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
            d := net.Dialer{}
            return d.DialContext(ctx, "unix", cfg.SocketPath)
        },
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 64,
        IdleConnTimeout:     90 * time.Second,
    }
}
