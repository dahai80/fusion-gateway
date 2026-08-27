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
    // RR11 (audit P2): MaxIdleConnsPerHost is NOT enough — MaxConnsPerHost=0
    // means unlimited ACTIVE connections to the single host, an FD-exhaustion
    // vector under a concurrent burst. The factory must set it (default 16).
    if tpt.MaxConnsPerHost != defaultMaxConnsPerHost {
        t.Errorf("MaxConnsPerHost: want %d (RR11 default), got %d", defaultMaxConnsPerHost, tpt.MaxConnsPerHost)
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

// TestRR11_MaxConnsPerHost_Default verifies the RR11 FD-exhaustion guard: when
// a backend config omits max_conns_per_host, TransportForBackend applies a
// non-zero default (16). The prior code left Go's default 0 = unlimited active
// connections to a single host — a concurrent burst opened hundreds of conns
// and exhausted the process FD table. Both TCP and UDS paths must cap it.
func TestRR11_MaxConnsPerHost_Default(t *testing.T) {
    t.Run("tcp_default", func(t *testing.T) {
        rt := TransportForBackend(config.BackendConfig{BaseURL: "http://127.0.0.1:11434"})
        tpt, ok := rt.(*http.Transport)
        if !ok {
            t.Fatalf("expected *http.Transport, got %T", rt)
        }
        if tpt.MaxConnsPerHost != defaultMaxConnsPerHost {
            t.Fatalf("MaxConnsPerHost: want default %d, got %d", defaultMaxConnsPerHost, tpt.MaxConnsPerHost)
        }
    })
    t.Run("uds_default", func(t *testing.T) {
        rt := TransportForBackend(config.BackendConfig{BaseURL: "http://unix/", SocketPath: "/tmp/rr11-probe.sock"})
        tpt, ok := rt.(*http.Transport)
        if !ok {
            t.Fatalf("expected *http.Transport, got %T", rt)
        }
        if tpt.MaxConnsPerHost != defaultMaxConnsPerHost {
            t.Fatalf("MaxConnsPerHost: want default %d, got %d", defaultMaxConnsPerHost, tpt.MaxConnsPerHost)
        }
    })
}

// TestRR11_MaxConnsPerHost_Override verifies a per-backend config override
// (max_conns_per_host) is honored on both the active and idle pools — a
// high-capacity cloud vendor can raise the cap, a tight local can lower it.
func TestRR11_MaxConnsPerHost_Override(t *testing.T) {
    rt := TransportForBackend(config.BackendConfig{
        BaseURL:          "http://127.0.0.1:11434",
        MaxConnsPerHost:  48,
        MaxIdleConnsPerHost: 12,
    })
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport, got %T", rt)
    }
    if tpt.MaxConnsPerHost != 48 {
        t.Errorf("MaxConnsPerHost: want 48 (override), got %d", tpt.MaxConnsPerHost)
    }
    if tpt.MaxIdleConnsPerHost != 12 {
        t.Errorf("MaxIdleConnsPerHost: want 12 (override), got %d", tpt.MaxIdleConnsPerHost)
    }
}

// TestRR11_MaxConnsPerHost_NegativeFallsBack verifies a stray negative config
// value does not propagate as a negative transport cap (which Go treats as
// unlimited) — the factory falls back to the safe default instead.
func TestRR11_MaxConnsPerHost_NegativeFallsBack(t *testing.T) {
    rt := TransportForBackend(config.BackendConfig{
        BaseURL:         "http://127.0.0.1:11434",
        MaxConnsPerHost: -5,
    })
    tpt, ok := rt.(*http.Transport)
    if !ok {
        t.Fatalf("expected *http.Transport, got %T", rt)
    }
    if tpt.MaxConnsPerHost != defaultMaxConnsPerHost {
        t.Errorf("MaxConnsPerHost: negative config must fall back to default %d, got %d", defaultMaxConnsPerHost, tpt.MaxConnsPerHost)
    }
}

// TestRR11_AllProvidersUseCappedTransport is the "no bare &http.Client{}" guard.
// Every cloud/local provider constructor must build its *http.Client through
// TransportForBackend so it inherits the MaxConnsPerHost FD cap. A bare
// &http.Client{Timeout: x} inherits http.DefaultTransport (MaxConnsPerHost=0 =
// unlimited) — the exact gap RR11 closes. This test asserts each provider's
// unexported httpClient field holds a client whose Transport is an
// *http.Transport with a non-zero MaxConnsPerHost. The httpClient field is
// same-package accessible (test is in package adapter).
func TestRR11_AllProvidersUseCappedTransport(t *testing.T) {
    cfg := config.BackendConfig{BaseURL: "http://127.0.0.1:1", APIKey: "k"}

    check := func(name string, c *http.Client) {
        t.Helper()
        if c == nil {
            t.Fatalf("%s: nil http.Client", name)
        }
        tpt, ok := c.Transport.(*http.Transport)
        if !ok {
            t.Fatalf("%s: Transport not *http.Transport (got %T) — bare client slipped through", name, c.Transport)
        }
        if tpt.MaxConnsPerHost <= 0 {
            t.Fatalf("%s: MaxConnsPerHost=%d — RR11 FD cap missing (0/unlimited is the bug)", name, tpt.MaxConnsPerHost)
        }
    }

    check("minimax", NewMinimaxProvider("minimax", cfg).httpClient)
    check("volcengine", NewVolcengineProvider("volcengine", cfg).httpClient)
    check("moonshot", NewMoonshotProvider("moonshot", cfg).httpClient)
    check("hunyuan", NewHunyuanProvider("hunyuan", cfg).httpClient)
    check("dashscope", NewDashScopeProvider("dashscope", cfg).httpClient)
    check("zhipu", NewZhipuProvider("zhipu", cfg).httpClient)
    check("qianfan", NewQianfanProvider("qianfan", cfg).httpClient)
    check("yi", NewYiProvider("yi", cfg).httpClient)
    check("deepseek", NewDeepSeekProvider("deepseek", cfg).httpClient)
    check("baichuan", NewBaichuanProvider("baichuan", cfg).httpClient)
    check("stepfun", NewStepFunProvider("stepfun", cfg).httpClient)
    check("openrouter", NewOpenRouterProvider("openrouter", cfg).httpClient)
    check("fusion_kb", NewFusionKBProvider("fusion_kb", cfg).httpClient)
    check("foundry", NewFoundryProvider("foundry", cfg).httpClient)
    check("bedrock", NewBedrockProvider("bedrock", cfg).httpClient)
    // vertex has two clients (api + token-refresh); both must be capped.
    v := NewVertexProvider("vertex", cfg)
    check("vertex.api", v.httpClient)
    check("vertex.token", v.tokenClient)
    // anthropic: non-stream + stream clients, both via TransportForBackend.
    a := NewAnthropicProvider("anthropic", cfg)
    check("anthropic.api", a.httpClient)
    check("anthropic.stream", a.streamHTTPClient)
    // openai_compatible + fusion_mlx were already on TransportForBackend (B5);
    // assert the RR11 cap propagated to them too.
    check("openai_compatible", NewOpenAICompatibleProvider("openai_compatible", cfg).httpClient)
    check("fusion_mlx", NewFusionMLXProvider(cfg, config.RoutingConfig{}).httpClient)
}
