package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/config"
)

// dataDirName is the project-local directory where Leonard keeps its DB and
// config. Matches DESIGN.md §3 and §5.
const dataDirName = ".leonard"

func newInitCmd(rt Runtime) *cobra.Command {
	var adapterFlag string
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Create .leonard/ and run the schema migration.",
		Long:  "Creates .leonard/leonard.db and writes a default config.toml. With --adapter=ground-truth, also scaffolds the .leonard/ground-truth/ tree. Safe to re-run — the schema migration and ground-truth scaffold are both idempotent.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(args)
			if err != nil {
				return err
			}
			adapters, err := parseAdapterFlag(adapterFlag)
			if err != nil {
				return fmt.Errorf("init %s: %w", root, err)
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
			// Capture whether config.toml is fresh so we can write
			// [[adapters]] blocks after scaffolding when ground-truth
			// is requested. We check before rt.Init because rt.Init
			// may create the file with no adapter blocks.
			cfgPath := filepath.Join(dataDir, config.Filename)
			cfgFresh := false
			if _, statErr := os.Stat(cfgPath); errors.Is(statErr, fs.ErrNotExist) {
				cfgFresh = true
			}

			// Always run the SQLite store init when "code" is in
			// the adapter set (the v0.52 default). Operators who
			// only want the ground-truth adapter (--adapter=
			// ground-truth) skip the DB; the .leonard/ground-
			// truth/ tree still lands.
			if adapters["code"] {
				if err := rt.Init(cmd.Context(), root, dataDir); err != nil {
					return fmt.Errorf("init %s: %w", root, err)
				}
			} else {
				if err := os.MkdirAll(dataDir, 0o755); err != nil {
					return fmt.Errorf("init %s: %w", root, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "leonard: initialized %s\n", dataDir)

			if adapters["ground-truth"] {
				created, err := scaffoldGroundTruth(dataDir)
				if err != nil {
					return fmt.Errorf("init %s: %w", root, err)
				}
				if len(created) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "leonard: ground-truth tree already present at %s\n", filepath.Join(dataDir, groundTruthDirName))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "leonard: scaffolded ground-truth tree (%d file(s) created)\n", len(created))
				}
			}

			// When config.toml was freshly created and ground-truth
			// is in the adapter set, overwrite it with explicit
			// [[adapters]] blocks so the operator gets a working
			// config without having to hand-edit TOML.
			if cfgFresh && adapters["ground-truth"] {
				var types []string
				if adapters["code"] {
					types = append(types, "code")
				}
				types = append(types, "ground-truth")
				cfg := config.DefaultWithAdapters(types...)
				if err := config.Save(cfg, cfgPath); err != nil {
					return fmt.Errorf("init %s: write config.toml: %w", root, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&adapterFlag, "adapter", "code", "Comma-separated adapters to enable. Values: code, ground-truth. Default: code.")
	return cmd
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
