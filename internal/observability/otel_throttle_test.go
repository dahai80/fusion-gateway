package observability

import (
    "bytes"
    "errors"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strconv"
    "strings"
    "testing"

    "go.opentelemetry.io/otel"
)

// TestThrottledOtelErrorHandler_LogThrottledMetricNot (#283): repeated export
// errors must log once per 5m window per distinct error (not once per batch,
// which flooded the log at ~194/day with no collector on :4317), while
// fusion_gateway_otel_errors_total counts every occurrence.
func TestThrottledOtelErrorHandler_LogThrottledMetricNot(t *testing.T) {
    var buf bytes.Buffer
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
    defer slog.SetDefault(prev)

    before := otelErrorMetricValue(t)

    h := throttledOtelErrorHandler{}
    exportErr := errors.New(`traces export: exporter export timeout: rpc error: code = Unavailable desc = "transport: Error while dialing: dial tcp [::1]:4317: connect: connection refused"`)
    for i := 0; i < 10; i++ {
        h.Handle(exportErr)
    }
    // A second, distinct failure class has its own throttle key.
    for i := 0; i < 10; i++ {
        h.Handle(errors.New("traces export: context deadline exceeded"))
    }

    logged := strings.Count(buf.String(), "otel internal error")
    if logged != 2 {
        t.Fatalf("expected exactly 2 throttled WARN lines (1 per distinct error), got %d", logged)
    }

    if d := otelErrorMetricValue(t) - before; d != 20 {
        t.Fatalf("metric delta = %v, want 20 (metric must NOT be throttled)", d)
    }
}

// TestThrottledOtelErrorHandler_NilError: a nil error is a no-op, not a panic.
func TestThrottledOtelErrorHandler_NilError(t *testing.T) {
    throttledOtelErrorHandler{}.Handle(nil)
}

// TestInitTracing_InstallsThrottledErrorHandler (#283): enabling OTel must
// install the throttled handler so the SDK's per-batch otel.Handle calls are
// rate-limited; a disabled config leaves the global handler untouched.
func TestInitTracing_InstallsThrottledErrorHandler(t *testing.T) {
    prevHandler := otel.GetErrorHandler()
    defer otel.SetErrorHandler(prevHandler)

    shutdown, err := InitTracing(nilContext(), OTelConfig{
        Enabled:  true,
        Endpoint: "localhost:4317",
        Protocol: "grpc",
    })
    if err != nil {
        t.Fatalf("InitTracing: %v", err)
    }
    defer func() { _ = shutdown(nilContext()) }()

    if _, ok := otel.GetErrorHandler().(throttledOtelErrorHandler); !ok {
        t.Fatalf("expected throttledOtelErrorHandler installed, got %T", otel.GetErrorHandler())
    }
}

func otelErrorMetricValue(t *testing.T) float64 {
    t.Helper()
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    for _, line := range strings.Split(rec.Body.String(), "\n") {
        if strings.HasPrefix(line, "fusion_gateway_otel_errors_total ") {
            v, err := strconv.ParseFloat(strings.TrimSpace(line[len("fusion_gateway_otel_errors_total "):]), 64)
            if err != nil {
                t.Fatalf("parse metric value: %v line=%q", err, line)
            }
            return v
        }
    }
    return 0
}
