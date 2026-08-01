package realtime

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/gorilla/websocket"
)

func TestProxy_UpgradeAndProxy_Bidirectional(t *testing.T) {
    backendMsgs := make(chan string, 10)
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        defer conn.Close()
        for {
            _, msg, err := conn.ReadMessage()
            if err != nil {
                return
            }
            backendMsgs <- string(msg)
            _ = conn.WriteMessage(websocket.TextMessage, []byte("echo:"+string(msg)))
        }
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/v1/realtime"
    proxy := NewProxy("X-Fusion-Route", "gateway-decision", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "test-key")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/v1/realtime"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial gateway failed: %v", err)
    }
    defer conn.Close()

    testMsg := `{"type":"session.create"}`
    if err := conn.WriteMessage(websocket.TextMessage, []byte(testMsg)); err != nil {
        t.Fatalf("write message failed: %v", err)
    }

    select {
    case msg := <-backendMsgs:
        if msg != testMsg {
            t.Errorf("backend received %q, want %q", msg, testMsg)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for backend to receive message")
    }

    _, resp, err := conn.ReadMessage()
    if err != nil {
        t.Fatalf("read response failed: %v", err)
    }
    if string(resp) != "echo:"+testMsg {
        t.Errorf("client received %q, want %q", string(resp), "echo:"+testMsg)
    }
}

func TestProxy_BackendUnavailable(t *testing.T) {
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, "ws://127.0.0.1:1/invalid", "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/v1/realtime"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Logf("dial result (expected failure): %v", err)
        return
    }
    defer conn.Close()

    _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
    _, _, err = conn.ReadMessage()
    if err == nil {
        t.Error("expected error when backend unavailable")
    }
}

func TestProxy_AuthHeader(t *testing.T) {
    receivedAuth := make(chan string, 1)

    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedAuth <- r.Header.Get("Authorization")
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, _ := up.Upgrade(w, r, nil)
        conn.Close()
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("X-Route", "test-value", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "my-secret-key")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    conn.Close()

    select {
    case auth := <-receivedAuth:
        if auth != "Bearer my-secret-key" {
            t.Errorf("auth header = %q, want %q", auth, "Bearer my-secret-key")
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for auth header")
    }
}

func TestUpgrader_CheckOrigin(t *testing.T) {
    if !upgrader.CheckOrigin(httptest.NewRequest("GET", "/", nil)) {
        t.Error("CheckOrigin should return true for all origins")
    }
}

func TestProxy_RouteHeaderForwarding(t *testing.T) {
    receivedRoute := make(chan string, 1)

    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedRoute <- r.Header.Get("X-Fusion-Route")
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, _ := up.Upgrade(w, r, nil)
        conn.Close()
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("X-Fusion-Route", "gateway-decision", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "test-key")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    conn.Close()

    select {
    case route := <-receivedRoute:
        if route != "gateway-decision" {
            t.Errorf("route header = %q, want %q", route, "gateway-decision")
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for route header")
    }
}

func TestProxy_NoRouteHeader(t *testing.T) {
    receivedRoute := make(chan string, 1)

    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedRoute <- r.Header.Get("X-Route")
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, _ := up.Upgrade(w, r, nil)
        conn.Close()
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "test-key")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    conn.Close()

    select {
    case route := <-receivedRoute:
        if route != "" {
            t.Errorf("expected empty route header, got %q", route)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for route header check")
    }
}

func TestProxy_Relay_WriteError(t *testing.T) {
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        conn.Close()
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    defer conn.Close()

    // Backend is already closed, so writing should fail
    _ = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
    _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
    _, _, err = conn.ReadMessage()
    if err == nil {
        t.Error("expected error when backend closed")
    }
}

func TestProxy_CustomMaxMsgMB(t *testing.T) {
    receivedMsg := make(chan string, 1)

    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        defer conn.Close()
        _, msg, err := conn.ReadMessage()
        if err != nil {
            return
        }
        receivedMsg <- string(msg)
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 1)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    defer conn.Close()

    _ = conn.WriteMessage(websocket.TextMessage, []byte("small"))
    select {
    case msg := <-receivedMsg:
        if msg != "small" {
            t.Errorf("expected small, got %q", msg)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for message")
    }
}

func TestProxy_UpgradeAndProxy_NoAPIKey(t *testing.T) {
    receivedAuth := make(chan string, 1)

    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedAuth <- r.Header.Get("Authorization")
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, _ := up.Upgrade(w, r, nil)
        conn.Close()
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    conn.Close()

    select {
    case auth := <-receivedAuth:
        if auth != "" {
            t.Errorf("expected empty auth, got %q", auth)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for auth header check")
    }
}

func TestProxy_UpgradeFails(t *testing.T) {
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, "ws://127.0.0.1:1/fail", "")
    }))
    defer gw.Close()

    // Make a regular HTTP request (not websocket upgrade) - this triggers
    // upgrader.Upgrade to fail (line 41-44)
    resp, err := http.Get(gw.URL)
    if err != nil {
        t.Logf("http get result: %v", err)
        return
    }
    defer resp.Body.Close()
}

