package observability

import (
    "context"
    "testing"
)

func TestInitTracing_Disabled(t *testing.T) {
    shutdown, err := InitTracing(context.Background(), OTelConfig{Enabled: false})
    if err != nil {
        t.Fatal(err)
    }
    if shutdown == nil {
        t.Error("expected shutdown function")
    }
    shutdown(context.Background())
}

func TestInitTracing_InvalidProtocol(t *testing.T) {
    _, err := InitTracing(context.Background(), OTelConfig{
        Enabled:  true,
        Endpoint: "localhost:4317",
        Protocol: "invalid",
    })
    if err == nil {
        t.Error("expected error for invalid protocol")
    }
}

func TestTracer_NotNil(t *testing.T) {
    tr := Tracer()
    if tr == nil {
        t.Error("expected non-nil tracer")
    }
}

func TestStartSpan(t *testing.T) {
    ctx, span := StartSpan(context.Background(), "test-span")
    if span == nil {
        t.Error("expected non-nil span")
    }
    span.End()
    if ctx == nil {
        t.Error("expected non-nil context")
    }
}

func TestSetSpanAttributes_NoPanic(t *testing.T) {
    _, span := StartSpan(context.Background(), "test")
    SetSpanAttributes(span)
    span.End()
}

func TestRecordSpanError_NoPanic(t *testing.T) {
    _, span := StartSpan(context.Background(), "test")
    RecordSpanError(span, nil)
    span.End()
}
