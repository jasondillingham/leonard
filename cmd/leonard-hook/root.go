package main

import (
	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// Backend supplies the post-edit handler with its store-backed indexer and
// claim recorder. Splitting it out means we can unit-test the cobra wiring
// without booting a real SQLite store; the production wire-up lives in
// backend_real.go.
type Backend interface {
	Indexer(projectRoot string) hooks.Indexer
	Claims(projectRoot string) hooks.ClaimRecorder
	Close() error
}

func newRootCmd(b Backend) *cobra.Command {
	root := &cobra.Command{
		Use:           "leonard-hook",
		Short:         "Dispatcher for Claude Code hook events.",
		Long:          "Subcommands map 1:1 to Claude Code hook event types. Each reads the hook JSON envelope from stdin and writes a hook response to stdout.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newPostEditCmd(b))
	root.AddCommand(newSessionStartCmd())
	return root
}
