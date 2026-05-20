// Command leonard-hook dispatches Claude Code hook events (PostToolUse,
// PreToolUse, SessionStart, Stop) to the matching Leonard handler.
//
// It is intended to be invoked from .claude/settings.json with a subcommand
// like `leonard-hook post-edit`.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jasondillingham/leonard/internal/telemetry"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Optional OpenTelemetry tracing. No-op by default; only the
	// `-tags otel` build pulls in the SDK and reads OTEL_* env vars
	// to decide whether to actually export spans. An Init failure is
	// non-fatal — the hook's core job doesn't depend on telemetry.
	shutdown, err := telemetry.Init(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook: telemetry init:", err)
	}
	defer func() { _ = shutdown(ctx) }()

	root := newRootCmd(newDefaultBackend())
	root.SetContext(ctx)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook:", err)
		os.Exit(exitCodeFor(err))
	}
}
