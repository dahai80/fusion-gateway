package observability

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
    "go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer

// N6 (audit): instrumentation/service version was hardcoded to "0.4.0" at
// three sites (init, resource, tracer). Read the build-time version injected
// by R4 (main.version via ldflags) instead, so OTel exports report the real
// running binary version, not a stale literal. Default "dev" matches a plain
// `go build` (no ldflags). SetVersion is called from main before server.New
// (which calls InitTracing) so the tracer + resource pick it up.
var version = "dev"

// SetVersion stamps the build-time version onto the OTel package so the tracer
// instrumentation version and the resource service version report the real
// binary, not the "dev" default. Called once from main at startup.
func SetVersion(v string) {
    if v == "" {
        return
    }
    version = v
    slog.Info("otel version set", "version", v)
}

func init() {
    tracer = otel.GetTracerProvider().Tracer(
        "github.com/fusion-gateway/fusion-gateway",
        trace.WithInstrumentationVersion(version),
    )
}

type OTelConfig struct {
    Enabled     bool   `mapstructure:"enabled"`
    Endpoint    string `mapstructure:"endpoint"`
    Protocol    string `mapstructure:"protocol"`
    ServiceName string `mapstructure:"service_name"`
}

func InitTracing(ctx context.Context, cfg OTelConfig) (func(context.Context) error, error) {
    if !cfg.Enabled {
        slog.Info("otel tracing disabled")
        return func(ctx context.Context) error { return nil }, nil
    }

    serviceName := cfg.ServiceName
    if serviceName == "" {
        serviceName = "fusion-gateway"
    }

    res, err := resource.Merge(
        resource.Default(),
        resource.NewWithAttributes(
            semconv.SchemaURL,
            semconv.ServiceNameKey.String(serviceName),
            semconv.ServiceVersionKey.String(version),
        ),
    )
    if err != nil {
        slog.Warn("otel resource merge conflict, using merged resource anyway", "error", err)
    }
    if res == nil {
        res = resource.Default()
    }

    var exporter sdktrace.SpanExporter
    protocol := cfg.Protocol
    if protocol == "" {
        protocol = "grpc"
    }
    endpoint := cfg.Endpoint
    if endpoint == "" {
        endpoint = "localhost:4317"
    }

    switch protocol {
    case "grpc":
        slog.Info("otel exporting via grpc", "endpoint", endpoint)
        exporter, err = otlptracegrpc.New(ctx,
            otlptracegrpc.WithEndpoint(endpoint),
            otlptracegrpc.WithInsecure(),
        )
    case "http":
        slog.Info("otel exporting via http", "endpoint", endpoint)
        exporter, err = otlptracehttp.New(ctx,
            otlptracehttp.WithEndpoint(endpoint),
            otlptracehttp.WithInsecure(),
        )
    default:
        return nil, fmt.Errorf("unsupported otel protocol: %s", protocol)
    }
    if err != nil {
        return nil, fmt.Errorf("otel exporter init: %w", err)
    }

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithResource(res),
        sdktrace.WithBatcher(exporter,
            sdktrace.WithBatchTimeout(5*time.Second),
        ),
        sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1))),
    )

    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{},
        propagation.Baggage{},
    ))
    tracer = tp.Tracer(
        "github.com/fusion-gateway/fusion-gateway",
        trace.WithInstrumentationVersion(version),
    )

    slog.Info("otel tracing initialized", "endpoint", endpoint, "protocol", protocol)
    return tp.Shutdown, nil
}

func Tracer() trace.Tracer {
    return tracer
}

func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
    return tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

func SetSpanAttributes(span trace.Span, attrs ...attribute.KeyValue) {
    if span.IsRecording() {
        span.SetAttributes(attrs...)
    }
}

func RecordSpanError(span trace.Span, err error) {
    if span.IsRecording() {
        span.RecordError(err)
    }
}

func HTTPMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
        spanName := fmt.Sprintf("HTTP %s %s", r.Method, r.URL.Path)
        ctx, span := tracer.Start(ctx, spanName,
            trace.WithAttributes(
                semconv.HTTPRequestMethodKey.String(r.Method),
                semconv.URLPathKey.String(r.URL.Path),
                semconv.UserAgentOriginalKey.String(r.UserAgent()),
            ),
        )
        defer span.End()

        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
