package observability

import (
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/logthrottle"
)

// otelErrThrottle (#283): with no OTLP collector listening, the SDK logged
// one "traces export: ..." error per export batch (~1 per 30s, 194/day) —
// pure noise that never recovered. The throttled handler logs the first
// occurrence of each distinct error immediately, then at most 1 per 5m,
// while fusion_gateway_otel_errors_total counts every occurrence so the
// failing endpoint stays observable in /metrics.
var otelErrThrottle = logthrottle.New(5 * time.Minute)

// throttledOtelErrorHandler implements otel.ErrorHandler.
type throttledOtelErrorHandler struct{}

func (throttledOtelErrorHandler) Handle(err error) {
    if err == nil {
        return
    }
    RecordOtelError()
    msg := err.Error()
    if otelErrThrottle.Allow(msg) {
        slog.Warn("otel internal error (log throttled to 1 per 5m per distinct error)",
            "error", msg)
    }
}
