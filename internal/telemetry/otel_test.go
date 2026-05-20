//go:build otel

package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestSpan_RecordsWhenTaggedBuild verifies the otel build tag really
// wires Span() to OpenTelemetry. The no-op build (default) has no
// equivalent test because there's nothing to observe — `Enabled()
// == false` is the contract.
func TestSpan_RecordsWhenTaggedBuild(t *testing.T) {
	if !Enabled() {
		t.Fatal("Enabled() returned false in an otel-tagged build")
	}
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exporter))
	prior := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prior)
		_ = tp.Shutdown(context.Background())
	})

	ctx, end := Span(context.Background(), "leonard.test-span")
	end()
	_ = ctx

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "leonard.test-span" {
		t.Errorf("span name = %q, want leonard.test-span", spans[0].Name)
	}
}

// TestInit_NoneExporter confirms `OTEL_TRACES_EXPORTER=none` returns
// a usable shutdown without standing up the OTLP HTTP exporter (which
// would try to reach a non-existent collector and slow the test).
func TestInit_NoneExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// TestInit_StdoutExporter exercises the local-debug path — writes to
// stderr, no network. We can't easily assert on the written bytes from
// here, but a successful Init + shutdown is enough to lock in that
// the wiring compiles and runs.
func TestInit_StdoutExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "stdout")
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
