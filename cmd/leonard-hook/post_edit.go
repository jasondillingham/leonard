package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/hooks"
)

// dataDirName mirrors cmd/leonard. Kept independent so the two binaries stay
// import-isolated.
const dataDirName = ".leonard"

func newPostEditCmd(b Backend) *cobra.Command {
	return &cobra.Command{
		Use:   "post-edit",
		Short: "Handle a Claude Code PostToolUse event (Edit/Write).",
		Long:  "Reads the PostToolUse hook envelope from stdin, refreshes the symbol index for the touched file, runs `go vet ./...`, persists a claim row, and writes a hook response to stdout.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			opts := hooks.PostEditOptions{
				Indexer:     b.Indexer(root),
				Claims:      b.Claims(root),
				ProjectRoot: root,
			}
			if err := hooks.HandlePostEdit(cmd.Context(), opts, cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("post-edit: %w", err)
			}
			return nil
		},
	}
}

// resolveProjectRoot walks up from cwd looking for a .leonard/ directory. If
// none is found we fall back to cwd — the post-edit handler is then a no-op
// at vet time (no go.mod check passes) but still records a claim row.
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
