package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newIndexCmd(rt Runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "index",
		Short: "Walk the project and refresh the symbol index.",
		Long:  "Walks cwd, dispatches each tracked file to its parser, and replaces symbol rows for files whose hash changed since the last run.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(cwd, dataDirName)
			if _, err := os.Stat(dataDir); err != nil {
				return fmt.Errorf("no %s here — run `leonard init` first", dataDirName)
			}
			n, err := rt.IndexAll(cmd.Context(), cwd, dataDir)
			if err != nil {
				return fmt.Errorf("index: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "leonard: indexed %d file(s)\n", n)
			return nil
		},
	}
}
