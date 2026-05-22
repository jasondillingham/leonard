package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// pendingOverrideDirName mirrors internal/adapters/groundtruth's
// constant — the directory where single-use bypass tokens live.
// Kept duplicated (rather than imported) because cmd/* and
// internal/* shouldn't cross-import; the format is the on-disk
// contract.
const pendingOverrideDirName = "pending-override"

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
		Long: `Records a single-use override token at .leonard/pending-override/<hash>.json
that bypasses path_filters / content_filters for the next matching edit.

v0.8 ships --once + --reason as required flags. Other shapes (e.g.,
--for=<duration>, --session-scoped) are forthcoming as the use cases
emerge.

Token semantics:
- Single-use: consumed by the next matching PreEdit and deleted.
- Expires after 5 minutes if unused.
- Reason is recorded and surfaced in audit logs (pending-decisions.log
  on consumption, then promoted to the decisions DB in #28).`,
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
			if filepath.IsAbs(rel) {
				return fmt.Errorf("override: path must be relative to project root, got absolute %q", rel)
			}
			cleanRel := filepath.Clean(rel)
			if strings.HasPrefix(cleanRel, "..") || strings.Contains(cleanRel, "/../") {
				return fmt.Errorf("override: path escapes project root: %q", rel)
			}

			tokenDir := filepath.Join(dataDir, pendingOverrideDirName)
			if err := os.MkdirAll(tokenDir, 0o755); err != nil {
				return fmt.Errorf("override: mkdir %s: %w", tokenDir, err)
			}
			tokenPath := filepath.Join(tokenDir, overrideTokenFilename(cleanRel))

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

// overrideTokenFilename matches the adapter side. Keep both in sync.
func overrideTokenFilename(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:])[:32] + ".json"
}
