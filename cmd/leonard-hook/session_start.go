package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/dispatcher"
)

func newSessionStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "session-start",
		Short: "Handle a Claude Code SessionStart event.",
		Long: `Reads the SessionStart hook envelope from stdin, dispatches to every
enabled adapter, aggregates their AdditionalContext, and writes a
SessionStartResponse. The code adapter still produces the recent-
decisions context block; the ground-truth and self-logging adapters
add their session-start surfaces alongside.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(root, dataDirName)

			dispatcher.SetGlobalStderr(os.Stderr)
			loaded, err := dispatcher.LoadEnabled(cmd.Context(), root, dataDir, os.Stderr)
			if err != nil {
				return fmt.Errorf("session-start: load adapters: %w", err)
			}
			defer loaded.Close()

			if err := loaded.HandleSessionStart(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("session-start: %w", err)
			}
			return nil
		},
	}
}
