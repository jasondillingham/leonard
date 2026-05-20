//go:build otel

package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// tracerName is the instrumentation-scope name attached to every span
// Leonard produces. Conventional OTel naming uses the import path; we
// shorten to "leonard" since this package is the single source of spans.
const tracerName = "leonard"

// Init wires up an OpenTelemetry tracer provider based on the standard
// OTEL_* env vars. Supported exporters (OTEL_TRACES_EXPORTER):
//
//   - otlp (default if OTEL_EXPORTER_OTLP_ENDPOINT is set)
//   - stdout (writes one JSON span per line to stderr — local debug)
//   - none / "" / unset → tracing disabled, no exporter wired up
//
// Returns a Shutdown func that flushes pending spans; defer it from
// main(). An error from Init is non-fatal at the call site — Leonard's
// core function doesn't depend on telemetry.
func Init(ctx context.Context) (Shutdown, error) {
	exporterName := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")))
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))

	// Choose exporter. Empty selection means "no tracing"; we still
	// install a no-op provider so call sites don't panic if they
	// somehow get a non-default tracer.
	var exporter sdktrace.SpanExporter
	var err error
	switch exporterName {
	case "stdout":
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
	case "otlp", "":
		if exporterName == "" && endpoint == "" {
			// Nothing configured — silent no-op (still installs the
			// global tracer so attribute lookups don't crash).
			return noopShutdown, nil
		}
		exporter, err = otlptracehttp.New(ctx)
	case "none":
		return noopShutdown, nil
	default:
		return noopShutdown, fmt.Errorf("telemetry: unknown OTEL_TRACES_EXPORTER %q", exporterName)
	}
	if err != nil {
		return noopShutdown, fmt.Errorf("telemetry: build exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("leonard"),
		),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("telemetry: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Span starts a child span under the active tracer. The returned
// EndSpan must be called exactly once — `defer end()` at the call
// site is the idiomatic shape.
func Span(ctx context.Context, name string) (context.Context, EndSpan) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, name)
	return ctx, func() { span.End() }
}

// Enabled reports whether the otel build tag was used. Mostly for
// tests that assert real tracing is wired in.
func Enabled() bool { return true }

func noopShutdown(_ context.Context) error { return nil }
