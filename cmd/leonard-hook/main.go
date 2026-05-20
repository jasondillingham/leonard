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
	"time"

	"github.com/jasondillingham/leonard/internal/telemetry"
)

// telemetryShutdownTimeout caps how long the hook waits for the
// telemetry SDK to flush pending spans on exit. Bughunt-3 otel F3
// measured an unreachable OTLP endpoint blocking the hook for ~30s
// (batch processor default ExportTimeout). 5s is a reasonable
// upper bound for export of the handful of spans one hook
// invocation produces, well under any reasonable Claude Code
// patience window.
const telemetryShutdownTimeout = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Optional OpenTelemetry tracing. No-op by default; only the
	// `-tags otel` build pulls in the SDK and reads OTEL_* env vars
	// to decide whether to actually export spans. An Init failure is
	// non-fatal — the hook's core job doesn't depend on telemetry.
	shutdown, initErr := telemetry.Init(ctx)
	if initErr != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook: telemetry init:", initErr)
	}

	code := runRoot(ctx)

	// Flush telemetry BEFORE os.Exit. Two bughunt-3 otel findings
	// pile up here:
	//   F1: deferred shutdown never runs after os.Exit, so every
	//       non-zero hook exit (including the load-bearing exit-2
	//       block path) used to drop queued spans.
	//   F2: ctx is signal-derived; on SIGINT it's already cancelled
	//       by the time we reach here, which aborts the flush
	//       instantly. Use a fresh background ctx with a short
	//       timeout instead.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), telemetryShutdownTimeout)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook: telemetry shutdown:", err)
	}
	os.Exit(code)
}

// runRoot dispatches the cobra command tree and returns the exit code
// that should propagate to the OS. Pulled out so main() can flush
// telemetry on the way out — `os.Exit` would skip a deferred
// shutdown.
func runRoot(ctx context.Context) int {
	root := newRootCmd(newDefaultBackend())
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook:", err)
		return exitCodeFor(err)
	}
	return 0
}
