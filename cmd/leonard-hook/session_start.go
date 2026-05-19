package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/config"
	"github.com/jasondillingham/leonard/internal/hooks"
	"github.com/jasondillingham/leonard/internal/store"
)

// storeOpener returns a DecisionReader for the given project root. When the
// SQLite file is absent (a fresh project that hasn't run `leonard init`)
// implementations MUST return (nil, nil) so HandleSessionStart can emit a
// no-op response instead of failing the new Claude Code session.
type storeOpener func(projectRoot string) (hooks.DecisionReader, error)

// defaultStoreOpener implements the open-or-skip contract described above. It
// stat-checks the DB file first so a missing-file error doesn't bubble up
// as a generic open failure.
func defaultStoreOpener(projectRoot string) (hooks.DecisionReader, error) {
	dbPath := filepath.Join(projectRoot, dataDirName, "leonard.db")
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dbPath, err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	return s, nil
}

func newSessionStartCmd() *cobra.Command {
	return newSessionStartCmdWithOpener(defaultStoreOpener)
}

// newSessionStartCmdWithOpener is the testing seam. session_start_test.go
// substitutes a stub opener here to drive the missing-store / error paths
// without touching the filesystem.
func newSessionStartCmdWithOpener(open storeOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "session-start",
		Short: "Handle a Claude Code SessionStart event.",
		Long:  "Reads the SessionStart hook envelope from stdin, queries the Leonard store for recent decisions, and writes a hook response that injects them as additional system context.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			reader, err := open(root)
			if err != nil {
				return fmt.Errorf("session-start: open store: %w", err)
			}
			cfg, err := config.LoadOrDefault(filepath.Join(root, dataDirName, config.Filename))
			if err != nil {
				return fmt.Errorf("session-start: load config: %w", err)
			}
			opts := hooks.SessionStartOptions{
				Decisions: reader,
				Limit:     cfg.Hooks.InjectDecisionsAtSessionStart,
			}
			if err := hooks.HandleSessionStart(cmd.Context(), opts, cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("session-start: %w", err)
			}
			return nil
		},
	}
}
