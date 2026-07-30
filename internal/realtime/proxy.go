package realtime

import (
    "context"
    "log/slog"
    "net/http"
    "sync"
    "time"

    "github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
    ReadBufferSize:  4096,
    WriteBufferSize: 4096,
    CheckOrigin: func(r *http.Request) bool {
        return true
    },
}

type Proxy struct {
    dialer          *websocket.Dialer
    routeHeader     string
    routeHeaderValue string
}

func NewProxy(routeHeader, routeHeaderValue string) *Proxy {
    return &Proxy{
        dialer: &websocket.Dialer{
            HandshakeTimeout: 10 * time.Second,
        },
        routeHeader:      routeHeader,
        routeHeaderValue: routeHeaderValue,
    }
}

func (p *Proxy) UpgradeAndProxy(w http.ResponseWriter, r *http.Request, backendURL string, apiKey string) {
    clientConn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        slog.Error("realtime: websocket upgrade failed", "error", err)
        return
    }
    defer clientConn.Close()

    requestHeader := http.Header{}
    if apiKey != "" {
        requestHeader.Set("Authorization", "Bearer "+apiKey)
    }
    if p.routeHeader != "" && p.routeHeaderValue != "" {
        requestHeader.Set(p.routeHeader, p.routeHeaderValue)
    }

    for key, vals := range r.Header {
        switch key {
        case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version",
            "Sec-Websocket-Extensions", "Sec-Websocket-Protocol":
        default:
            for _, v := range vals {
                requestHeader.Add(key, v)
            }
        }
    }

    backendConn, resp, err := p.dialer.DialContext(r.Context(), backendURL, requestHeader)
    if err != nil {
        slog.Error("realtime: backend dial failed", "backend_url", backendURL, "error", err)
        if resp != nil {
            errMsg := map[string]interface{}{
                "type":  "error",
                "error": map[string]string{"message": "backend connection failed"},
            }
            clientConn.WriteJSON(errMsg)
        }
        return
    }
    defer backendConn.Close()

    slog.Info("realtime: proxy established",
        "client", r.RemoteAddr,
        "backend", backendURL,
    )

    ctx, cancel := context.WithCancel(r.Context())
    defer cancel()

    var once sync.Once
    closeBoth := func() {
        once.Do(func() {
            cancel()
            clientConn.Close()
            backendConn.Close()
        })
    }

    go p.relay(ctx, "client_to_backend", clientConn, backendConn, closeBoth)
    p.relay(ctx, "backend_to_client", backendConn, clientConn, closeBoth)
}

func (p *Proxy) relay(ctx context.Context, direction string, src *websocket.Conn, dst *websocket.Conn, onClose func()) {
    defer onClose()

    src.SetReadLimit(1 << 20)

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        msgType, data, err := src.ReadMessage()
        if err != nil {
            if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
                slog.Warn("realtime: read error",
                    "direction", direction,
                    "error", err,
                )
            }
            return
        }

        if err := dst.WriteMessage(msgType, data); err != nil {
            slog.Warn("realtime: write error",
                "direction", direction,
                "error", err,
            )
            return
        }
    }
}
