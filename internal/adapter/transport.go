package adapter

import (
    "context"
    "log/slog"
    "net"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

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
