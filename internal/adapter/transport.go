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

// maxResponseBytes caps how much of a non-streaming SUCCESS response body the
// gateway reads into memory (RR9, audit P0). The success path previously used
// json.NewDecoder(resp.Body).Decode with no bound — a misbehaving or
// man-in-the-middle backend returning a 2GB 200 JSON would drive Decode to OOM
// the whole gateway process. 10 MiB matches the SSE aggregate response cap and
// is far beyond any legitimate single non-streaming LLM response. Wrap resp.Body
// with LimitResponseReader before handing it to NewDecoder/ReadAll.
const maxResponseBytes = 10 << 20 // 10 MiB

// LimitResponseReader wraps a success-path response body in a 10 MiB cap (RR9).
// Use it on every non-streaming aggregation path: json.NewDecoder(LimitResponseReader(resp.Body))
// or io.ReadAll(LimitResponseReader(resp.Body)). Streaming/SSE paths keep their
// own line+aggregate caps and must not be wrapped (wrapping would cap the stream).
func LimitResponseReader(r io.Reader) io.Reader {
    return io.LimitReader(r, maxResponseBytes)
}

// readErrorBody reads a capped slice of resp.Body for inclusion in an error
// message. It is the bounded replacement for the bare `io.ReadAll(resp.Body)`
// pattern on non-200 paths: an unbounded read lets a hostile or buggy upstream
// exhaust gateway memory by streaming gigabytes on an error status. The cap is
// advisory for diagnostics — truncation only loses trailing error detail,
// never a model payload. Always called on the error path where the body is
// about to be discarded anyway, so Close is the caller's responsibility.
//
// RR10 (audit P0): the read error is surfaced at Warn (not silently Debug) so
// an upstream IO fault / truncation is observable and distinguishable from a
// legitimately empty error body. The signature stays []byte (single return) to
// avoid a sweeping 25-caller refactor; observability is restored via the log
// level rather than a return-value contract change.
func ReadErrorBody(resp *http.Response) []byte {
    b, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
    if err != nil {
        slog.Warn("failed to read capped upstream error body (read fault, not empty body)",
            "error", err, "note", "error body unavailable; surfacing status only")
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

// defaultMaxConnsPerHost is the active-connection cap applied to a backend
// host when BackendConfig.MaxConnsPerHost is unset (0). 16 is a deliberate
// ceiling for a single-host backend: high enough not to serialize a healthy
// burst, low enough that a single misbehaving host cannot open hundreds of
// concurrent connections and exhaust the process FD table. Per-backend
// overrides via config (max_conns_per_host) let a known-high-capacity cloud
// vendor raise this.
const defaultMaxConnsPerHost = 16

// defaultMaxIdleConnsPerHost is the idle keep-alive pool size per host when
// BackendConfig.MaxIdleConnsPerHost is unset. 64 (vs the Go default of 2)
// avoids redialing a high-QPS local backend on every request.
const defaultMaxIdleConnsPerHost = 64

// defaultResponseHeaderTimeout is how long the transport waits for upstream
// response HEADERS after the request is fully sent, when
// BackendConfig.ResponseHeaderTimeout is unset (R5). 30s is deliberately far
// below the typical Client.Timeout (120s): a stuck upstream that accepted the
// connection but never sent headers must fail HERE, not occupy a full 120s
// slot — otherwise a handful of stuck upstreams saturates the bounded
// MaxConnsPerHost pool (16) and stalls ALL requests to that backend.
const defaultResponseHeaderTimeout = 30 * time.Second

// defaultDialTimeout caps the TCP/UDS dial. R5: the prior TCP transport cloned
// DefaultTransport (which carries a 30s DialContext) but the UDS transport had
// no dial timeout at all — a dead socket could hang the dial indefinitely. 10s
// is long enough for any healthy intranet/cloud dial, short enough to fail fast.
const defaultDialTimeout = 10 * time.Second

// defaultTLSHandshakeTimeout caps the TLS handshake (R5). The prior TCP
// transport inherited DefaultTransport's 10s, but the UDS transport had none.
// Applied uniformly now.
const defaultTLSHandshakeTimeout = 10 * time.Second

func resolveResponseHeaderTimeout(cfg config.BackendConfig) time.Duration {
    if cfg.ResponseHeaderTimeout > 0 {
        return cfg.ResponseHeaderTimeout
    }
    return defaultResponseHeaderTimeout
}

// resolveMaxConnsPerHost returns the configured cap or the safe default,
// logging when the default is applied so an operator can see the effective FD
// bound per host. A negative config value is treated as "no explicit cap" (the
// caller still applies the default) rather than panicking.
func resolveMaxConnsPerHost(cfg config.BackendConfig) int {
    if cfg.MaxConnsPerHost > 0 {
        return cfg.MaxConnsPerHost
    }
    slog.Info("transport: applying default max_conns_per_host",
        "backend", cfg.BaseURL, "default", defaultMaxConnsPerHost,
        "note", "set max_conns_per_host in config to override")
    return defaultMaxConnsPerHost
}

func resolveMaxIdleConnsPerHost(cfg config.BackendConfig) int {
    if cfg.MaxIdleConnsPerHost > 0 {
        return cfg.MaxIdleConnsPerHost
    }
    return defaultMaxIdleConnsPerHost
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
// MaxIdleConnsPerHost (default 64; the Go default of 2 starves a high-QPS
// local backend and forces redials), IdleConnTimeout 90s.
//
// RR11 (audit P2): MaxConnsPerHost is now always set (default 16 when unset in
// config). The prior code left Go's default of 0 = unlimited active
// connections to a single host — a concurrent burst opened hundreds of
// simultaneous connections and exhausted the process FD table, taking down
// accept/log/other-backend FDs alongside the offending host. This is the single
// hard gate on per-host connection growth. Every cloud/local provider MUST
// route its *http.Client through this factory; bare &http.Client{Timeout} is
// forbidden (it inherits http.DefaultTransport which has no MaxConnsPerHost).
func TransportForBackend(cfg config.BackendConfig) http.RoundTripper {
    maxConns := resolveMaxConnsPerHost(cfg)
    maxIdle := resolveMaxIdleConnsPerHost(cfg)
    respHeaderTimeout := resolveResponseHeaderTimeout(cfg)

    if cfg.SocketPath == "" {
        t, ok := http.DefaultTransport.(*http.Transport)
        if !ok {
            slog.Warn("DefaultTransport is not *http.Transport, using fresh transport for TCP backend", "backend", cfg.BaseURL)
            t = &http.Transport{}
        }
        cloned := t.Clone()
        cloned.MaxIdleConns = 100
        cloned.MaxIdleConnsPerHost = maxIdle
        cloned.MaxConnsPerHost = maxConns
        cloned.IdleConnTimeout = 90 * time.Second
        // R5 (audit P0): bound header wait, dial, and TLS handshake so a stuck
        // upstream cannot occupy a full Client.Timeout slot and, with the bounded
        // MaxConnsPerHost pool, stall all requests to the backend. Without
        // ResponseHeaderTimeout a connected-but-silent upstream hung for 120s.
        cloned.ResponseHeaderTimeout = respHeaderTimeout
        cloned.DialContext = (&net.Dialer{Timeout: defaultDialTimeout}).DialContext
        cloned.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
        slog.Info("transport: TCP backend timeouts applied",
            "backend", cfg.BaseURL,
            "response_header_timeout", respHeaderTimeout.String(),
            "dial_timeout", defaultDialTimeout.String(),
            "tls_handshake_timeout", defaultTLSHandshakeTimeout.String(),
            "max_conns_per_host", maxConns,
            "max_idle_conns_per_host", maxIdle,
        )
        return cloned
    }

    slog.Info("building UDS transport for backend",
        "socket_path", cfg.SocketPath,
        "base_url", cfg.BaseURL,
        "max_conns_per_host", maxConns,
        "max_idle_conns_per_host", maxIdle,
        "response_header_timeout", respHeaderTimeout.String(),
    )
    return &http.Transport{
        DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
            d := net.Dialer{Timeout: defaultDialTimeout}
            return d.DialContext(ctx, "unix", cfg.SocketPath)
        },
        MaxIdleConns:           100,
        MaxIdleConnsPerHost:    maxIdle,
        MaxConnsPerHost:        maxConns,
        IdleConnTimeout:        90 * time.Second,
        ResponseHeaderTimeout:  respHeaderTimeout,
        TLSHandshakeTimeout:    defaultTLSHandshakeTimeout,
        ExpectContinueTimeout:  1 * time.Second,
    }
}
