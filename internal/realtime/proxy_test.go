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
            conn.WriteMessage(websocket.TextMessage, []byte("echo:"+string(msg)))
        }
    }))
    defer backend.Close()

    backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http") + "/v1/realtime"
    proxy := NewProxy("X-Fusion-Route", "gateway-decision")

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
    proxy := NewProxy("", "")

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

    conn.SetReadDeadline(time.Now().Add(2 * time.Second))
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
    proxy := NewProxy("X-Route", "test-value")

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
