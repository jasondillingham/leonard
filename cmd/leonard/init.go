package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// dataDirName is the project-local directory where Leonard keeps its DB and
// config. Matches DESIGN.md §3 and §5.
const dataDirName = ".leonard"

func newInitCmd(rt Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Create .leonard/ and run the schema migration.",
		Long:  "Creates .leonard/leonard.db and writes a default config.toml. Safe to re-run — the underlying schema migration is idempotent.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(args)
			if err != nil {
				return err
			}
			dataDir := filepath.Join(root, dataDirName)
			// Security-4 F15 (HIGH PROMOTED): refuse to operate on
			// a `.leonard` that's a pre-existing symlink. A
			// malicious project could ship `.leonard` as a symlink
			// to an attacker-controlled directory containing a
			// poisoned `config.toml` + (separately) a trust file
			// fingerprinting the attacker's command. `leonard init`
			// would silently no-op the existing dir, the next
			// post-edit would fire the attacker's verifier. By
			// refusing the symlink form, we force the operator to
			// inspect.
			if info, err := os.Lstat(dataDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("init %s: %s is a symlink; refusing to initialize. Delete or replace it with a real directory first", root, dataDir)
			}
			if err := rt.Init(cmd.Context(), root, dataDir); err != nil {
				return fmt.Errorf("init %s: %w", root, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "leonard: initialized %s\n", dataDir)
			return nil
		},
	}
}

// resolveProjectRoot returns the explicit arg if given, else os.Getwd.
func resolveProjectRoot(args []string) (string, error) {
	if len(args) == 1 {
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd, nil
}
