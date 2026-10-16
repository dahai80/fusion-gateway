package shell

import (
    "bufio"
    "context"
    "net"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestProxy_ForwardsToUpstream(t *testing.T) {
    var gotPath string
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("hello-from-module"))
    }))
    defer upstream.Close()

    p := NewProxy(func(string) (string, string) { return upstream.URL, "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/index.html", nil))
    if w.Code != http.StatusOK || w.Body.String() != "hello-from-module" {
        t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
    }
    if gotPath != "/index.html" {
        t.Fatalf("upstream path = %q", gotPath)
    }
}

func TestProxy_UpstreamDownEnvelope(t *testing.T) {
    p := NewProxy(func(id string) (string, string) {
        if id == "fusion-dead" {
            return "http://127.0.0.1:1", ""
        }
        return "", ""
    })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-dead/api/models", nil))
    if w.Code != http.StatusServiceUnavailable {
        t.Fatalf("code = %d, want 503", w.Code)
    }
    body := w.Body.String()
    for _, want := range []string{`"code":"backend_unavailable"`, `"module_id":"fusion-dead"`, `"retryable":true`} {
        if !strings.Contains(body, want) {
            t.Errorf("envelope missing %s: %s", want, body)
        }
    }
}

func TestProxy_ModuleNotMapped(t *testing.T) {
    p := NewProxy(func(string) (string, string) { return "", "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-unknown/x", nil))
    if w.Code != http.StatusNotFound {
        t.Fatalf("code = %d, want 404", w.Code)
    }
    if !strings.Contains(w.Body.String(), `"code":"module_not_found"`) {
        t.Fatalf("body = %s", w.Body.String())
    }
}

func TestProxy_MissingModuleID(t *testing.T) {
    p := NewProxy(func(string) (string, string) { return "http://127.0.0.1:1", "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/", nil))
    if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "missing module id") {
        t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
    }
}

func TestProxy_InvalidUpstreamURL(t *testing.T) {
    p := NewProxy(func(string) (string, string) { return "not-a-url", "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-x/y", nil))
    if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "upstream_invalid") {
        t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
    }
}

func TestHealthPoller_RecordsObservations(t *testing.T) {
    var hits atomic.Int64
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        hits.Add(1)
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()

    m := validManifest("fusion-mlx")
    m.Health = upstream.URL + "/health"
    r := NewRegistry([]config.ShellManifest{m}, nil, 30*time.Second)
    poller := NewHealthPoller(r, 10*time.Millisecond)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go poller.Run(ctx)
    time.Sleep(60 * time.Millisecond)
    if hits.Load() == 0 {
        t.Fatal("poller never probed the health URL")
    }
    views := r.List()
    if len(views) != 1 || views[0].Status != StatusOnline {
        t.Fatalf("want online dynamic registration, got %+v", views)
    }
}

func TestHealthPoller_MarksDown(t *testing.T) {
    m := validManifest("fusion-dead")
    m.Health = "http://127.0.0.1:1/health"
    r := NewRegistry([]config.ShellManifest{m}, nil, 30*time.Second)
    poller := NewHealthPoller(r, 10*time.Millisecond)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go poller.Run(ctx)
    time.Sleep(60 * time.Millisecond)
    views := r.List()
    if views[0].Status != StatusOffline {
        t.Fatalf("want offline after failed probes, got %s", views[0].Status)
    }
}

func TestProxy_UpstreamCredentialInjection(t *testing.T) {
    var gotAuth, gotCookie string
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotAuth = r.Header.Get("Authorization")
        gotCookie = r.Header.Get("Cookie")
        _, _ = w.Write([]byte(`{"ok":true}`))
    }))
    defer upstream.Close()
    p := NewProxy(func(id string) (string, string) {
        if id == "fusion-mlx" {
            return upstream.URL, "module-secret-key"
        }
        return "", ""
    })
    req := httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/v1/models", nil)
    req.Header.Set("Authorization", "Bearer user-session-token") // caller token must NOT leak upstream
    req.Header.Set("Cookie", "fusion_shell_session=leak-me")
    w := httptest.NewRecorder()
    p.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
    }
    if gotAuth != "Bearer module-secret-key" {
        t.Fatalf("upstream Authorization = %q, want injected module key", gotAuth)
    }
    if gotCookie != "" {
        t.Fatalf("session cookie leaked upstream: %q", gotCookie)
    }
}

