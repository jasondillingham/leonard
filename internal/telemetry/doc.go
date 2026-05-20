// Package telemetry exposes a tiny build-tag-gated tracing surface.
//
// Default builds (no `-tags otel`) compile in zero-overhead no-op stubs;
// `go build -tags otel ./...` swaps in real OpenTelemetry spans. The
// hot-path call sites in internal/index, internal/hooks, and internal/
// mcp all look identical regardless of build mode:
//
//	ctx, end := telemetry.Span(ctx, "leonard.pre-edit")
//	defer end()
//
// Why a build tag rather than always-linked: keeps the default binary
// CGo-free and small, preserves the project's pure-Go install story,
// and avoids dragging the OpenTelemetry SDK's transitive dependencies
// into projects that don't want them. Operators who care about
// timing measure with the tagged build; everyone else pays nothing.
//
// Init reads the standard OTel environment variables (OTEL_TRACES_
// EXPORTER, OTEL_EXPORTER_OTLP_ENDPOINT, etc.) so users with existing
// OTel toolchains drop right in.
package telemetry

import "context"

// Shutdown is the cleanup function Init returns. Call once at program
// exit (typically `defer shutdown(ctx)`); the no-op build returns a
// function that does nothing, the otel build flushes pending spans.
type Shutdown func(context.Context) error

// EndSpan is the cleanup function Span returns. Always call exactly
// once per Span invocation, typically via `defer end()`.
type EndSpan func()
