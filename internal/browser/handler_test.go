package browser

import (
    "bytes"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// passthrough is a WrapFunc that does no auth — the browser handler is tested
// in isolation; the real auth wrap is exercised by the server-level tests.
func passthrough(h http.HandlerFunc) http.HandlerFunc { return h }

func newTestHandler(t *testing.T, fn *fakeNode) (*Handler, *Registry) {
    t.Helper()
    reg, err := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second,
        []config.BrowserNodeConfig{{ID: "n1", SocketPath: fn.socket, Token: "test-token"}})
    if err != nil {
        t.Fatalf("NewRegistry: %v", err)
    }
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 0, 200))
    return NewHandler(proxy), reg
}

func newMux(h *Handler) *http.ServeMux {
    mux := http.NewServeMux()
    h.RegisterRoutes(mux, passthrough, passthrough)
    return mux
}

func doRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
    t.Helper()
    var r io.Reader
    if body != nil {
        b, err := json.Marshal(body)
        if err != nil {
            t.Fatalf("marshal body: %v", err)
        }
        r = bytes.NewReader(b)
    }
    req := httptest.NewRequest(method, path, r)
    if body != nil {
        req.Header.Set("Content-Type", "application/json")
    }
    rr := httptest.NewRecorder()
    mux.ServeHTTP(rr, req)
    return rr
}

func TestHandlerCreateReturns201(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-7", true) },
    })
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    rr := doRequest(t, mux, "POST", "/v1/browser/sessions", CreateSessionRequest{Mode: WebModeHeadless})
    if rr.Code != 201 {
        t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
    }
    var res CreateResult
    if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if res.SessionID != "sess-7" || res.NodeID != "n1" || !res.CredentialInjected {
        t.Fatalf("CreateResult wrong: %+v", res)
    }
}

func TestHandlerCreateMalformedBody400(t *testing.T) {
    fn := newFakeNode(t, nil)
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    req := httptest.NewRequest("POST", "/v1/browser/sessions", bytes.NewReader([]byte("{not json")))
    rr := httptest.NewRecorder()
    mux.ServeHTTP(rr, req)
    if rr.Code != 400 {
        t.Fatalf("expected 400 for malformed body, got %d", rr.Code)
    }
    if !bytes.Contains(rr.Body.Bytes(), []byte(`"code":"invalid_request"`)) {
        t.Fatalf("expected invalid_request code, got %s", rr.Body.String())
    }
}

func TestHandlerCreateEmptyModeDefaultsHeadless(t *testing.T) {
    var seenMode string
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame {
            var cr CreateSessionRequest
            _ = json.Unmarshal(req.Payload, &cr)
            seenMode = string(cr.Mode)
            return createResp("sess-1", false)
        },
    })
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    // Empty mode → handler defaults to headless before forwarding.
    rr := doRequest(t, mux, "POST", "/v1/browser/sessions", map[string]any{})
    if rr.Code != 201 {
        t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
    }
    if seenMode != "headless" {
        t.Fatalf("expected mode defaulted to headless, got %q", seenMode)
    }
}

func TestHandlerExecuteReturns200Verbatim(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-1", false) },
        reqTypeExecute:       func(req RequestFrame) ResponseFrame { return stateResp(map[string]any{"session_id": "sess-1", "url": "https://x.test"}) },
    })
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    if doRequest(t, mux, "POST", "/v1/browser/sessions", CreateSessionRequest{Mode: WebModeHeadless}).Code != 201 {
        t.Fatal("create precondition failed")
    }
    rr := doRequest(t, mux, "POST", "/v1/browser/sessions/sess-1/actions", BrowserActionRequest{Action: ActionClick})
    if rr.Code != 200 {
        t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
    }
    if !bytes.Contains(rr.Body.Bytes(), []byte(`"url":"https://x.test"`)) {
        t.Fatalf("state not relayed verbatim: %s", rr.Body.String())
    }
}

func TestHandlerExecutePathIDOverridesBody(t *testing.T) {
    var seenSessionID string
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-1", false) },
        reqTypeExecute: func(req RequestFrame) ResponseFrame {
            // session_id rides inside the execute payload (BrowserActionRequest),
            // not the envelope (envelope SessionID is the close op only).
            var ar BrowserActionRequest
            _ = json.Unmarshal(req.Payload, &ar)
            seenSessionID = ar.SessionID
            return stateResp(map[string]any{"session_id": "sess-1"})
        },
    })
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    doRequest(t, mux, "POST", "/v1/browser/sessions", CreateSessionRequest{Mode: WebModeHeadless})
    // Body carries a DIFFERENT session_id; path param is authoritative.
    rr := doRequest(t, mux, "POST", "/v1/browser/sessions/sess-1/actions",
        BrowserActionRequest{SessionID: "WRONG", Action: ActionClick})
    if rr.Code != 200 {
        t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
    }
    if seenSessionID != "sess-1" {
        t.Fatalf("path id should override body, forwarded session_id=%q", seenSessionID)
    }
}

