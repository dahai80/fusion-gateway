package observability

import (
    "context"
    "testing"

    "go.opentelemetry.io/otel"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/trace"
)

func setupRecordingTracer() (func(), trace.Tracer) {
    tp := sdktrace.NewTracerProvider()
    otel.SetTracerProvider(tp)
    tracer = tp.Tracer("github.com/fusion-gateway/fusion-gateway")
    return func() {
        _ = tp.Shutdown(context.Background())
        tracer = otel.GetTracerProvider().Tracer("github.com/fusion-gateway/fusion-gateway")
    }, tracer
}

func TestSetSpanAttributes_RecordingTrue(t *testing.T) {
    cleanup, _ := setupRecordingTracer()
    defer cleanup()

    _, span := StartSpan(nilContext(), "test-recording-attrs")
    defer span.End()
    if !span.IsRecording() {
        t.Fatal("span should be recording with real TracerProvider")
    }
    SetSpanAttributes(span)
}

func TestRecordSpanError_RecordingTrue(t *testing.T) {
    cleanup, _ := setupRecordingTracer()
    defer cleanup()

    _, span := StartSpan(nilContext(), "test-recording-err")
    defer span.End()
    if !span.IsRecording() {
        t.Fatal("span should be recording with real TracerProvider")
    }
    RecordSpanError(span, errTest)
}

func TestSetSpanAttributes_NonRecordingDefault(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-default-nonrecording")
    defer span.End()
    if span.IsRecording() {
        t.Log("default provider creates recording spans, this is unexpected")
    }
    SetSpanAttributes(span)
}

func TestRecordSpanError_NonRecordingDefault(t *testing.T) {
    _, span := StartSpan(nilContext(), "test-default-nonrecording-err")
    defer span.End()
    if span.IsRecording() {
        t.Log("default provider creates recording spans, this is unexpected")
    }
    RecordSpanError(span, errTest)
}
