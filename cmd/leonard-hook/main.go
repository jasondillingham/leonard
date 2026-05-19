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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(newDefaultBackend())
	root.SetContext(ctx)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "leonard-hook:", err)
		os.Exit(exitCodeFor(err))
	}
}
