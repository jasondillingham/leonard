package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// newClaimsCmd is the `leonard claims ...` group. Currently one subcommand,
// `unverified`, mirroring the MCP get_unverified_claims tool — useful for
// spot-checking the ledger from the terminal without spinning up Claude.
func newClaimsCmd(rt Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "Inspect the claim ledger.",
		Long:  "Mirrors get_unverified_claims from the MCP tool surface. The Stop hook surfaces these at session end; this command is for inspection from the terminal.",
	}
	cmd.AddCommand(newClaimsUnverifiedCmd(rt))
	cmd.AddCommand(newClaimsResolveCmd(rt))
	return cmd
}

// newClaimsResolveCmd implements `leonard claims resolve <id> [--note "..."]`.
// Marks a single claim as manually resolved by the operator — sets
// verified=1 and appends a "manually resolved by operator" note to
// the evidence so the audit trail records why this row stopped
// surfacing in Stop.
//
// v0.38 escape hatch: most stale claims get cleaned up automatically
// by SupersedeOutstandingFailures on the next vet=ok run; this is
// for claims that survive that net (e.g. a verifier-failure on a
// file you've decided is intentionally broken pending a refactor).
func newClaimsResolveCmd(rt Runtime) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "resolve <id>",
		Short: "Mark a single claim as manually resolved.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("claims resolve: invalid id %q: %w", args[0], err)
			}
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			if err := rt.ResolveClaim(cmd.Context(), dataDir, id, note); err != nil {
				return fmt.Errorf("claims resolve: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "leonard: claim #%d resolved\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "optional audit note appended to the claim's evidence")
	return cmd
}

func newClaimsUnverifiedCmd(rt Runtime) *cobra.Command {
	var sessionID string
	cmd := &cobra.Command{
		Use:   "unverified",
		Short: "List unverified claims (vet failure or explicit verified=false).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			rows, err := rt.GetUnverifiedClaims(cmd.Context(), dataDir, sessionID)
			if err != nil {
				return fmt.Errorf("claims unverified: %w", err)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "leonard: no unverified claims")
				return nil
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "leonard: %d unverified claim(s)\n", len(rows))
			for _, r := range rows {
				fmt.Fprintf(out, "  #%d  %s  %s\n", r.ID, formatUnixTime(r.RecordedAt), r.Claim)
				if r.FilePath != "" {
					fmt.Fprintf(out, "      file: %s\n", r.FilePath)
				}
				if r.SessionID != "" {
					fmt.Fprintf(out, "      session: %s\n", r.SessionID)
				}
				if reason := firstLine(r.Evidence); reason != "" {
					fmt.Fprintf(out, "      %s\n", reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "filter to a single Claude Code session id (empty = all sessions)")
	return cmd
}
