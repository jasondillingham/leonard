package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/dispatcher"
)

func newPreEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pre-edit",
		Short: "Handle a Claude Code PreToolUse event (Edit/Write).",
		Long: `Reads the PreToolUse hook envelope from stdin, dispatches to every
enabled adapter (code, ground-truth, self-logging), aggregates verdicts
with deny-beats-pass semantics, and writes the matching response to
stdout. The code adapter retains the v0.52 fabrication-guard logic;
the ground-truth and self-logging adapters add the v1.0 surfaces.

Adapter selection comes from .leonard/config.toml's [[adapters]]
blocks (when present) or auto-detection (go.mod or post_edit.verify
→ code; .leonard/ground-truth/ or truth_dir set → ground-truth).
See internal/config/config.go EnabledAdapters for the rules.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(root, ".leonard")

			dispatcher.SetGlobalStderr(os.Stderr)
			loaded, err := dispatcher.LoadEnabled(cmd.Context(), root, dataDir, os.Stderr)
			if err != nil {
				return blockOnDecode(fmt.Errorf("pre-edit: load adapters: %w", err))
			}
			defer loaded.Close()

			if err := loaded.HandlePreEdit(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return blockOnDecode(fmt.Errorf("pre-edit: %w", err))
			}
			return nil
		},
	}
}
