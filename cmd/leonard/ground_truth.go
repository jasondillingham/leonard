package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

func newGroundTruthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ground-truth",
		Short: "Tools for inspecting and maintaining the ground-truth tree.",
		Long:  "Subcommands: lint (validate syntax + schema), stats (counts + staleness).",
	}
	cmd.AddCommand(newGroundTruthLintCmd())
	cmd.AddCommand(newGroundTruthStatsCmd())
	return cmd
}

func newGroundTruthLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint",
		Short: "Validate facts.yaml / stories.md / do-not-claim.md / filters.yaml.",
		Long: `Re-loads the ground-truth tree and reports any parse errors with
file:line context. Returns exit 1 if any file fails to parse;
exit 0 if the whole tree loads cleanly.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			a := groundtruth.New()
			raw, err := loadGroundTruthRaw(dataDir)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "leonard ground-truth lint: FAIL\n  %v\n", err)
				return &exitCode{code: 1}
			}
			if err := a.Init(context.Background(), adapters.Config{
				ProjectRoot: projectRoot,
				Stderr:      cmd.ErrOrStderr(),
				Raw:         raw,
			}); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "leonard ground-truth lint: FAIL\n  %v\n", err)
				return &exitCode{code: 1}
			}
			defer a.Close()
			gta := a.(*groundtruth.GroundTruthAdapter)

			fmt.Fprintln(cmd.OutOrStdout(), "leonard ground-truth lint: OK")
			fmt.Fprintf(cmd.OutOrStdout(), "  facts:     %s\n", presence(!gta.Facts().IsEmpty()))
			fmt.Fprintf(cmd.OutOrStdout(), "  stories:   %d entr%s\n",
				len(gta.Stories()), pluralize(len(gta.Stories()), "y", "ies"))
			fmt.Fprintf(cmd.OutOrStdout(), "  rules:     %d entr%s\n",
				len(gta.Rules()), pluralize(len(gta.Rules()), "y", "ies"))
			fmt.Fprintf(cmd.OutOrStdout(), "  filters:   %s\n", presence(!gta.Filters().IsEmpty()))
			return nil
		},
	}
}

func newGroundTruthStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Report fact / story / rule counts and stale last_verified dates.",
		Long: `Loads the ground-truth tree and prints:

  - fact count (top-level keys + recursive scalar count)
  - story count
  - do-not-claim rule count (grouped by category)
  - filters: path_filters / content_filters counts
  - stale facts: entries whose last_verified is more than 6 months
    old (or missing)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			a := groundtruth.New()
			raw, err := loadGroundTruthRaw(dataDir)
			if err != nil {
				return fmt.Errorf("ground-truth stats: %w", err)
			}
			if err := a.Init(context.Background(), adapters.Config{
				ProjectRoot: projectRoot,
				Stderr:      cmd.ErrOrStderr(),
				Raw:         raw,
			}); err != nil {
				return fmt.Errorf("ground-truth stats: %w", err)
			}
			defer a.Close()
			gta := a.(*groundtruth.GroundTruthAdapter)

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "## facts.yaml")
			facts := gta.Facts()
			if facts.IsEmpty() {
				fmt.Fprintln(out, "  (empty)")
			} else {
				fmt.Fprintf(out, "  top-level keys: %d\n", len(facts.Root))
				fmt.Fprintf(out, "  scalars total:  %d\n", countScalars(facts.Root))
				stale := findStaleFacts(facts.Root, 6*30*24*time.Hour)
				if len(stale) > 0 {
					fmt.Fprintf(out, "  stale entries (last_verified > 6mo or missing):\n")
					for _, s := range stale {
						fmt.Fprintf(out, "    - %s\n", s)
					}
				}
			}
			fmt.Fprintln(out)

			fmt.Fprintln(out, "## stories.md")
			stories := gta.Stories()
			if len(stories) == 0 {
				fmt.Fprintln(out, "  (empty)")
			} else {
				names := make([]string, 0, len(stories))
				for _, s := range stories {
					names = append(names, s.Name)
				}
				sort.Strings(names)
				fmt.Fprintf(out, "  %d stor%s: %s\n",
					len(names), pluralize(len(names), "y", "ies"),
					strings.Join(names, ", "))
			}
			fmt.Fprintln(out)

			fmt.Fprintln(out, "## do-not-claim.md")
			rules := gta.Rules()
			if len(rules) == 0 {
				fmt.Fprintln(out, "  (empty)")
			} else {
				byCategory := map[string]int{}
				for _, r := range rules {
					cat := r.Category
					if cat == "" {
						cat = "(uncategorized)"
					}
					byCategory[cat]++
				}
				fmt.Fprintf(out, "  %d total\n", len(rules))
				cats := make([]string, 0, len(byCategory))
				for c := range byCategory {
					cats = append(cats, c)
				}
				sort.Strings(cats)
				for _, c := range cats {
					fmt.Fprintf(out, "    %s: %d\n", c, byCategory[c])
				}
			}
			fmt.Fprintln(out)

			fmt.Fprintln(out, "## filters.yaml")
			f := gta.Filters()
			if f.IsEmpty() && len(f.PathFilters) == 0 && len(f.ContentFilters) == 0 {
				fmt.Fprintln(out, "  (empty)")
			} else {
				fmt.Fprintf(out, "  path_filters:    %d\n", len(f.PathFilters))
				fmt.Fprintf(out, "  content_filters: %d\n", len(f.ContentFilters))
			}
			return nil
		},
	}
}

// presence reports "yes (N keys)" or "no" for a top-level
// presence/absence check.
func presence(has bool) string {
	if has {
		return "yes"
	}
	return "absent"
}

// countScalars walks a YAML-decoded tree and counts leaf scalars
// (strings/numbers/bools/dates). Operator-facing rough metric of
// "how many facts are recorded."
func countScalars(node any) int {
	switch v := node.(type) {
	case map[string]any:
		n := 0
		for _, c := range v {
			n += countScalars(c)
		}
		return n
	case []any:
		n := 0
		for _, c := range v {
			n += countScalars(c)
		}
		return n
	case nil:
		return 0
	default:
		return 1
	}
}

// findStaleFacts walks the tree looking for map entries that have
// a last_verified field. Returns dotted paths to entries whose
// last_verified is more than threshold old (or missing/unparseable).
func findStaleFacts(root map[string]any, threshold time.Duration) []string {
	var stale []string
	walkForStale(root, "", threshold, &stale)
	sort.Strings(stale)
	return stale
}

func walkForStale(node any, path string, threshold time.Duration, stale *[]string) {
	switch v := node.(type) {
	case map[string]any:
		// Check this map for last_verified.
		if lv, ok := v["last_verified"]; ok {
			if isStale(lv, threshold) {
				p := path
				if p == "" {
					p = "(root)"
				}
				*stale = append(*stale, p)
			}
		}
		// Recurse.
		for k, c := range v {
			child := k
			if path != "" {
				child = path + "." + k
			}
			walkForStale(c, child, threshold, stale)
		}
	case []any:
		for i, c := range v {
			child := fmt.Sprintf("%s[%d]", path, i)
			walkForStale(c, child, threshold, stale)
		}
	}
}

func isStale(lv any, threshold time.Duration) bool {
	var ts time.Time
	switch v := lv.(type) {
	case time.Time:
		ts = v
	case string:
		var err error
		ts, err = time.Parse("2006-01-02", v)
		if err != nil {
			return true // unparseable counts as stale
		}
	default:
		return true
	}
	return time.Since(ts) > threshold
}
