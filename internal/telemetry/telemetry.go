package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Config controls OpenTelemetry bootstrap.
type Config struct {
	ServiceName    string
	// Exporter is "none", "otlp", or "stdout".
	Exporter string
	// OTLPEndpoint is host:port or full URL for the OTLP HTTP collector (traces path added by SDK).
	OTLPEndpoint string
}

// Setup installs the global TracerProvider and TextContextPropagator.
// Returns a shutdown func that flushes spans; always call it on process exit.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	service := cfg.ServiceName
	if service == "" {
		service = "chargepoints-api"
	}
	exporter := strings.ToLower(strings.TrimSpace(cfg.Exporter))
	if exporter == "" {
		if cfg.OTLPEndpoint != "" {
			exporter = "otlp"
		} else {
			exporter = "none"
		}
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(service),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	var tp *sdktrace.TracerProvider
	switch exporter {
	case "none", "noop":
		tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res))
		slog.Info("otel tracing disabled", "exporter", "none")
	case "stdout":
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("otel stdout exporter: %w", err)
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)
		slog.Info("otel tracing enabled", "exporter", "stdout")
	case "otlp":
		opts := []otlptracehttp.Option{}
		if cfg.OTLPEndpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpoint(cfg.OTLPEndpoint), otlptracehttp.WithInsecure())
		}
		exp, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otel otlp exporter: %w", err)
		}
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)
		slog.Info("otel tracing enabled", "exporter", "otlp", "endpoint", cfg.OTLPEndpoint)
	default:
		return nil, fmt.Errorf("unknown OTEL_TRACES_EXPORTER %q (want none|stdout|otlp)", cfg.Exporter)
	}

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx)
	}
	return shutdown, nil
}
