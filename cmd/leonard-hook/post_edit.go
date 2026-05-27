package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/dispatcher"
)

// dataDirName mirrors cmd/leonard. Kept independent so the two
// binaries stay import-isolated.
const dataDirName = ".leonard"

func newPostEditCmd(_ Backend) *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "post-edit",
		Short: "Handle a Claude Code PostToolUse event (Edit/Write).",
		Long: `Reads the PostToolUse hook envelope from stdin, dispatches to every
enabled adapter, and writes the merged response to stdout. The code
adapter still runs the v0.52 index-refresh + verifier-execution +
claim-ledger flow internally; the ground-truth and self-logging
adapters add their post-edit surfaces alongside.

By default the hook runs silently on clean edits (no systemMessage in
the terminal). Pass --verbose to see a status line after every edit.`,
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
				return blockOnDecode(fmt.Errorf("post-edit: load adapters: %w", err))
			}
			defer loaded.Close()

			loaded.Verbose = verbose
			if err := loaded.HandlePostEdit(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return blockOnDecode(fmt.Errorf("post-edit: %w", err))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "print a status line after every edit (default: silent on clean runs)")
	return cmd
}

// resolveProjectRoot walks up from cwd looking for a .leonard/ directory. If
// none is found we fall back to cwd. Mirrors the pre-#46 behavior so the
// dispatcher's auto-detection has the same starting point.
func resolveProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, dataDirName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd, nil
		}
		dir = parent
	}
}