func TestHandlerExecutePinMiss404(t *testing.T) {
    fn := newFakeNode(t, nil)
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    rr := doRequest(t, mux, "POST", "/v1/browser/sessions/ghost/actions", BrowserActionRequest{Action: ActionClick})
    if rr.Code != 404 {
        t.Fatalf("expected 404 pin miss, got %d: %s", rr.Code, rr.Body.String())
    }
    if !bytes.Contains(rr.Body.Bytes(), []byte(`"code":"session_not_found"`)) {
        t.Fatalf("expected session_not_found code, got %s", rr.Body.String())
    }
}

func TestHandlerCloseReturns204(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeCreateSession: func(req RequestFrame) ResponseFrame { return createResp("sess-1", false) },
        reqTypeClose:         func(req RequestFrame) ResponseFrame { return ResponseFrame{Type: respTypeClosed} },
    })
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    doRequest(t, mux, "POST", "/v1/browser/sessions", CreateSessionRequest{Mode: WebModeHeadless})
    rr := doRequest(t, mux, "DELETE", "/v1/browser/sessions/sess-1", nil)
    if rr.Code != 204 {
        t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
    }
}

func TestHandlerCloseIdempotentNoPin204(t *testing.T) {
    fn := newFakeNode(t, nil)
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    rr := doRequest(t, mux, "DELETE", "/v1/browser/sessions/ghost", nil)
    if rr.Code != 204 {
        t.Fatalf("expected 204 for idempotent close of unknown, got %d", rr.Code)
    }
}

func TestHandlerNodesReturnsMap(t *testing.T) {
    fn := newFakeNode(t, nil)
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    rr := doRequest(t, mux, "GET", "/v1/browser/nodes", nil)
    if rr.Code != 200 {
        t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
    }
    var out struct {
        Nodes []nodeMapEntry `json:"nodes"`
        Count int            `json:"count"`
    }
    if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if out.Count != 1 || len(out.Nodes) != 1 {
        t.Fatalf("expected 1 node, got count=%d nodes=%d", out.Count, len(out.Nodes))
    }
    if out.Nodes[0].ID != "n1" || out.Nodes[0].SocketPath == "" {
        t.Fatalf("node map entry wrong: %+v", out.Nodes[0])
    }
    if out.Nodes[0].State != "live" {
        t.Fatalf("expected state live, got %s", out.Nodes[0].State)
    }
}

func TestHandlerMetricsReturns200(t *testing.T) {
    fn := newFakeNode(t, map[string]func(req RequestFrame) ResponseFrame{
        reqTypeMetrics: func(req RequestFrame) ResponseFrame { return metricsResp(map[string]any{"counters": 1}) },
    })
    h, _ := newTestHandler(t, fn)
    mux := newMux(h)

    rr := doRequest(t, mux, "GET", "/v1/browser/metrics", nil)
    if rr.Code != 200 {
        t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
    }
    if !bytes.Contains(rr.Body.Bytes(), []byte(`"counters":1`)) {
        t.Fatalf("metrics not relayed: %s", rr.Body.String())
    }
}

func TestHandlerRegisterRoutesNilProxyNoOp(t *testing.T) {
    // A handler with a nil proxy must not panic and must register no routes.
    h := NewHandler(nil)
    mux := http.NewServeMux()
    h.RegisterRoutes(mux, passthrough, passthrough) // no panic
    // Any browser path should 404 (route never registered).
    rr := doRequest(t, mux, "GET", "/v1/browser/nodes", nil)
    if rr.Code != 404 {
        t.Fatalf("nil-proxy handler should register nothing, got %d", rr.Code)
    }
}

func TestHandlerCreateQuotaExceededMaps503(t *testing.T) {
    // Global quota exceeded → 503 quota_exceeded (not retryable).
    reg, _ := NewRegistry(dialClient(), 5*time.Second, 3, 30*time.Second, nil)
    seedNode(t, reg, "n1", "/tmp/n1.sock")
    setCap(t, reg, "n1", &FBNodeCapacity{NodeID: "n1", MaxSessions: 4, LiveSessions: 3, FreeMemoryMB: 8000})
    proxy := NewProxy(reg, dialClient(), NewScheduler(reg, 3, 200))
    h := NewHandler(proxy)
    mux := newMux(h)

    rr := doRequest(t, mux, "POST", "/v1/browser/sessions", CreateSessionRequest{Mode: WebModeHeadless})
    if rr.Code != 503 {
        t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
    }
    if !bytes.Contains(rr.Body.Bytes(), []byte(`"code":"quota_exceeded"`)) {
        t.Fatalf("expected quota_exceeded code, got %s", rr.Body.String())
    }
}
