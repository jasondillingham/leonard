// Command leonard is the CLI entrypoint for the Leonard ground-truth toolkit.
//
// Subcommands wrap the same store + indexer used by leonard-mcp and
// leonard-hook so operators can drive setup, inspection, and manual
// operations from the shell.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(newDefaultRuntime())
	root.SetContext(ctx)

	if err := root.Execute(); err != nil {
		var ec *exitCode
		if errors.As(err, &ec) {
			if ec.msg != "" {
				fmt.Fprintln(os.Stderr, ec.msg)
			}
			os.Exit(ec.code)
		}
		fmt.Fprintln(os.Stderr, "leonard:", err)
		os.Exit(2)
	}
}
