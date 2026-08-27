package server

import (
    "context"
    "fmt"
    "io"
    "net"
    "net/http"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/mcp"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// newMCPListenerTestServer builds a Server whose MCP dedicated listener is bound
// to an OS-assigned port (host 127.0.0.1, port 0) and serving the MCP routes
// under the MCP auth gate as the sole middleware. It does NOT call Start() (which
// would bind the main :11432 mux too); it constructs mcpServer/mcpListener/
// mcpHandler/mcpGate directly so the test isolates the MCP listener. Returns the
// server and the live base URL (http://127.0.0.1:<port>).
func newMCPListenerTestServer(t *testing.T, token, masterKey string) (*Server, string) {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.MCP.Enabled = true
    cfg.Config.MCP.ListenEnabled = true
    cfg.Config.MCP.Host = "127.0.0.1"
    cfg.Config.MCP.Port = 0 // OS-assigned
    cfg.Config.MCP.Token = token
    cfg.Config.Auth.MasterKey = masterKey

    gw := mcp.NewMCPClusterGateway(mcp.GatewayConfig{Host: cfg.Config.MCP.Host, Port: cfg.Config.MCP.Port})
    gw.Start()
    handler := mcp.NewHandler(gw)

    s := newTestServer()
    s.cfg = cfg
    s.mcpHandler = handler
    s.mcpGate = mcp.AuthGate(mcp.AuthConfig{Token: token, MasterKey: masterKey})

    if err := s.startMCPListener(); err != nil {
        t.Fatalf("startMCPListener: %v", err)
    }
    // Read the OS-assigned port back from the bound listener.
    addr := s.mcpListener.Addr().(*net.TCPAddr)
    return s, fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
}

// TestMCPListener_RejectsAnonymous: dedicated listener up, no Authorization
// header → 401 on /mcp/v1/call. The MCP auth gate is the sole middleware, so
// this is the security guarantee: MCP is NOT reachable anonymously even though
// the test Server has auth.enabled=false (inherited from newTestServer).
// Guard: if the gate were not wired as the sole middleware on the dedicated
// listener, an anonymous call would reach handleToolCall (SSRF amplifier).
func TestMCPListener_RejectsAnonymous(t *testing.T) {
    s, base := newMCPListenerTestServer(t, "mcp-secret", "")
    defer s.mcpServer.Close()
    resp, err := http.Post(base+"/mcp/v1/call", "application/json", nil)
    if err != nil {
        t.Fatalf("post: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusUnauthorized {
        t.Fatalf("status=%d, want 401 (anonymous must be rejected on dedicated MCP listener)", resp.StatusCode)
    }
}

// TestMCPListener_ValidTokenPasses: Bearer <mcp.token> → not 401 (reaches the
// handler; the handler may 4xx on an empty body but the auth gate let it through).
// Guard: if the gate rejected a valid token, MCP would be unusable.
func TestMCPListener_ValidTokenPasses(t *testing.T) {
    s, base := newMCPListenerTestServer(t, "mcp-secret", "")
    defer s.mcpServer.Close()
    req, _ := http.NewRequest(http.MethodPost, base+"/mcp/v1/tools", nil)
    req.Header.Set("Authorization", "Bearer mcp-secret")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatalf("do: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusUnauthorized {
        t.Fatalf("status=401 with valid mcp.token — gate rejected a valid credential")
    }
}

// TestMCPListener_WrongTokenRejected: Bearer <wrong> → 401 on the dedicated
// listener. Guard: if any non-empty token passed, the gate would be a no-op.
func TestMCPListener_WrongTokenRejected(t *testing.T) {
    s, base := newMCPListenerTestServer(t, "mcp-secret", "")
    defer s.mcpServer.Close()
    req, _ := http.NewRequest(http.MethodPost, base+"/mcp/v1/tools", nil)
    req.Header.Set("Authorization", "Bearer wrong")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatalf("do: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusUnauthorized {
        t.Fatalf("status=%d, want 401 (wrong token must be rejected)", resp.StatusCode)
    }
}

// TestMCPListener_AuthDisabledDoesNotOpenMCP: the inherited test Server has
// auth.enabled=false + passthrough (newTestServer), yet the dedicated MCP
// listener STILL requires the token. This is the core #118 guarantee: the MCP
// auth gate is independent of the main auth chain.
// Guard: if the gate deferred to the main auth.enabled flag, disabling main auth
// would open MCP anonymously.
func TestMCPListener_AuthDisabledDoesNotOpenMCP(t *testing.T) {
    s, base := newMCPListenerTestServer(t, "mcp-secret", "")
    defer s.mcpServer.Close()
    if s.cfg.Config.Auth.Enabled {
        t.Fatal("test precondition: main auth must be disabled to prove MCP gate independence")
    }
    resp, err := http.Post(base+"/mcp/v1/call", "application/json", nil)
    if err != nil {
        t.Fatalf("post: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusUnauthorized {
        t.Fatalf("status=%d, want 401 — MCP gate must hold even with main auth.enabled=false", resp.StatusCode)
    }
}

// TestMCPListener_MasterKeyFallback: no mcp.token, Bearer <master_key> → passes
// the gate on the dedicated listener. Guard: the fallback branch must work on
// the dedicated path, not only in the unit gate.
func TestMCPListener_MasterKeyFallback(t *testing.T) {
    s, base := newMCPListenerTestServer(t, "", "master-secret")
    defer s.mcpServer.Close()
    req, _ := http.NewRequest(http.MethodPost, base+"/mcp/v1/tools", nil)
    req.Header.Set("Authorization", "Bearer master-secret")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatalf("do: %v", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusUnauthorized {
        t.Fatalf("status=401 with master_key — dedicated listener master-key fallback broken")
    }
}

// TestMCPListener_ShutdownDrainsListener: Server.Shutdown drains the dedicated
// MCP listener BEFORE the main httpServer (F7 ordering — drain the dependent
// before its upstream). We exercise the full Server.Shutdown path: it must
// close mcpListener so a new request fails to connect. The main httpServer is
// given a bound-but-unstarted dummy so Shutdown's httpServer.Shutdown call is
// a no-op (already closed), not a nil-deref.
// Guard: if the mcpServer drain block were absent from Server.Shutdown, the
// listener (and its serve goroutine) would leak past shutdown — a new start on
// the same port would collide, and in-flight MCP dials would be cut mid-flight
// by the main drain without first quiescing.
func TestMCPListener_ShutdownDrainsListener(t *testing.T) {
    s, base := newMCPListenerTestServer(t, "mcp-secret", "")
    // Give Server.Shutdown a real (bound, immediately-closed) httpServer so the
    // main-drain call at the end of Shutdown does not nil-deref. It must remain
    // non-nil and Shutdown-safe for the whole method to complete.
    dummyLn, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatalf("dummy listen: %v", err)
    }
    s.httpServer = &http.Server{Handler: http.NewServeMux()}
    safego.Go("dummy_serve", func() {
        _ = s.httpServer.Serve(dummyLn)
    })

    // Full shutdown path: MCP drain runs first, then main httpServer drain.
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := s.Shutdown(ctx); err != nil {
        t.Fatalf("shutdown: %v", err)
    }
    // The dedicated MCP listener must be closed by the drain — a new request
    // fails to connect.
    client := &http.Client{Timeout: 1 * time.Second}
    resp, perr := client.Post(base+"/mcp/v1/tools", "application/json", nil)
    if perr == nil {
        body, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        t.Fatalf("expected connection failure after shutdown, got status=%d body=%q", resp.StatusCode, string(body))
    }
}
