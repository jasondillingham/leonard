package main

import (
	"encoding/json"
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
			// Match the session-start / stop pattern: when the project hasn't
			// run `leonard init` yet the DB is absent and there's nothing
			// useful for the hook to do. Emit a no-op {"continue": true} and
			// surface a one-line operator hint on stderr so the user knows
			// why nothing happened. Without this, the realBackend below
			// panics on the first Indexer call.
			if !leonardDBExists(root) {
				fmt.Fprintln(cmd.ErrOrStderr(), "leonard: post-edit skipped — run `leonard init` first")
				return json.NewEncoder(cmd.OutOrStdout()).Encode(hooks.HookResponse{Continue: true})
			}
			opts := hooks.PostEditOptions{
				Indexer:     b.Indexer(root),
				Claims:      b.Claims(root),
				ProjectRoot: root,
			}
			if err := hooks.HandlePostEdit(cmd.Context(), opts, cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return blockOnDecode(fmt.Errorf("post-edit: %w", err))
			}
			return nil
		},
	}
}

// leonardDBExists reports whether the SQLite store file is present at the
// expected path under projectRoot. Mirrors the stat-check in
// defaultStoreOpener / defaultClaimsStoreOpener so the three hooks degrade
// uniformly on a fresh checkout.
func leonardDBExists(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, dataDirName, "leonard.db"))
	return err == nil
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