// TestProxy_DynamicUpstreamFromRegistry (#232): a manifest registered with
// its own upstream/api_key is proxied without static config; credential
// injection semantics are identical to the static path.
func TestProxy_DynamicUpstreamFromRegistry(t *testing.T) {
    var gotAuth string
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotAuth = r.Header.Get("Authorization")
        _, _ = w.Write([]byte(`{"dynamic":true}`))
    }))
    defer upstream.Close()

    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-cowork")
    m.Upstream = upstream.URL
    m.APIKey = "dynamic-module-key"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }

    p := NewProxy(func(id string) (string, string) {
        if up, key, ok := r.UpstreamFor(id); ok {
            return up, key
        }
        return "", ""
    })
    req := httptest.NewRequest(http.MethodGet, "/shell/fusion-cowork/runs", nil)
    req.Header.Set("Authorization", "Bearer user-session-token")
    w := httptest.NewRecorder()
    p.ServeHTTP(w, req)
    if w.Code != http.StatusOK || w.Body.String() != `{"dynamic":true}` {
        t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
    }
    if gotAuth != "Bearer dynamic-module-key" {
        t.Fatalf("upstream Authorization = %q", gotAuth)
    }

    // Static override wins: a static mapping replaces the dynamic upstream.
    staticUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte(`{"static":true}`))
    }))
    defer staticUp.Close()
    p2 := NewProxy(func(id string) (string, string) {
        if id == "fusion-cowork" {
            return staticUp.URL, ""
        }
        if up, key, ok := r.UpstreamFor(id); ok {
            return up, key
        }
        return "", ""
    })
    w2 := httptest.NewRecorder()
    p2.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/shell/fusion-cowork/runs", nil))
    if w2.Body.String() != `{"static":true}` {
        t.Fatalf("static override body = %q", w2.Body.String())
    }
}

// TestRegistry_ListZeroesAPIKey (#232): the session-visible list output must
// never carry the upstream credential.
func TestRegistry_ListZeroesAPIKey(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-secret")
    m.Upstream = "http://127.0.0.1:9100"
    m.APIKey = "super-secret"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    for _, v := range r.List() {
        if v.APIKey != "" {
            t.Fatalf("list leaked api_key for %s", v.ID)
        }
    }
    // But the proxy path still resolves it.
    if _, key, ok := r.UpstreamFor("fusion-secret"); !ok || key != "super-secret" {
        t.Fatalf("UpstreamFor = %q %v", key, ok)
    }
}

// TestRegistry_UpsertRejectsBadUpstream (#232): an upstream, when carried,
// must be an absolute http(s) URL.
func TestRegistry_UpsertRejectsBadUpstream(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-bad")
    m.Upstream = "not-a-url"
    if err := r.Upsert(m); err == nil {
        t.Fatal("relative upstream accepted")
    }
    m.Upstream = "ftp://127.0.0.1:1"
    if err := r.Upsert(m); err == nil {
        t.Fatal("non-http upstream accepted")
    }
}

// TestProxy_WebSocketUpgradePassthrough (#232): the /shell/ proxy passes
// WebSocket upgrades through bidirectionally (httputil.ReverseProxy hijack
// path), with the same credential-injection semantics as plain requests.
func TestProxy_WebSocketUpgradePassthrough(t *testing.T) {
    upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Upgrade") != "websocket" {
            http.Error(w, "not an upgrade", http.StatusBadRequest)
            return
        }
        if r.Header.Get("Authorization") != "Bearer ws-module-key" {
            http.Error(w, "missing module credential", http.StatusUnauthorized)
            return
        }
        h := w.(http.Hijacker)
        conn, buf, _ := h.Hijack()
        defer conn.Close()
        resp := "HTTP/1.1 101 Switching Protocols\r\n" +
            "Upgrade: websocket\r\n" +
            "Connection: Upgrade\r\n" +
            "Sec-WebSocket-Accept: fake\r\n\r\n"
        _, _ = buf.WriteString(resp)
        _ = buf.Flush()
        // Echo one line back as the "socket" payload.
        line, err := buf.ReadString('\n')
        if err != nil {
            return
        }
        _, _ = buf.WriteString("echo:" + line)
        _ = buf.Flush()
    }))
    defer upSrv.Close()

    p := NewProxy(func(string) (string, string) { return upSrv.URL, "ws-module-key" })
    proxySrv := httptest.NewServer(http.HandlerFunc(p.ServeHTTP))
    defer proxySrv.Close()

    // Raw TCP client speaking a minimal WS handshake — httptest.NewRecorder
    // cannot represent a hijacked connection.
    var d net.Dialer
    conn, err := d.DialContext(context.Background(), "tcp", proxySrv.Listener.Addr().String())
    if err != nil {
        t.Fatalf("dial: %v", err)
    }
    defer conn.Close()
    target := strings.TrimPrefix(upSrv.URL, "http://")
    req := "GET /shell/fusion-cowork/ws HTTP/1.1\r\n" +
        "Host: " + target + "\r\n" +
        "Upgrade: websocket\r\n" +
        "Connection: Upgrade\r\n" +
        "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
        "Sec-WebSocket-Version: 13\r\n\r\n"
    if _, err := conn.Write([]byte(req)); err != nil {
        t.Fatalf("write handshake: %v", err)
    }
    // Read until end of the upgrade response head.
    br := bufio.NewReader(conn)
    status, err := br.ReadString('\n')
    if err != nil {
        t.Fatalf("read status: %v", err)
    }
    if !strings.Contains(status, "101") {
        t.Fatalf("upgrade status = %q", status)
    }
    for {
        line, err := br.ReadString('\n')
        if err != nil || line == "\r\n" {
            break
        }
    }
    if _, err := conn.Write([]byte("ping\n")); err != nil {
        t.Fatalf("write payload: %v", err)
    }
    reply := make([]byte, 16)
    n, err := br.Read(reply)
    if err != nil || !strings.HasPrefix(string(reply[:n]), "echo:ping") {
        t.Fatalf("ws payload = %q err=%v", string(reply[:n]), err)
    }
}
