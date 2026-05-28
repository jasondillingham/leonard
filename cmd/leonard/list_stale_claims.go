package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

func newListStaleClaimsCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "list-stale-claims",
		Short: "Scan files for unverified or forbidden claims.",
		Long: `Walks the project tree (or a --scope glob) and runs the
ground-truth adapter's claim detector on each file. Reports every
unverified or forbidden claim with its line number and excerpt.

Exit code:
  0  no findings
  1  unverified findings
  2  forbidden findings (takes priority over unverified)
  >2 error

Examples:
  leonard list-stale-claims
  leonard list-stale-claims --scope="docs/**/*.md"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			a := groundtruth.New()
			raw, err := loadGroundTruthRaw(dataDir)
			if err != nil {
				return fmt.Errorf("list-stale-claims: %w", err)
			}
			if err := a.Init(context.Background(), adapters.Config{
				ProjectRoot: projectRoot,
				Stderr:      cmd.ErrOrStderr(),
				Raw:         raw,
			}); err != nil {
				return fmt.Errorf("list-stale-claims: init ground-truth adapter: %w", err)
			}
			defer a.Close()
			gta := a.(*groundtruth.GroundTruthAdapter)

			targets, err := collectTargets(projectRoot, scope)
			if err != nil {
				return fmt.Errorf("list-stale-claims: %w", err)
			}

			maxCode := 0
			for _, path := range targets {
				content, err := os.ReadFile(path)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "list-stale-claims: read %s: %v\n", path, err)
					continue
				}
				res := gta.Detect(string(content))
				if res.Summary.Unverified == 0 && res.Summary.Forbidden == 0 {
					continue
				}

				rel, _ := filepath.Rel(projectRoot, path)
				renderCheckResult(cmd.OutOrStdout(), rel, content, res, "plain")

				if res.Summary.Forbidden > 0 && maxCode < 2 {
					maxCode = 2
				} else if res.Summary.Unverified > 0 && maxCode < 1 {
					maxCode = 1
				}
			}

			if maxCode > 0 {
				return &exitCode{code: maxCode}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "glob pattern relative to project root (e.g. \"docs/**/*.md\"); defaults to all .md files")
	return cmd
}

// collectTargets returns the list of files to scan. When scope is set it is
// treated as a filepath.Glob pattern relative to projectRoot; when empty,
// every .md file under projectRoot (excluding .leonard/ and .git/) is returned.
func collectTargets(projectRoot, scope string) ([]string, error) {
	if scope != "" {
		pattern := filepath.Join(projectRoot, scope)
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("scope glob %q: %w", scope, err)
		}
		return matches, nil
	}

	var out []string
	err := filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".leonard" || name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) == ".md" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}
