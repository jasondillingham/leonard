package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/dispatcher"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Handle a Claude Code Stop event.",
		Long: `Reads the Stop hook envelope from stdin, dispatches to every
enabled adapter, concatenates their SystemMessage lines, and writes
a StopResponse. The code adapter still surfaces unverified-claim
counts; the ground-truth adapter adds the per-session findings
digest.`,
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
				return fmt.Errorf("stop: load adapters: %w", err)
			}
			defer loaded.Close()

			if err := loaded.HandleStop(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
			return nil
		},
	}
}
