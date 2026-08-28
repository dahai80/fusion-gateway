package adapter

// N2 (audit): transport + bounded-reader helpers moved to internal/httpx so
// non-adapter packages (cluster, middleware, hardware, router) import the
// httpx leaf instead of reaching into adapter (the provider layer). This file
// is now a thin delegating shim so the ~111 in-adapter call sites keep working
// unchanged (Rule 3: surgical). New external callers should import httpx
// directly; these wrappers exist for backward compat within adapter only.
//
// The exported constants below re-export the httpx defaults so existing
// adapter tests that assert against them keep compiling.

import (
    "io"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

// DefaultMaxConnsPerHost re-exports the httpx default for in-adapter tests
// (transport_test.go asserts MaxConnsPerHost == DefaultMaxConnsPerHost).
const DefaultMaxConnsPerHost = httpx.DefaultMaxConnsPerHost

// DefaultResponseHeaderTimeout re-exports the httpx default for in-adapter
// tests (r5_test.go asserts the R5 header timeout).
const DefaultResponseHeaderTimeout = httpx.DefaultResponseHeaderTimeout

// LimitResponseReader delegates to httpx.LimitResponseReader (10 MiB success-
// path body cap, RR9). In-adapter callers use the unqualified name; behavior
// is identical to the pre-N2 implementation.
func LimitResponseReader(r io.Reader) io.Reader {
    return httpx.LimitResponseReader(r)
}

// ReadErrorBody delegates to httpx.ReadErrorBody (capped 1 MiB error-body
// read, RR10). Single-return signature preserved to avoid a 25-caller refactor.
func ReadErrorBody(resp *http.Response) []byte {
    return httpx.ReadErrorBody(resp)
}

// TruncateForError delegates to httpx.TruncateForError (512-byte error embed).
func TruncateForError(b []byte) string {
    return httpx.TruncateForError(b)
}

// TransportForBackend delegates to httpx.TransportForBackend. Every
// cloud/local provider MUST route its *http.Client through this (RR11
// MaxConnsPerHost cap + R5 header/dial/TLS timeouts); bare
// &http.Client{Timeout} is forbidden.
func TransportForBackend(cfg config.BackendConfig) http.RoundTripper {
    return httpx.TransportForBackend(cfg)
}
