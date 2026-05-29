package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newDecisionsCmd is the `leonard decisions ...` group. Mirrors the MCP
// decision tools (record_decision / get_decisions / get_stale_decisions)
// so the same log is greppable outside a Claude Code session.
func newDecisionsCmd(rt Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decisions",
		Short: "Inspect and record decisions in the Leonard log.",
		Long:  "Mirrors the MCP decision tools. Subcommands: list, add, stale. The MCP server is the canonical write surface during a Claude session; these commands are for inspection and one-off entries from the terminal.",
	}
	cmd.AddCommand(newDecisionsListCmd(rt))
	cmd.AddCommand(newDecisionsAddCmd(rt))
	cmd.AddCommand(newDecisionsStaleCmd(rt))
	return cmd
}

func newDecisionsListCmd(rt Runtime) *cobra.Command {
	var topic string
	var since int64
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recorded decisions (newest first).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			rows, err := rt.GetDecisions(cmd.Context(), dataDir, topic, since, limit)
			if err != nil {
				return fmt.Errorf("decisions list: %w", err)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "leonard: no decisions recorded")
				return nil
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "leonard: %d decision(s)\n", len(rows))
			for _, r := range rows {
				marker := ""
				if r.SupersededBy != nil {
					marker = fmt.Sprintf("  [superseded→#%d]", *r.SupersededBy)
				}
				fmt.Fprintf(out, "  #%d  %s  %s → %s%s\n", r.ID, formatUnixTime(r.RecordedAt), r.Topic, r.Choice, marker)
				if reason := firstLine(r.Reasoning); reason != "" {
					fmt.Fprintf(out, "      %s\n", reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "exact-match topic filter")
	cmd.Flags().Int64Var(&since, "since", 0, "unix-seconds lower bound on recorded_at")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of rows returned (0 = use default, currently 50)")
	return cmd
}

func newDecisionsAddCmd(rt Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <topic> <choice> <reasoning...>",
		Short: "Record a new decision.",
		Long:  "Three positional args: topic, choice, reasoning. The reasoning may be multiple words (everything after the choice is joined with spaces). Outputs the new decision_id on success.",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			topic := args[0]
			choice := args[1]
			reasoning := strings.Join(args[2:], " ")
			id, err := rt.RecordDecision(cmd.Context(), dataDir, topic, choice, reasoning)
			if err != nil {
				return fmt.Errorf("decisions add: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "leonard: recorded decision #%d\n", id)
			return nil
		},
	}
	return cmd
}

func newDecisionsStaleCmd(rt Runtime) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "stale",
		Short: "Show decisions whose related files or symbols no longer exist in the index.",
		Long:  "Cross-checks each decision's related_files / related_symbols against the current symbol index. A non-empty result means a decision is reasoning about something that's been deleted or renamed — worth a manual supersede or revisit.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			rows, err := rt.GetStaleDecisions(cmd.Context(), dataDir, limit)
			if err != nil {
				return fmt.Errorf("decisions stale: %w", err)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "leonard: no stale decisions")
				return nil
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "leonard: %d stale decision(s)\n", len(rows))
			for _, r := range rows {
				fmt.Fprintf(out, "  #%d  %s → %s\n", r.Decision.ID, r.Decision.Topic, r.Decision.Choice)
				if len(r.MissingFiles) > 0 {
					fmt.Fprintf(out, "      missing files: %s\n", strings.Join(r.MissingFiles, ", "))
				}
				if len(r.MissingSymbols) > 0 {
					fmt.Fprintf(out, "      missing symbols: %s\n", strings.Join(r.MissingSymbols, ", "))
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of rows returned (0 = use default, currently 200)")
	return cmd
}

// dataDirForCwd resolves the project's `.leonard/` directory under the
// current working directory OR any ancestor and verifies it exists.
// Returns a clear "run leonard init first" error when no ancestor has
// the dir.
//
// Bughunt-2 cli F9 (carry-over): previously this only checked cwd, so
// `leonard verify Foo` from a subdir of an initialized project failed
// with "run leonard init first" even though .leonard/ existed in an
// ancestor. Mirrors leonard-hook's resolveProjectRoot walk-up.
func dataDirForCwd() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		dataDir := filepath.Join(dir, dataDirName)
		if _, err := os.Stat(dataDir); err == nil {
			return dataDir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s in %s or any ancestor — run `leonard init` first", dataDirName, cwd)
		}
		dir = parent
	}
}

// formatUnixTime is the timestamp format shared by decisions/claims listings.
// RFC3339 keeps it sortable and unambiguous (timezone explicit). A zero
// timestamp returns "" — happens for fixture rows the test fakes leave unset.
func formatUnixTime(t int64) string {
	if t == 0 {
		return ""
	}
	return time.Unix(t, 0).Format(time.RFC3339)
}

// firstLine returns the leading non-empty line of s, trimmed. Used so a
// multi-paragraph reasoning entry fits on one terminal line in `decisions
// list` output; the full text is still in the DB for MCP consumers.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		if l != "" {
			return l
		}
	}
	return ""
}
