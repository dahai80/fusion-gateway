package adapter

// B2 / #129 Gap 2 test: InjectFusionHeaders must propagate the W3C
// traceparent onto outbound upstream requests so the distributed trace chain
// survives the gateway→fusion-mlx / gateway→cloud hop. The handler ctx carries
// the span started by observability.HTTPMiddleware; Inject writes traceparent +
// baggage from it. No span on ctx → no traceparent (no-op), but fusion headers
// still pass.

import (
    "context"
    "net/http/httptest"
    "testing"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/propagation"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newRecordingTracer(t *testing.T) context.Context {
    t.Helper()
    tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
    t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},
        propagation.Baggage{},
    ))
    ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
    t.Cleanup(func() { span.End() })
    return ctx
}

// TestB2_InjectFusionHeaders_WithSpanEmitsTraceparent: a ctx carrying a
// recording span → InjectFusionHeaders writes the traceparent header onto the
// outbound request, AND the X-Request-ID passthrough header still lands.
func TestB2_InjectFusionHeaders_WithSpanEmitsTraceparent(t *testing.T) {
    ctx := newRecordingTracer(t)
    inbound := httptest.NewRequest("POST", "/v1/chat/completions", nil)
    inbound.Header.Set("X-Request-ID", "req-abc-123")
    ctx = WithFusionHeaders(ctx, inbound)

    outReq := httptest.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(ctx, outReq)

    if got := outReq.Header.Get("X-Request-ID"); got != "req-abc-123" {
        t.Fatalf("B2: X-Request-ID passthrough broken, got %q want req-abc-123", got)
    }
    tp := outReq.Header.Get("Traceparent")
    if tp == "" {
        t.Fatal("B2: Traceparent NOT injected onto upstream request — trace chain breaks at gateway hop")
    }
}

// TestB2_InjectFusionHeaders_NoSpanIsNoOp: a bare ctx with no span (e.g. OTel
// disabled, or a decoupled ctx that lost the span) → no traceparent written,
// but fusion passthrough headers still propagate (X-Request-ID correlation
// must not depend on tracing being enabled).
func TestB2_InjectFusionHeaders_NoSpanIsNoOp(t *testing.T) {
    otel.SetTracerProvider(sdktrace.NewTracerProvider())
    otel.SetTextMapPropagator(propagation.TraceContext{})

    inbound := httptest.NewRequest("POST", "/v1/chat/completions", nil)
    inbound.Header.Set("X-Request-ID", "req-xyz-789")
    ctx := WithFusionHeaders(context.Background(), inbound)

    outReq := httptest.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(ctx, outReq)

    if got := outReq.Header.Get("X-Request-ID"); got != "req-xyz-789" {
        t.Fatalf("B2: no-span path dropped X-Request-ID, got %q want req-xyz-789", got)
    }
    if outReq.Header.Get("Traceparent") != "" {
        t.Fatalf("B2: Traceparent written on no-span path (should be no-op), got %q", outReq.Header.Get("Traceparent"))
    }
}

// TestB2_InjectFusionHeaders_NoHeadersNoSpan: nothing on ctx → no panic, no
// headers. Guards the early-return + IsRecording guard under an empty ctx.
func TestB2_InjectFusionHeaders_NoHeadersNoSpan(t *testing.T) {
    outReq := httptest.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
    InjectFusionHeaders(context.Background(), outReq)
    if outReq.Header.Get("Traceparent") != "" {
        t.Fatalf("B2: empty ctx wrote Traceparent, got %q", outReq.Header.Get("Traceparent"))
    }
    if outReq.Header.Get("X-Request-ID") != "" {
        t.Fatalf("B2: empty ctx wrote X-Request-ID, got %q", outReq.Header.Get("X-Request-ID"))
    }
}