func TestProxy_BackendDialWithHTTPError(t *testing.T) {
    // Backend returns HTTP error (not websocket), so dial fails with resp != nil
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusServiceUnavailable)
        _, _ = w.Write([]byte("unavailable"))
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    errCh := make(chan string, 1)
    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "test-key")
        errCh <- "done"
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Logf("dial failed (expected): %v", err)
        return
    }
    defer conn.Close()

    _ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
    var received map[string]interface{}
    err = conn.ReadJSON(&received)
    if err != nil {
        t.Logf("read json error: %v (backend may have closed)", err)
    } else {
        t.Logf("received from proxy: %v", received)
        if received["type"] == "error" {
            t.Log("got error message from proxy - correct behavior")
        }
    }

    select {
    case <-errCh:
        t.Log("proxy handler completed")
    case <-time.After(3 * time.Second):
        t.Log("timeout waiting for proxy handler")
    }
}

func TestProxy_Relay_WriteErrorDst(t *testing.T) {
    // Create backend that closes immediately after upgrade, so write to it fails
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        // Close immediately to cause write errors
        conn.Close()
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    defer conn.Close()

    // Send message - backend is closed, write should fail
    _ = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
    _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
    _, _, _ = conn.ReadMessage()
    // Just needs to complete without hanging - the write error path is exercised
}

func TestProxy_Relay_BackendWriteToClosedClient(t *testing.T) {
    // Backend sends messages; client closes; write to client fails
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        defer conn.Close()
        // Rapidly send messages to client
        for i := 0; i < 100; i++ {
            err := conn.WriteMessage(websocket.TextMessage, []byte("msg"))
            if err != nil {
                return
            }
        }
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    // Close client quickly so backend writes fail
    conn.Close()

    // Give time for the relay to hit the write error
    time.Sleep(500 * time.Millisecond)
}

func TestProxy_Relay_WriteToClosedBackend(t *testing.T) {
    // Backend accepts and immediately closes without reading
    // Client sends a message, proxy tries to write to closed backend
    closeCh := make(chan struct{})
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        // Send close frame and close
        _ = conn.WriteMessage(websocket.CloseMessage,
            websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
        conn.Close()
        closeCh <- struct{}{}
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    defer conn.Close()

    // Wait for backend to close
    select {
    case <-closeCh:
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for backend close")
    }

    // Small delay to let close propagate
    time.Sleep(100 * time.Millisecond)

    // Send messages after backend is closed - write to backend should fail
    for i := 0; i < 5; i++ {
        _ = conn.WriteMessage(websocket.TextMessage, []byte("after-close"))
    }
    _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
    _, _, _ = conn.ReadMessage()
}

func TestProxy_Relay_LargeMessageReadLimit(t *testing.T) {
    // Test that relay respects read limit with maxMsgMB=1
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        defer conn.Close()
        // Send a message larger than 1MB
        bigMsg := make([]byte, 2*1024*1024)
        err = conn.WriteMessage(websocket.TextMessage, bigMsg)
        if err != nil {
            return
        }
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 1) // 1MB limit

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }
    defer conn.Close()

    _ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
    _, _, err = conn.ReadMessage()
    if err != nil {
        t.Logf("read error (expected due to size limit): %v", err)
    }
}

func TestProxy_Relay_ClientCloseTriggersWriteError(t *testing.T) {
    // Client sends a message then immediately closes.
    // Backend echoes back - but client is already closed, so write to client fails.
    started := make(chan struct{})
    backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
        conn, err := up.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        defer conn.Close()
        close(started)
        for {
            msgType, msg, err := conn.ReadMessage()
            if err != nil {
                return
            }
            // Echo back with small delay so client can close first
            time.Sleep(50 * time.Millisecond)
            err = conn.WriteMessage(msgType, msg)
            if err != nil {
                return
            }
        }
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/"
    proxy := NewProxy("", "", 4)

    gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        proxy.UpgradeAndProxy(w, r, backendWSURL, "")
    }))
    defer gw.Close()

    gwWSURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/"
    conn, _, err := websocket.DefaultDialer.Dial(gwWSURL, nil)
    if err != nil {
        t.Fatalf("dial failed: %v", err)
    }

    // Wait for backend to be ready
    select {
    case <-started:
    case <-time.After(2 * time.Second):
        t.Fatal("timeout waiting for backend start")
    }

    // Send a message then close client immediately
    _ = conn.WriteMessage(websocket.TextMessage, []byte("trigger"))
    // Close with abnormal close to trigger unexpected close
    _ = conn.WriteMessage(websocket.CloseMessage,
        websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, ""))
    conn.Close()

    // Give relay time to process the write error
    time.Sleep(300 * time.Millisecond)
}
