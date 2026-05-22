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
// trustTargetVerifier and trustTargetGroundTruth name the two
// trust targets supported by `leonard config trust`. New targets
// land alongside these as additional cases in the switch below.
const (
	trustTargetVerifier    = "verifier"
	trustTargetGroundTruth = "ground-truth"
	trustTargetSync        = "sync"
)

func newConfigTrustCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "trust [target] [args]",
		Short: "Authorize a blocking action — verifier command, ground-truth adapter, or sync plugin.",
		Long: `Grants operator trust for blocking actions.

Targets:
  verifier         Authorize [post_edit.verify].command (default for backwards
                   compat — empty target means verifier). Hashes the command
                   with SHA-256 and stores the fingerprint at
                   $XDG_CONFIG_HOME/leonard/trust/<hash>.sha256.

  ground-truth     Authorize the ground-truth adapter to reject edits that
                   introduce forbidden claims. Writes a marker file at
                   $XDG_CONFIG_HOME/leonard/trust/<hash>.ground-truth.trust
                   — until the marker exists, the adapter logs warnings
                   instead of denying.

  sync <name>      Authorize a sync plugin's command path. Hashes the
                   .leonard/config.toml [sync.<name>].command with SHA-256
                   and stores the fingerprint at
                   $XDG_CONFIG_HOME/leonard/trust/<hash>.sync-<name>.sha256.
                   leonard sync refuses to invoke the plugin until the
                   fingerprint matches (bughunt-11 F3).

Trust is per-project, attached to the canonical absolute path of the
project root. Files live OUTSIDE the project tree so .leonard/ write
attacks can't poison them.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := trustTargetVerifier
			if len(args) >= 1 {
				target = args[0]
			}
			switch target {
			case trustTargetVerifier:
				return runTrustVerifier(cmd, yes)
			case trustTargetGroundTruth:
				return runTrustGroundTruth(cmd, yes)
			case trustTargetSync:
				if len(args) < 2 {
					return fmt.Errorf("config trust sync: plugin name required (e.g., `leonard config trust sync github`)")
				}
				return runTrustSync(cmd, args[1], yes)
			default:
				return fmt.Errorf("config trust: unknown target %q (expected one of: verifier, ground-truth, sync)", target)
			}
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip interactive confirmation (intended for scripted setup)")
	return cmd
}

// runTrustSync authorizes a sync plugin's command path. Reads the
// configured plugin from .leonard/config.toml's [sync.<name>] block,
// shows the resolved command to the operator, and (on confirmation)
// stores the SHA-256 fingerprint. The plugin runner refuses to exec
// until the fingerprint matches.
//
// Same posture as runTrustVerifier: the trust file lives outside
// the project tree so .leonard/ write attacks can't poison it.
func runTrustSync(cmd *cobra.Command, name string, yes bool) error {
	dataDir, err := dataDirForCwd()
	if err != nil {
		return err
	}
	projectRoot := filepath.Dir(dataDir)
	cfg, err := loadSyncConfig(dataDir)
	if err != nil {
		return fmt.Errorf("config trust sync: %w", err)
	}
	pc, ok := cfg.Sync[name]
	if !ok {
		return fmt.Errorf("config trust sync: no plugin named %q in .leonard/config.toml (configured: %s)",
			name, strings.Join(syncNames(cfg), ", "))
	}
	if strings.TrimSpace(pc.Command) == "" {
		return fmt.Errorf("config trust sync: plugin %q has empty command; nothing to authorize", name)
	}
	trustPath, err := config.SyncPluginTrustPath(projectRoot, name)
	if err != nil {
		return fmt.Errorf("config trust sync: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "leonard: about to trust sync plugin %q with command:\n\n", name)
	fmt.Fprintln(out, "    "+pc.Command)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "When you run `leonard sync` (or `leonard sync "+name+"`), this binary will execute.")
	fmt.Fprintln(out, "Fingerprint will be stored at "+trustPath)
	fmt.Fprintln(out)

	if err := confirmTrust(cmd, yes); err != nil {
		return err
	}

	if err := config.WriteSyncPluginTrust(projectRoot, name, pc.Command); err != nil {
		return fmt.Errorf("config trust sync: write trust file: %w", err)
	}
	fp := config.FingerprintCommand(pc.Command)
	fmt.Fprintf(out, "leonard: sync plugin %q authorized (fingerprint %s…).\n", name, fp[:12])
	return nil
}

// runTrustVerifier authorizes [post_edit.verify].command. Extracted
// from the original newConfigTrustCmd body so the new top-level
// trust command can dispatch on target without nesting case-bodies.
func runTrustVerifier(cmd *cobra.Command, yes bool) error {
	dataDir, err := dataDirForCwd()
	if err != nil {
		return err
	}
	projectRoot := filepath.Dir(dataDir)
	cfgPath := filepath.Join(dataDir, config.Filename)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config trust: read %s: %w", cfgPath, err)
	}
	command := strings.TrimSpace(cfg.PostEdit.Verify.Command)
	if command == "" {
		return fmt.Errorf("config trust: [post_edit.verify].command is empty in %s; nothing to authorize", cfgPath)
	}
	trustPath, err := config.TrustFilePath(projectRoot)
	if err != nil {
		return fmt.Errorf("config trust: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "leonard: about to trust the following [post_edit.verify].command:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "    "+command)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "On every Edit/Write the post-edit hook will run this through `sh -c`.")
	fmt.Fprintln(out, "Fingerprint will be stored at "+trustPath)
	fmt.Fprintln(out)

	if err := confirmTrust(cmd, yes); err != nil {
		return err
	}

	fp := config.FingerprintCommand(command)
	if err := config.WriteTrustedFingerprint(projectRoot, fp); err != nil {
		return fmt.Errorf("config trust: write trust file: %w", err)
	}
	fmt.Fprintf(out, "leonard: verifier command authorized (fingerprint %s…).\n", fp[:12])
	return nil
}

// runTrustGroundTruth grants the ground-truth adapter authority to
// reject edits. Mirrors runTrustVerifier's confirmation flow but
// writes the per-adapter marker file (no command fingerprint —
// adapter trust is binary).
func runTrustGroundTruth(cmd *cobra.Command, yes bool) error {
	dataDir, err := dataDirForCwd()
	if err != nil {
		return err
	}
	projectRoot := filepath.Dir(dataDir)

	trustPath, err := config.AdapterTrustFilePath(projectRoot, trustTargetGroundTruth)
	if err != nil {
		return fmt.Errorf("config trust: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "leonard: about to grant the ground-truth adapter authority to reject edits.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Once trusted, the pre-edit hook will hard-reject Edit/Write/MultiEdit")
	fmt.Fprintln(out, "operations whose content matches any rule in .leonard/ground-truth/do-not-claim.md.")
	fmt.Fprintln(out, "Untrusted: the adapter logs warnings but does not block.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Trust marker will be stored at "+trustPath)
	fmt.Fprintln(out)

	if err := confirmTrust(cmd, yes); err != nil {
		return err
	}

	if err := config.WriteAdapterTrust(projectRoot, trustTargetGroundTruth); err != nil {
		return fmt.Errorf("config trust: write trust file: %w", err)
	}
	fmt.Fprintln(out, "leonard: ground-truth adapter authorized to reject edits.")
	return nil
}

// confirmTrust handles the "yes/no" prompt shared by the verifier
// and ground-truth flows. Honors --yes for scripted setup.
func confirmTrust(cmd *cobra.Command, yes bool) error {
	if yes {
		return nil
	}
	out := cmd.OutOrStdout()
	fmt.Fprint(out, "Type 'yes' to confirm: ")
	var response string
	if _, err := fmt.Fscanln(cmd.InOrStdin(), &response); err != nil {
		return fmt.Errorf("config trust: aborted: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(response)) != "yes" {
		return fmt.Errorf("config trust: aborted (must type 'yes' verbatim)")
	}
	return nil
}
