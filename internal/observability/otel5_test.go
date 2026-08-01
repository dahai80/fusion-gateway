package observability

import (
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

func TestInitTracing_HTTPWithRealCollector(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.Copy(io.Discard, r.Body)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    srv.Listener.Addr().String(),
        Protocol:    "http",
        ServiceName: "test-otel-http-real",
    }
    shutdown, err := InitTracing(context.Background(), cfg)
    if err != nil {
        t.Fatalf("InitTracing HTTP should succeed: %v", err)
    }
    if shutdown == nil {
        t.Fatal("expected shutdown function")
    }
    time.Sleep(50 * time.Millisecond)
    if err := shutdown(context.Background()); err != nil {
        t.Logf("shutdown error (acceptable): %v", err)
    }
}

func TestInitTracing_GRPCRealCollector(t *testing.T) {
    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    "localhost:4317",
        Protocol:    "grpc",
        ServiceName: "test-otel-grpc-real",
    }
    shutdown, err := InitTracing(context.Background(), cfg)
    if err != nil {
        t.Fatalf("InitTracing gRPC should succeed: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(context.Background())
    }
}

func TestInitTracing_EmptyEndpointAndProtocol2(t *testing.T) {
    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    "",
        Protocol:    "",
        ServiceName: "test-empty-ep-proto2",
    }
    shutdown, err := InitTracing(context.Background(), cfg)
    if err != nil {
        t.Fatalf("InitTracing with defaults should succeed: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(context.Background())
    }
}

func TestInitTracing_EmptyServiceNameWithCollector2(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, _ = io.Copy(io.Discard, r.Body)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    cfg := OTelConfig{
        Enabled:     true,
        Endpoint:    srv.Listener.Addr().String(),
        Protocol:    "http",
        ServiceName: "",
    }
    shutdown, err := InitTracing(context.Background(), cfg)
    if err != nil {
        t.Fatalf("InitTracing with empty service name should succeed: %v", err)
    }
    if shutdown != nil {
        _ = shutdown(context.Background())
    }
}

func TestInitTracing_DisabledReturnsNoError2(t *testing.T) {
    shutdown, err := InitTracing(context.Background(), OTelConfig{Enabled: false})
    if err != nil {
        t.Fatalf("disabled should not error: %v", err)
    }
    if shutdown == nil {
        t.Fatal("expected shutdown function even when disabled")
    }
    _ = shutdown(context.Background())
}

func TestInitTracing_InvalidProtocolReturnsError2(t *testing.T) {
    _, err := InitTracing(context.Background(), OTelConfig{
        Enabled:  true,
        Protocol: "tcp",
    })
    if err == nil {
        t.Fatal("expected error for unsupported protocol")
    }
}
