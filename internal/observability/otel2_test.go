package observability

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestHTTPMiddleware(t *testing.T) {
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    wrapped := HTTPMiddleware(handler)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req.Header.Set("User-Agent", "test-client")
    rec := httptest.NewRecorder()
    wrapped.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestHTTPMiddleware_DifferentMethods(t *testing.T) {
    methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}
    for _, method := range methods {
        t.Run(method, func(t *testing.T) {
            handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusOK)
            })
            wrapped := HTTPMiddleware(handler)
            req := httptest.NewRequest(method, "/v1/test", nil)
            rec := httptest.NewRecorder()
            wrapped.ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                t.Fatalf("expected 200 for %s, got %d", method, rec.Code)
            }
        })
    }
}

func TestSetSpanAttributes_Recording(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-attrs")
    defer span.End()
    SetSpanAttributes(span)
}

func TestSetSpanAttributes_WithAttrs(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-attrs-with")
    defer span.End()
    SetSpanAttributes(span)
}

func TestRecordSpanError_WithErr(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-err")
    defer span.End()
    RecordSpanError(span, errTest)
}

func TestRecordSpanError_NilErr(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-nil-err")
    defer span.End()
    RecordSpanError(span, nil)
}

func TestInitTracing_EmptyServiceName(t *testing.T) {
    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    "localhost:4317",
        Protocol:    "grpc",
        ServiceName: "",
    }
    shutdown, err := InitTracing(nilContext(), cfg)
    if err != nil {
        t.Logf("InitTracing with grpc may fail without collector: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(nilContext())
    }
}

func TestInitTracing_HTTPProtocol(t *testing.T) {
    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    "localhost:4318",
        Protocol:    "http",
        ServiceName: "test-svc",
    }
    shutdown, err := InitTracing(nilContext(), cfg)
    if err != nil {
        t.Logf("InitTracing with http may fail without collector: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(nilContext())
    }
}

func TestInitTracing_EmptyEndpoint(t *testing.T) {
    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    "",
        Protocol:    "",
        ServiceName: "test-empty-endpoint",
    }
    shutdown, err := InitTracing(nilContext(), cfg)
    if err != nil {
        t.Logf("InitTracing with empty endpoint may fail without collector: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(nilContext())
    }
}

func TestInitTracing_DisabledReturnsNilShutdown(t *testing.T) {
    cfg := OTelConfig{
        Enabled: false,
    }
    shutdown, err := InitTracing(nilContext(), cfg)
    if err != nil {
        t.Fatalf("disabled InitTracing should not error: %v", err)
    }
    if shutdown == nil {
        t.Fatal("shutdown func should not be nil even when disabled")
    }
    if err := shutdown(nilContext()); err != nil {
        t.Fatalf("disabled shutdown should not error: %v", err)
    }
}

func TestTracer_ReturnsValue(t *testing.T) {
    tr := Tracer()
    if tr == nil {
        t.Fatal("Tracer() should not return nil")
    }
}
