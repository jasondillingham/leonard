package main

import (
	"fmt"

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
