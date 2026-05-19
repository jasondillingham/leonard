package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newVerifyCmd(rt Runtime) *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "verify <name>",
		Short: "Look up a symbol by name (CLI mirror of MCP verify_symbol).",
		Long:  "Reports whether the named symbol exists in the project index. Exit code 0 = found, 1 = not found, 2 = error.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(cwd, dataDirName)
			if _, err := os.Stat(dataDir); err != nil {
				return fmt.Errorf("no %s here — run `leonard init` first", dataDirName)
			}
			matches, err := rt.VerifySymbol(cmd.Context(), dataDir, args[0], kind)
			if err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			if len(matches) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "leonard: no match for %q\n", args[0])
				return &exitCode{code: 1, msg: ""}
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "leonard: %d match(es) for %q\n", len(matches), args[0])
			for _, m := range matches {
				fmt.Fprintf(out, "  %s:%d  %s  %s  %s\n", m.File, m.Line, m.Kind, m.QualifiedName, m.Signature)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (function|method|type|const|var|interface)")
	return cmd
}

// exitCode lets a RunE function return a non-zero status without printing an
// error message. The main() entry point checks for this type.
type exitCode struct {
	code int
	msg  string
}

func (e *exitCode) Error() string { return e.msg }
