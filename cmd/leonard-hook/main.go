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

// hookWallClockBudget is the hard ceiling on one hook invocation's
// lifetime. Claude Code's default hook timeout is 60s — but timing
// out only means Claude Code stops WAITING; the child keeps running.
// incident-1: a session-start scan that ignored ctx spun at 2 cores
// for hours as an orphan after SIGTERM (the signal was trapped by
// NotifyContext, cancelling a context nobody checked). The deadline
// plus the watchdog below guarantee the process dies regardless of
// what a handler does.
const hookWallClockBudget = 55 * time.Second

// watchdogGrace is how long the watchdog waits after ctx cancellation
// (signal or deadline) before force-exiting. Longer than
// telemetryShutdownTimeout so a well-behaved wind-down, including the
// telemetry flush, is never cut short.
const watchdogGrace = telemetryShutdownTimeout + 5*time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancelBudget := context.WithTimeout(ctx, hookWallClockBudget)
	defer cancelBudget()

	// Watchdog: once the context is cancelled — SIGTERM/SIGINT or the
	// wall-clock budget — a handler that fails to observe ctx gets
	// watchdogGrace to finish before the process is force-killed. On
	// the normal path ctx is never cancelled and this goroutine parks
	// until process exit. Exit code 1: a plain error to Claude Code,
	// never the blocking exit 2.
	go func() {
		<-ctx.Done()
		time.Sleep(watchdogGrace)
		fmt.Fprintf(os.Stderr, "leonard-hook: watchdog: handler still running %s after cancellation (%v); forcing exit\n", watchdogGrace, context.Cause(ctx))
		os.Exit(1)
	}()

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
//
// Bughunt-9 F3 (HIGH): close the backend before returning so the
// store's wal_checkpoint(PASSIVE) actually fires. Without this each
// short-lived hook process leaks an unchecked-pointed WAL handle and
// the WAL file grows unbounded across many invocations (15 MB after
// 700 hooks pre-v0.52).
func runRoot(ctx context.Context) int {
	backend := newDefaultBackend()
	defer func() {
		if err := backend.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "leonard-hook: backend close:", err)
		}
	}()
	root := newRootCmd(backend)
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook:", err)
		return exitCodeFor(err)
	}
	return 0
}
