package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newTruthStoryCmd(rt Runtime) *cobra.Command {
	var (
		scope          string
		since          string
		format         string
		limit          int
		includeTrivial bool
	)
	cmd := &cobra.Command{
		Use:   "truth-story",
		Short: "Render the chronological narrative of all truth-source changes.",
		Long: `Prints every decision-log entry that carries a TruthChange block,
oldest-first. Useful for "how did this project's truth get to its
current state?"

Filters:
  --scope=domain|toolkit  restrict to one truth scope
  --since=YYYY-MM-DD       only entries on or after the date
  --limit=N                cap the result (default 200)
  --include-trivial        show --trivial bypass entries (default: collapse)

Output formats:
  --format=markdown        human-readable (default)
  --format=json            machine-readable, full schema
  --format=plain           one entry per line, grep-friendly`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}

			sinceUnix := int64(0)
			if since != "" {
				t, err := time.Parse("2006-01-02", since)
				if err != nil {
					return fmt.Errorf("truth-story: --since must be YYYY-MM-DD (got %q): %w", since, err)
				}
				sinceUnix = t.Unix()
			}

			switch scope {
			case "", "domain", "toolkit":
				// allowed
			default:
				return fmt.Errorf("truth-story: --scope must be domain or toolkit (got %q)", scope)
			}

			switch format {
			case "", "markdown", "json", "plain":
				// allowed
			default:
				return fmt.Errorf("truth-story: --format must be markdown, json, or plain (got %q)", format)
			}

			rows, err := rt.GetTruthChanges(cmd.Context(), dataDir, scope, sinceUnix, limit)
			if err != nil {
				return fmt.Errorf("truth-story: %w", err)
			}

			out := cmd.OutOrStdout()
			renderStory(out, rows, format, includeTrivial)
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "restrict to one scope: domain or toolkit")
	cmd.Flags().StringVar(&since, "since", "", "YYYY-MM-DD lower bound on recorded date")
	cmd.Flags().StringVar(&format, "format", "markdown", "output format: markdown, json, plain")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of entries (0 = default 200)")
	cmd.Flags().BoolVar(&includeTrivial, "include-trivial", false, "include trivial-bypass entries (default: collapse)")
	return cmd
}

func renderStory(out io.Writer, rows []TruthHistoryRow, format string, includeTrivial bool) {
	if format == "" {
		format = "markdown"
	}

	// Filter trivial entries up-front so format-specific renderers
	// don't have to repeat the logic.
	var (
		shown     []TruthHistoryRow
		collapsed int
	)
	for _, r := range rows {
		if r.Trivial && !includeTrivial {
			collapsed++
			continue
		}
		shown = append(shown, r)
	}

	switch format {
	case "json":
		renderStoryJSON(out, shown, collapsed)
	case "plain":
		renderStoryPlain(out, shown, collapsed)
	default:
		renderStoryMarkdown(out, shown, collapsed)
	}
}

func renderStoryMarkdown(out io.Writer, rows []TruthHistoryRow, collapsedTrivial int) {
	if len(rows) == 0 && collapsedTrivial == 0 {
		fmt.Fprintln(out, "leonard: no truth-change entries recorded.")
		return
	}
	fmt.Fprintf(out, "# Truth-source change narrative\n\n")
	fmt.Fprintf(out, "%d entr%s shown.\n\n", len(rows), pluralize(len(rows), "y", "ies"))
	for _, r := range rows {
		printTruthHistoryRow(out, r)
	}
	if collapsedTrivial > 0 {
		fmt.Fprintf(out, "(%d trivial entr%s collapsed; pass --include-trivial to see all)\n",
			collapsedTrivial, pluralize(collapsedTrivial, "y", "ies"))
	}
}

func renderStoryJSON(out io.Writer, rows []TruthHistoryRow, collapsedTrivial int) {
	type payload struct {
		Entries          []TruthHistoryRow `json:"entries"`
		CollapsedTrivial int               `json:"collapsed_trivial,omitempty"`
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload{Entries: rows, CollapsedTrivial: collapsedTrivial})
}

func renderStoryPlain(out io.Writer, rows []TruthHistoryRow, collapsedTrivial int) {
	for _, r := range rows {
		ts := time.Unix(r.RecordedAt, 0).UTC().Format("2006-01-02T15:04:05Z")
		scope := r.Scope
		if scope == "" {
			scope = "?"
		}
		trivial := ""
		if r.Trivial {
			trivial = " TRIVIAL"
		}
		motivation := strings.ReplaceAll(r.MotivatedBy, "\t", " ")
		motivation = strings.ReplaceAll(motivation, "\n", " ")
		fmt.Fprintf(out, "%s\t#%d\t%s%s\t%s\t%s\n", ts, r.ID, scope, trivial, r.Topic, motivation)
	}
	if collapsedTrivial > 0 {
		fmt.Fprintf(out, "# %d trivial entries collapsed (pass --include-trivial)\n", collapsedTrivial)
	}
}
