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

func init() {
    tracer = otel.GetTracerProvider().Tracer(
        "github.com/fusion-gateway/fusion-gateway",
        trace.WithInstrumentationVersion("0.4.0"),
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
            semconv.ServiceVersionKey.String("0.4.0"),
        ),
    )
    if err != nil {
        return nil, fmt.Errorf("otel resource merge: %w", err)
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
        trace.WithInstrumentationVersion("0.4.0"),
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
