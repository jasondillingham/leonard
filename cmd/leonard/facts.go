package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
	"github.com/jasondillingham/leonard/internal/index"
)

func newFactsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "facts",
		Short: "Inspect facts.yaml: history and cross-file impact.",
		Long:  "Subcommands: diff (git history of facts.yaml), impact (files that reference a fact value).",
	}
	cmd.AddCommand(newFactsDiffCmd())
	cmd.AddCommand(newFactsImpactCmd())
	return cmd
}

// newFactsDiffCmd shows what changed in facts.yaml using git history.
// Uncommitted changes (git diff HEAD) are shown first, then recent
// commit history (git log -p). Falls back gracefully when git is
// unavailable or the file isn't tracked.
func newFactsDiffCmd() *cobra.Command {
	var n int
	var since string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show git history of facts.yaml with timestamps.",
		Long: `Shows uncommitted changes to facts.yaml followed by recent commit
history. Requires git; exits 1 when git is unavailable or facts.yaml
is not tracked.

Examples:
  leonard facts diff
  leonard facts diff -n 10
  leonard facts diff --since="2 weeks ago"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)
			factsRel := filepath.Join(".leonard", "ground-truth", "facts.yaml")
			factsAbs := filepath.Join(projectRoot, factsRel)

			out := cmd.OutOrStdout()

			if _, statErr := os.Stat(factsAbs); statErr != nil {
				fmt.Fprintf(out, "facts.yaml not found at %s\n", factsAbs)
				return nil
			}

			// Uncommitted changes first.
			unstaged, gitErr := runGitIn(projectRoot, "diff", "HEAD", "--", factsRel)
			if gitErr != nil {
				fmt.Fprintf(out, "git not available or facts.yaml is not tracked: %v\n", gitErr)
				fmt.Fprintf(out, "facts.yaml path: %s\n", factsAbs)
				return &exitCode{code: 1}
			}
			if strings.TrimSpace(unstaged) != "" {
				fmt.Fprintf(out, "## Uncommitted changes\n\n")
				fmt.Fprintln(out, unstaged)
			} else {
				fmt.Fprintln(out, "## Uncommitted changes: none")
			}

			// Commit history.
			logArgs := []string{"log", "--date=short", fmt.Sprintf("-n%d", n), "-p", "--", factsRel}
			if since != "" {
				logArgs = append([]string{"log", "--date=short", fmt.Sprintf("-n%d", n), "--since=" + since, "-p", "--", factsRel}, logArgs[6:]...)
				logArgs = []string{"log", "--date=short", fmt.Sprintf("-n%d", n), "--since=" + since, "-p", "--", factsRel}
			}
			history, _ := runGitIn(projectRoot, logArgs...)
			fmt.Fprintf(out, "\n## Commit history (last %d)\n\n", n)
			if strings.TrimSpace(history) == "" {
				fmt.Fprintln(out, "(no commits touch facts.yaml yet)")
			} else {
				fmt.Fprintln(out, history)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&n, "number", "n", 5, "number of commits to show")
	cmd.Flags().StringVar(&since, "since", "", `show commits more recent than a date (e.g. "2 weeks ago")`)
	return cmd
}

// newFactsImpactCmd finds .md files that reference a given fact's value.
func newFactsImpactCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "impact <key>",
		Short: "Show .md files that reference a fact's value.",
		Long: `Looks up the value at <key> in facts.yaml (dot-separated path,
e.g. tech_stack.primary_language) and scans .md files for references.
Reports file path, hit count, and first-occurrence line number.

When the key resolves to a map or array all contained scalars are
searched independently.

Exit code:
  0  key found and scan complete (even if no files reference it)
  1  key not found in facts.yaml or facts.yaml missing

Examples:
  leonard facts impact tech_stack.primary_language
  leonard facts impact bosun
  leonard facts impact metrics.message_count`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keyPath := args[0]

			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			a := groundtruth.New()
			raw, err := loadGroundTruthRaw(dataDir)
			if err != nil {
				return fmt.Errorf("facts impact: %w", err)
			}
			if err := a.Init(context.Background(), adapters.Config{
				ProjectRoot: projectRoot,
				Stderr:      cmd.ErrOrStderr(),
				Raw:         raw,
			}); err != nil {
				return fmt.Errorf("facts impact: init: %w", err)
			}
			defer a.Close()
			gta := a.(*groundtruth.GroundTruthAdapter)

			leaves, err := groundtruth.ResolveFactKey(gta.Facts(), keyPath)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "facts impact: %v\n", err)
				return &exitCode{code: 1}
			}

			ig, _ := index.LoadIgnore(projectRoot)

			out := cmd.OutOrStdout()
			root := projectRoot

			// Collect results per leaf value.
			type entry struct {
				leaf    groundtruth.FactLeaf
				results []groundtruth.ImpactResult
			}
			var entries []entry
			for _, leaf := range leaves {
				results, findErr := groundtruth.FindImpactedFiles(root, leaf.Value, leaf.Path, ig)
				if findErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "facts impact: scan: %v\n", findErr)
					continue
				}
				// Filter by scope glob if provided.
				if scope != "" {
					var filtered []groundtruth.ImpactResult
					for _, r := range results {
						rel, _ := filepath.Rel(projectRoot, r.Path)
						matched, _ := filepath.Match(scope, rel)
						if matched {
							filtered = append(filtered, r)
						}
					}
					results = filtered
				}
				// Sort: strong hits first, then alphabetically by path.
				sort.Slice(results, func(i, j int) bool {
					si := results[i].StrongHits > 0
					sj := results[j].StrongHits > 0
					if si != sj {
						return si
					}
					return results[i].Path < results[j].Path
				})
				entries = append(entries, entry{leaf: leaf, results: results})
			}

			// Sort leaves by path for deterministic output.
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].leaf.Path < entries[j].leaf.Path
			})

			totalFiles := 0
			for _, e := range entries {
				totalFiles += len(e.results)
			}

			if totalFiles == 0 {
				fmt.Fprintf(out, "facts impact %s: no .md files reference %s\n",
					keyPath, formatLeafValues(leaves))
				return nil
			}

			for _, e := range entries {
				if len(e.results) == 0 {
					continue
				}
				fmt.Fprintf(out, "## %s = %q\n", e.leaf.Path, e.leaf.Value)
				for _, r := range e.results {
					rel, _ := filepath.Rel(projectRoot, r.Path)
					annotation := ""
					if r.StrongHits == 0 {
						annotation = " — value-only"
					}
					fmt.Fprintf(out, "  %s:%d  (%d occurrence%s)%s\n",
						rel, r.FirstLine, r.Hits, pluralize(r.Hits, "", "s"), annotation)
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "glob pattern to restrict file scan (e.g. \"docs/**/*.md\")")
	return cmd
}

// runGitIn runs a git command in dir and returns combined stdout. Returns
// an error if git is not on PATH or the command exits non-zero.
func runGitIn(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return buf.String(), nil
}

func formatLeafValues(leaves []groundtruth.FactLeaf) string {
	if len(leaves) == 1 {
		return fmt.Sprintf("%q", leaves[0].Value)
	}
	vals := make([]string, len(leaves))
	for i, l := range leaves {
		vals[i] = fmt.Sprintf("%q", l.Value)
	}
	return strings.Join(vals, ", ")
}
