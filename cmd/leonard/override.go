package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/leonard/internal/config"
)

// overrideTokenTTL is informational only on the CLI side — the
// adapter enforces it on read. Kept here so the operator gets a
// useful "expires in 5m0s" message.
const overrideTokenTTL = 5 * time.Minute

// OverrideToken is the on-disk JSON shape. Must match the adapter
// side (internal/adapters/groundtruth/override.go's overrideToken).
type OverrideToken struct {
	FilePath  string `json:"file_path"`
	Reason    string `json:"reason"`
	Timestamp string `json:"ts"`
}

func newOverrideCmd() *cobra.Command {
	var (
		once   bool
		reason string
	)
	cmd := &cobra.Command{
		Use:   "override [path]",
		Short: "Grant a single-use bypass of ground-truth filter rules.",
		Long: `Records a single-use override token that bypasses path_filters /
content_filters for the next matching edit.

v0.8 ships --once + --reason as required flags. Other shapes (e.g.,
--for=<duration>, --session-scoped) are forthcoming as the use cases
emerge.

Token semantics:
- Single-use: consumed by the next matching PreEdit and deleted.
- Expires after 5 minutes if unused.
- Reason is recorded and surfaced in audit logs (pending-decisions.log
  on consumption, then promoted to the decisions DB in #28).
- v1.0 (bughunt-11 F1): token lives at $XDG_CONFIG_HOME/leonard/
  pending-override/<projHash>.<relHash>.json, NOT under .leonard/.
  Relocated out of the project tree so the bash-obfuscation
  attack class can't plant a forged token.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rel := args[0]
			if !once {
				return errors.New("override: --once is required in v0.8 (other forms are forthcoming)")
			}
			if strings.TrimSpace(reason) == "" {
				return errors.New("override: --reason is required and must be non-empty")
			}
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)
			if filepath.IsAbs(rel) {
				return fmt.Errorf("override: path must be relative to project root, got absolute %q", rel)
			}
			cleanRel := filepath.Clean(rel)
			if strings.HasPrefix(cleanRel, "..") || strings.Contains(cleanRel, "/../") {
				return fmt.Errorf("override: path escapes project root: %q", rel)
			}

			// bughunt-11 F1: token at XDG_CONFIG_HOME, not .leonard/.
			tokenPath, err := config.PendingTokenPath("override", projectRoot, cleanRel)
			if err != nil {
				return fmt.Errorf("override: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
				return fmt.Errorf("override: mkdir %s: %w", filepath.Dir(tokenPath), err)
			}

			// bughunt-11 F2 defense in depth: refuse symlink at the
			// canonical token path.
			if info, err := os.Lstat(tokenPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("override: %s is a symlink; refusing to overwrite — delete it first", tokenPath)
			}

			tok := OverrideToken{
				FilePath:  cleanRel,
				Reason:    reason,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			}
			body, err := json.Marshal(tok)
			if err != nil {
				return fmt.Errorf("override: marshal token: %w", err)
			}
			if err := os.WriteFile(tokenPath, body, 0o600); err != nil {
				return fmt.Errorf("override: write token %s: %w", tokenPath, err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "leonard: override token granted for %s\n", cleanRel)
			fmt.Fprintf(out, "         reason: %s\n", reason)
			fmt.Fprintf(out, "         token expires in %s; consumed on next matching edit.\n", overrideTokenTTL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "grant a single-use bypass (required in v0.8)")
	cmd.Flags().StringVar(&reason, "reason", "", "justification recorded with the override (required)")
	return cmd
}
