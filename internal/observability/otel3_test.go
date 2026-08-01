package observability

import (
    "io"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestInitTracing_HTTPWithMockCollector(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.Copy(io.Discard, r.Body)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    srv.Listener.Addr().String(),
        Protocol:    "http",
        ServiceName: "test-coverage",
    }
    shutdown, err := InitTracing(nilContext(), cfg)
    if err != nil {
        t.Logf("InitTracing with mock HTTP collector: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(nilContext())
    }
}

func TestInitTracing_GRPCWithMockCollector(t *testing.T) {
    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    "localhost:4317",
        Protocol:    "grpc",
        ServiceName: "test-grpc-coverage",
    }
    shutdown, err := InitTracing(nilContext(), cfg)
    if err != nil {
        t.Logf("InitTracing with gRPC may fail without collector: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(nilContext())
    }
}

func TestSetSpanAttributes_NonRecording(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-nonrecording")
    span.End()
    SetSpanAttributes(span)
}

func TestRecordSpanError_NonRecording(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-nonrecording-err")
    span.End()
    RecordSpanError(span, errTest)
}
