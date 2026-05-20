//go:build !otel

package telemetry

import "context"

// Init is the no-op fallback. The OTel dependency tree is not pulled
// in; this function returns a no-op shutdown and never errors.
// Build with `-tags otel` to enable real tracing.
func Init(_ context.Context) (Shutdown, error) {
	return noopShutdown, nil
}

// Span returns the context unchanged and a no-op end function. Cost
// at the call site is one indirect call and one tuple return — well
// under what the Go compiler will inline at -O1+, but even unoptimized
// it's negligible on Leonard's hot paths.
func Span(ctx context.Context, _ string) (context.Context, EndSpan) {
	return ctx, noopEnd
}

// Enabled reports whether real tracing is compiled in. Mostly useful
// in tests that assert the no-op path is selected when no tag is set.
func Enabled() bool { return false }

func noopShutdown(_ context.Context) error { return nil }
func noopEnd()                              {}
