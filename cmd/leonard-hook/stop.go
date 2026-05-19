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

// claimsStoreOpener returns a ClaimsReader for the given project root. When
// the SQLite file is absent (a fresh project that hasn't run `leonard init`)
// implementations MUST return (nil, nil) so HandleStop can emit a no-op
// response instead of failing the session stop. Mirrors the storeOpener
// pattern in session_start.go; kept separate so each hook's contract maps
// to exactly one store interface.
type claimsStoreOpener func(projectRoot string) (hooks.ClaimsReader, error)

func defaultClaimsStoreOpener(projectRoot string) (hooks.ClaimsReader, error) {
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

func newStopCmd() *cobra.Command {
	return newStopCmdWithOpener(defaultClaimsStoreOpener)
}

// newStopCmdWithOpener is the testing seam. stop_test.go substitutes a stub
// opener to drive the missing-store / error paths without touching the
// filesystem.
func newStopCmdWithOpener(open claimsStoreOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Handle a Claude Code Stop event.",
		Long:  "Reads the Stop hook envelope from stdin, queries the Leonard store for unverified claims scoped to the payload's session, and writes a hook response surfacing them as additional context. The hook is advisory — it never blocks the session from ending.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			reader, err := open(root)
			if err != nil {
				return fmt.Errorf("stop: open store: %w", err)
			}
			cfg, err := config.LoadOrDefault(filepath.Join(root, dataDirName, config.Filename))
			if err != nil {
				return fmt.Errorf("stop: load config: %w", err)
			}
			opts := hooks.StopOptions{
				Claims: reader,
				Limit:  cfg.Hooks.SurfaceUnverifiedClaimsAtStop,
			}
			if err := hooks.HandleStop(cmd.Context(), opts, cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
			return nil
		},
	}
}
