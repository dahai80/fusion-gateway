package server

import (
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"
    "time"
)

// Issue #602: identity stalls leave half-dead keep-alive connections in the
// portal's pooled transport — /auth/api/identity/* hung 0-byte until client
// timeout even after identity recovered, and only a gateway restart healed
// it. Required: bounded round-trip surfacing as 504, stale-conn drop +
// one fresh-connection retry, and a tiny fail-fast breaker.

func portal602(t *testing.T, upstream *url.URL) *authPortal {
    t.Helper()
    // Production client shape but with test-speed bounds — the full 5s
    // response-header bound × retries pushes the package past its -race
    // timeout budget. Structure under test (retry, breaker, 504 mapping)
    // is identical.
    cl := newIdentityHTTPClient()
    if tr, ok := cl.Transport.(*http.Transport); ok {
        tr.ResponseHeaderTimeout = 250 * time.Millisecond
    }
    return &authPortal{
        identity:   upstream,
        httpClient: cl,
    }
}

func Test602_TransportIsDedicated(t *testing.T) {
    p := portal602(t, nil)
    if p.httpClient.Timeout != identityProxyTimeout {
        t.Fatalf("client timeout = %s, want %s", p.httpClient.Timeout, identityProxyTimeout)
    }
    tr, ok := p.httpClient.Transport.(*http.Transport)
    if !ok {
        t.Fatal("portal transport must be a dedicated *http.Transport, not http.DefaultTransport")
    }
    if tr.IdleConnTimeout > 60*time.Second {
        t.Fatalf("idle conn timeout %s must be short so a stalled pool drains", tr.IdleConnTimeout)
    }
    if tr.ResponseHeaderTimeout > 10*time.Second {
        t.Fatalf("response header timeout %s must bound a silent upstream", tr.ResponseHeaderTimeout)
    }
}

func Test602_IdentityHangSurfaces504Fast(t *testing.T) {
    // accept-but-never-respond peer — the half-dead shape that poisoned the
    // pool in the live incident.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        select {
        case <-r.Context().Done():
        case <-time.After(30 * time.Second):
        }
    }))
    defer srv.Close()
    u, err := url.Parse(srv.URL)
    if err != nil {
        t.Fatalf("parse: %v", err)
    }
    p := portal602(t, u)
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(`{"username":"a"}`))
    w := httptest.NewRecorder()
    start := time.Now()
    code := p.forwardIdentity(w, req, "/api/v1/auth/login", []byte(`{"username":"a"}`))
    elapsed := time.Since(start)
    if code != http.StatusGatewayTimeout {
        t.Fatalf("hung upstream must surface 504, got %d body=%s", code, w.Body.String())
    }
    // 5s response-header bound + one fresh-conn retry = ~10s ceiling; must
    // never ride the full 15s Client.Timeout.
    if elapsed > 13*time.Second {
        t.Fatalf("round-trip took %s, must be bounded well under client timeout", elapsed)
    }
}

func Test602_BreakerFailsFastAfterConsecutiveTimeouts(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        select {
        case <-r.Context().Done():
        case <-time.After(30 * time.Second):
        }
    }))
    defer srv.Close()
    u, _ := url.Parse(srv.URL)
    p := portal602(t, u)
    for i := 0; i < identityBreakerThreshold; i++ {
        req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(`{}`))
        w := httptest.NewRecorder()
        if code := p.forwardIdentity(w, req, "/api/v1/auth/login", []byte(`{}`)); code != http.StatusGatewayTimeout {
            t.Fatalf("attempt %d: expected 504, got %d", i+1, code)
        }
    }
    // Breaker open: next request fails fast WITHOUT an upstream round-trip.
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(`{}`))
    w := httptest.NewRecorder()
    start := time.Now()
    if code := p.forwardIdentity(w, req, "/api/v1/auth/login", []byte(`{}`)); code != http.StatusServiceUnavailable {
        t.Fatalf("open breaker must fail fast 503, got %d", code)
    }
    if time.Since(start) > 500*time.Millisecond {
        t.Fatalf("open breaker must not round-trip upstream, took %s", time.Since(start))
    }
}

func Test602_SuccessResetsFailCounter(t *testing.T) {
    ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{}`))
    }))
    defer ok.Close()
    u, _ := url.Parse(ok.URL)
    p := portal602(t, u)
    p.identityBreakerFails.Store(identityBreakerThreshold - 1)
    req := httptest.NewRequest(http.MethodPost, "/auth/api/identity/api/v1/auth/login", strings.NewReader(`{}`))
    w := httptest.NewRecorder()
    if code := p.forwardIdentity(w, req, "/api/v1/auth/login", []byte(`{}`)); code != http.StatusOK {
        t.Fatalf("healthy upstream must pass, got %d", code)
    }
    if p.identityBreakerFails.Load() != 0 || p.identityBreakerOpenUntil.Load() != 0 {
        t.Fatal("success must reset the fail counter and keep the breaker closed")
    }
}

func Test602_BreakerExpiresAfterCooldown(t *testing.T) {
    p := portal602(t, nil)
    p.identityBreakerOpenUntil.Store(time.Now().Add(-time.Second).UnixNano())
    if p.identityBreakerTripped() {
        t.Fatal("expired open window must clear, not trip")
    }
    if p.identityBreakerOpenUntil.Load() != 0 || p.identityBreakerFails.Load() != 0 {
        t.Fatal("expiry must reset breaker state")
    }
}
