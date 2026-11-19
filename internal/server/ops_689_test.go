package server

import (
    "net/http"
    "net/http/httptest"
    "strconv"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/ops"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// fakeOpsSink records emitted events; thread-safe because withMiddleware
// emission runs on the request goroutine (single request per test here).
type fakeOpsSink struct {
    events []ops.Event
}

func (f *fakeOpsSink) Emit(ev ops.Event) bool {
    f.events = append(f.events, ev)
    return true
}

func newTestServer689(t *testing.T) (*Server, *fakeOpsSink) {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    sink := &fakeOpsSink{}
    s := &Server{
        cfg:         cfg,
        pool:        adapter.NewPool(),
        hwCollector: hwCollector,
        router:      router.NewEngine(cfg, hwCollector),
        tokEngine:   tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        startTime:   time.Now(),
        version:     "test-ver",
        activeReqs:  newActiveRequests(),
        opsEmitter:  sink,
    }
    s.middlewareChain = func(next http.Handler) http.Handler { return next }
    return s, sink
}

func Test689_HttpRequestEventEmitted(t *testing.T) {
    s, sink := newTestServer689(t)
    handler := s.withMiddleware(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    handler(httptest.NewRecorder(), req)
    if len(sink.events) != 1 {
        t.Fatalf("expected 1 http_request event, got %d", len(sink.events))
    }
    ev := sink.events[0]
    if ev.EventType != "http_request" || ev.Status != "200" || ev.Source != "gateway" {
        t.Fatalf("unexpected event: %+v", ev)
    }
    if ev.Payload["route"] != "/v1/models" || ev.Payload["method"] != "GET" {
        t.Fatalf("unexpected payload: %+v", ev.Payload)
    }
    if ev.LatencyMS < 0 {
        t.Fatalf("latency must be non-negative, got %d", ev.LatencyMS)
    }
}

func Test689_ErrorEventOn5xxAnd429(t *testing.T) {
    for _, status := range []int{500, 502, 429} {
        s, sink := newTestServer689(t)
        handler := s.withMiddleware(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(status)
        })
        req := httptest.NewRequest(http.MethodPost, "/v1/agent/tasks/8f14e45f-ea3b-4a2b-9c1d-5e6f7a8b9c0d/cancel", nil)
        handler(httptest.NewRecorder(), req)
        if len(sink.events) != 2 {
            t.Fatalf("status %d: expected http_request + http_error, got %d events", status, len(sink.events))
        }
        reqEv, errEv := sink.events[0], sink.events[1]
        if errEv.EventType != "http_error" || errEv.Status != strconv.Itoa(status) {
            t.Fatalf("status %d: second event wrong: %+v", status, errEv)
        }
        wantRoute := "/v1/agent/tasks/{id}/cancel"
        if reqEv.Payload["route"] != wantRoute {
            t.Fatalf("route template must collapse ids, got %v", reqEv.Payload["route"])
        }
        sig, _ := errEv.Payload["signature"].(string)
        if sig != strconv.Itoa(status)+" "+wantRoute {
            t.Fatalf("signature must be status+route, got %q", sig)
        }
    }
}

func Test689_NoErrorEventOn4xx(t *testing.T) {
    for _, status := range []int{400, 401, 404} {
        s, sink := newTestServer689(t)
        handler := s.withMiddleware(func(w http.ResponseWriter, r *http.Request) {
            w.WriteHeader(status)
        })
        handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
        if len(sink.events) != 1 || sink.events[0].EventType != "http_request" {
            t.Fatalf("status %d: only http_request expected, got %+v", status, sink.events)
        }
    }
}

func Test689_NilEmitterNoOp(t *testing.T) {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    s := &Server{
        cfg:         cfg,
        pool:        adapter.NewPool(),
        hwCollector: hwCollector,
        router:      router.NewEngine(cfg, hwCollector),
        tokEngine:   tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        startTime:   time.Now(),
        version:     "test-ver",
        activeReqs:  newActiveRequests(),
    }
    s.middlewareChain = func(next http.Handler) http.Handler { return next }
    handler := s.withMiddleware(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
}
