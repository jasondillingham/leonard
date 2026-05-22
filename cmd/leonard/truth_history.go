package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newTruthHistoryCmd(rt Runtime) *cobra.Command {
	var (
		limit          int
		includeTrivial bool
	)
	cmd := &cobra.Command{
		Use:   "truth-history [path]",
		Short: "Show the decision-log entries that touched a truth-source file.",
		Long: `Prints the chronological narrative of how the supplied truth-source
file got to its current shape. Entries are returned oldest-first so the
output reads as a story.

Source: decisions whose truth_change.files includes the supplied path
(#21's schema). Trivial entries (--trivial bypasses) are collapsed by
default; pass --include-trivial to see them all.

Example:
  leonard truth-history .leonard/ground-truth/do-not-claim.md
  leonard truth-history internal/adapters/code/post_edit.go --include-trivial`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			rows, err := rt.GetTruthHistory(cmd.Context(), dataDir, path, limit)
			if err != nil {
				return fmt.Errorf("truth-history: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintf(out, "leonard: no truth-change entries for %s\n", path)
				return nil
			}
			fmt.Fprintf(out, "Truth history for %s (%d entr%s):\n\n",
				path, len(rows), pluralize(len(rows), "y", "ies"))

			shown := 0
			collapsed := 0
			for _, r := range rows {
				if r.Trivial && !includeTrivial {
					collapsed++
					continue
				}
				printTruthHistoryRow(out, r)
				shown++
			}
			if collapsed > 0 {
				fmt.Fprintf(out, "(%d trivial entr%s collapsed; pass --include-trivial to see all)\n",
					collapsed, pluralize(collapsed, "y", "ies"))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of entries returned (0 = default 200)")
	cmd.Flags().BoolVar(&includeTrivial, "include-trivial", false, "include trivial-bypass entries (default: collapse)")
	return cmd
}

// printTruthHistoryRow renders one row as a multi-line block. Format
// is intentionally human-readable rather than greppable — the v0.9
// `truth-story` CLI (#35) will add machine-friendly --format=json.
func printTruthHistoryRow(out interface{ Write([]byte) (int, error) }, r TruthHistoryRow) {
	ts := time.Unix(r.RecordedAt, 0).UTC().Format("2006-01-02 15:04 UTC")
	scope := r.Scope
	if scope == "" {
		scope = "?"
	}
	header := fmt.Sprintf("[#%d] %s  scope=%s", r.ID, ts, scope)
	if r.Trivial {
		header += "  TRIVIAL"
	}
	if r.Supersedes != nil {
		header += fmt.Sprintf("  supersedes=#%d", *r.Supersedes)
	}
	fmt.Fprintln(out, header)
	if r.Topic != "" {
		fmt.Fprintln(out, "  topic:        "+r.Topic)
	}
	if r.MotivatedBy != "" {
		fmt.Fprintln(out, "  motivated_by: "+r.MotivatedBy)
	}
	if r.Choice != "" {
		fmt.Fprintln(out, "  choice:       "+r.Choice)
	}
	if r.Reasoning != "" {
		fmt.Fprintln(out, "  reasoning:    "+oneLine(r.Reasoning))
	}
	if r.DiffRef != "" {
		fmt.Fprintln(out, "  diff:         "+r.DiffRef)
	}
	if r.Trivial && r.TrivialReason != "" {
		fmt.Fprintln(out, "  trivial:      "+r.TrivialReason)
	}
	if len(r.Files) > 1 {
		fmt.Fprintln(out, "  files:        "+strings.Join(r.Files, ", "))
	}
	fmt.Fprintln(out)
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// oneLine flattens multi-line reasoning to a single line for the
// human-readable rendering. Long reasonings stay readable; JSON
// output (deferred to v0.9 / #35) keeps the original.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}
