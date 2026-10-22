package shell

import (
    "bufio"
    "context"
    "encoding/json"
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

// TestRegistry_RegistrationBodyDecodesAPIKey (#232 regression): api_key must
// DECODE from a registration body (json:"-" blocked decode, live-found) while
// list output stays clean.
func TestRegistry_RegistrationBodyDecodesAPIKey(t *testing.T) {
    var m config.ShellManifest
    body := `{"id":"fusion-x","title":"X","path":"/x","entry":"/shell/fusion-x/",` +
        `"devices":["desktop"],"category":"dev","upstream":"http://127.0.0.1:9100","api_key":"k123"}`
    if err := json.Unmarshal([]byte(body), &m); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if m.APIKey != "k123" || m.Upstream != "http://127.0.0.1:9100" {
        t.Fatalf("decoded = %+v", m)
    }
    r := NewRegistry(nil, nil, 30*time.Second)
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if _, key, ok := r.UpstreamFor("fusion-x"); !ok || key != "k123" {
        t.Fatalf("UpstreamFor = %q %v", key, ok)
    }
    // List JSON must not carry the secret.
    raw, _ := json.Marshal(r.List())
    if strings.Contains(string(raw), "k123") {
        t.Fatalf("list JSON leaked api_key: %s", raw)
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

// TestProxy_ExtPathUpstreamOverride (#256): manifest-declared ext endpoints
// dial the dynamic upstream while everything else keeps the static one.
func TestProxy_ExtPathUpstreamOverride(t *testing.T) {
    var staticPaths, extPaths atomic.Value
    staticPaths.Store([]string{})
    extPaths.Store([]string{})
    record := func(dst *atomic.Value, path string) {
        cur := dst.Load().([]string)
        dst.Store(append(cur, path))
    }
    staticSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        record(&staticPaths, r.URL.Path)
        _, _ = w.Write([]byte("static"))
    }))
    defer staticSrv.Close()
    extSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        record(&extPaths, r.URL.Path)
        if r.Header.Get("Authorization") != "Bearer fk-bridge" {
            w.WriteHeader(http.StatusUnauthorized)
            return
        }
        _, _ = w.Write([]byte("ext"))
    }))
    defer extSrv.Close()

    p := NewProxy(func(string) (string, string) { return staticSrv.URL, "fk-static" })
    p.SetExtResolver(func(moduleID, path string) (string, string, bool) {
        if path == "/shell/fusion-mlx/api/search/models" || strings.HasPrefix(path, "/shell/fusion-mlx/api/notifications") {
            return extSrv.URL, "fk-bridge", true
        }
        return "", "", false
    })

    // Ext path → dynamic upstream + dynamic credential.
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/api/search/models?q=glm", nil))
    if w.Code != http.StatusOK || w.Body.String() != "ext" {
        t.Fatalf("ext path code=%d body=%q, want ext upstream", w.Code, w.Body.String())
    }
    // Undeclared path → static upstream + static credential.
    w = httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/index.html", nil))
    if w.Code != http.StatusOK || w.Body.String() != "static" {
        t.Fatalf("normal path code=%d body=%q, want static upstream", w.Code, w.Body.String())
    }
    // Notification sub-path matches at the segment boundary.
    w = httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/api/notifications/engine", nil))
    if w.Code != http.StatusOK || w.Body.String() != "ext" {
        t.Fatalf("notification sub-path code=%d body=%q, want ext upstream", w.Code, w.Body.String())
    }

    if got := len(extPaths.Load().([]string)); got != 2 {
        t.Fatalf("ext upstream hits = %d, want 2", got)
    }
    if got := len(staticPaths.Load().([]string)); got != 1 {
        t.Fatalf("static upstream hits = %d, want 1", got)
    }
    if got := staticPaths.Load().([]string); got[0] != "/index.html" {
        t.Fatalf("static path = %q, want /index.html", got[0])
    }
}

// TestProxy_TransportCachePerUpstream: one module dialing two upstreams
// must not reuse the first upstream's client for the second (#256).
func TestProxy_TransportCachePerUpstream(t *testing.T) {
    staticSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("static"))
    }))
    defer staticSrv.Close()
    extSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("ext"))
    }))
    defer extSrv.Close()

    p := NewProxy(func(string) (string, string) { return staticSrv.URL, "" })
    p.SetExtResolver(func(_, path string) (string, string, bool) {
        if path == "/shell/m/api/x" {
            return extSrv.URL, "", true
        }
        return "", "", false
    })

    // Interleave: normal first (caches static client), then ext (must build
    // a client for the ext upstream, not reuse the static one).
    for i := 0; i < 2; i++ {
        w := httptest.NewRecorder()
        p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/m/index.html", nil))
        if w.Body.String() != "static" {
            t.Fatalf("iter %d: normal path = %q", i, w.Body.String())
        }
        w = httptest.NewRecorder()
        p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/m/api/x", nil))
        if w.Body.String() != "ext" {
            t.Fatalf("iter %d: ext path = %q (stale transport?)", i, w.Body.String())
        }
    }
}

func TestHealthPoller_ExpiresStaleRefusedRegistration(t *testing.T) {
    // #276: dynamic registration whose heartbeat expired AND whose upstream
    // refuses connections must be dropped from the registry — not served as
    // an unreachable menu entry to every fresh session forever.
    m := validManifest("fusion-stale")
    m.Upstream = "http://127.0.0.1:1"
    r := NewRegistry(nil, nil, 20*time.Millisecond)
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    poller := NewHealthPoller(r, 10*time.Millisecond)
    time.Sleep(60 * time.Millisecond) // heartbeat expires
    poller.pollAll(context.Background())
    if views := r.List(); len(views) != 0 {
        t.Fatalf("want stale refused registration dropped, got %+v", views)
    }
}

func TestHealthPoller_KeepsStaleRegistrationWhenUpstreamAlive(t *testing.T) {
    // Heartbeat-dead but upstream still accepting connections: keep the
    // entry (greyed unreachable) — only connection-refusing ones expire.
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()
    m := validManifest("fusion-halfup")
    m.Upstream = upstream.URL
    r := NewRegistry(nil, nil, 20*time.Millisecond)
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    poller := NewHealthPoller(r, 10*time.Millisecond)
    time.Sleep(60 * time.Millisecond)
    poller.pollAll(context.Background())
    views := r.List()
    if len(views) != 1 || views[0].Status != StatusUnreachable {
        t.Fatalf("want 1 unreachable view (kept, not dropped), got %+v", views)
    }
}

func TestHealthPoller_ExpirySkipsBuiltins(t *testing.T) {
    // Builtins (gateway-owned, e.g. fusion-admin) must survive the stale
    // sweep: Remove rejects them, and re-registration only happens at boot.
    m := validManifest("fusion-admin")
    m.Upstream = "http://127.0.0.1:1"
    r := NewRegistry(nil, nil, 20*time.Millisecond)
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    r.MarkBuiltin(m.ID)
    poller := NewHealthPoller(r, 10*time.Millisecond)
    time.Sleep(60 * time.Millisecond)
    poller.pollAll(context.Background())
    views := r.List()
    if len(views) != 1 || views[0].ID != "fusion-admin" {
        t.Fatalf("want builtin kept despite stale heartbeat + refused upstream, got %+v", views)
    }
}
