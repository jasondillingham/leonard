package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/config"
)

// newConfigCmd is the `leonard config ...` group. Currently one
// subcommand: `trust`. Future subcommands could include `show`
// (pretty-print the resolved config) or `validate` (lint the
// schema).
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage Leonard's project-local configuration.",
		Long:  "Currently exposes the `trust` subcommand for authorizing the [post_edit.verify].command (see SECURITY.md).",
	}
	cmd.AddCommand(newConfigTrustCmd())
	return cmd
}

// newConfigTrustCmd implements `leonard config trust`. Reads the
// current `[post_edit.verify].command` from `.leonard/config.toml`,
// prompts the operator (or proceeds with --yes), and stores the
// SHA-256 fingerprint in `.leonard/trusted-verifier.sha256`. The
// post-edit hook checks that fingerprint before invoking `sh -c`.
//
// Security review #4 / bughunt-8: closes the entire Bash-
// obfuscation bypass class by moving authorization from "is the
// command lexically safe?" (impossible to enumerate) to "did the
// operator interactively approve this exact command?".
func newConfigTrustCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Authorize the current [post_edit.verify].command to execute.",
		Long: `Reads the [post_edit.verify].command field from .leonard/config.toml,
shows it to the operator, and (on confirmation) writes its SHA-256
fingerprint to .leonard/trusted-verifier.sha256. The post-edit hook
will refuse to run the verifier until the fingerprint matches.

Trust is per-machine — the fingerprint file is gitignored. A team
that wants shared opt-in can document a make target instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			cfgPath := filepath.Join(dataDir, config.Filename)
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("config trust: read %s: %w", cfgPath, err)
			}
			command := strings.TrimSpace(cfg.PostEdit.Verify.Command)
			if command == "" {
				return fmt.Errorf("config trust: [post_edit.verify].command is empty in %s; nothing to authorize", cfgPath)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "leonard: about to trust the following [post_edit.verify].command:")
			fmt.Fprintln(out)
			fmt.Fprintln(out, "    "+command)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "On every Edit/Write the post-edit hook will run this through `sh -c`.")
			fmt.Fprintln(out, "The fingerprint will be stored at "+filepath.Join(dataDir, config.TrustFilename)+" (gitignored).")
			fmt.Fprintln(out)

			if !yes {
				fmt.Fprint(out, "Type 'yes' to confirm: ")
				var response string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &response); err != nil {
					return fmt.Errorf("config trust: aborted: %w", err)
				}
				if strings.ToLower(strings.TrimSpace(response)) != "yes" {
					return fmt.Errorf("config trust: aborted (must type 'yes' verbatim)")
				}
			}

			fp := config.FingerprintCommand(command)
			if err := config.WriteTrustedFingerprint(dataDir, fp); err != nil {
				return fmt.Errorf("config trust: write trust file: %w", err)
			}
			fmt.Fprintf(out, "leonard: verifier command authorized (fingerprint %s…).\n", fp[:12])
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip interactive confirmation (intended for scripted setup)")
	return cmd
}
