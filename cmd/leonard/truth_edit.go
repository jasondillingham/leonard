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

// pendingTrivialDirName is the subdirectory under .leonard/ where
// `leonard truth-edit --trivial` drops single-use bypass tokens.
// Selflog's PreEdit (#25) consumes a matching token to allow a
// require-tier edit through and logs the trivial entry to
// pending-decisions.log.
const pendingTrivialDirName = "pending-trivial"

// trivialTokenTTL caps how long a token stays valid after creation.
// 5 minutes is generous enough for an operator to issue the CLI
// command then make the edit in their hook-driven session; short
// enough that a forgotten token doesn't grant indefinite bypass.
const trivialTokenTTL = 5 * time.Minute

// TrivialToken is the on-disk JSON shape of a single-use bypass.
// Field names mirror store.TruthChange's Trivial* fields so a
// later "promote to decision-log" step (#28) is a one-step copy.
type TrivialToken struct {
	FilePath      string `json:"file_path"`
	TrivialReason string `json:"trivial_reason"`
	Timestamp     string `json:"ts"`
}

func newTruthEditCmd() *cobra.Command {
	var trivialReason string
	cmd := &cobra.Command{
		Use:   "truth-edit [path]",
		Short: "Mark an edit to a truth-source file (currently --trivial only).",
		Long: `Records intent to edit a truth-source file. v0.7 ships --trivial
which lets a require-tier edit (e.g., .leonard/ground-truth/do-not-claim.md
or internal/adapters/*.go) bypass the rationale-required gate when the
change is a typo fix, whitespace, or other not-load-bearing tweak.

Without --trivial the command is a no-op stub (full rationale capture
lands in #28).

Token semantics:
- Token is single-use: the next matching PreEdit consumes it and
  deletes the file.
- Token expires after 5 minutes if unused, so a forgotten command
  doesn't grant indefinite bypass.
- Token's trivial_reason is logged to .leonard/pending-decisions.log
  when consumed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rel := args[0]
			if trivialReason == "" {
				return errors.New("truth-edit: --trivial requires a non-empty reason")
			}
			dataDir, err := dataDirForCwd()
			if err != nil {
				return err
			}
			projectRoot := filepath.Dir(dataDir)

			// Refuse absolute paths. Trivial-bypass authority is
			// strictly per-project — accepting an absolute path
			// outside the project would let an operator suppress
			// a guard on someone else's tree.
			if filepath.IsAbs(rel) {
				return fmt.Errorf("truth-edit: path must be relative to project root, got absolute %q", rel)
			}
			// Reject "../" escapes. We don't want a token written
			// for a path outside the project tree.
			cleanRel := filepath.Clean(rel)
			if strings.HasPrefix(cleanRel, "..") || strings.Contains(cleanRel, "/../") {
				return fmt.Errorf("truth-edit: path escapes project root: %q", rel)
			}

			tokenDir := filepath.Join(dataDir, pendingTrivialDirName)
			if err := os.MkdirAll(tokenDir, 0o755); err != nil {
				return fmt.Errorf("truth-edit: mkdir %s: %w", tokenDir, err)
			}
			tokenPath := filepath.Join(tokenDir, trivialTokenFilename(cleanRel))

			tok := TrivialToken{
				FilePath:      cleanRel,
				TrivialReason: trivialReason,
				Timestamp:     time.Now().UTC().Format(time.RFC3339),
			}
			body, err := json.Marshal(tok)
			if err != nil {
				return fmt.Errorf("truth-edit: marshal token: %w", err)
			}
			if err := os.WriteFile(tokenPath, body, 0o600); err != nil {
				return fmt.Errorf("truth-edit: write token %s: %w", tokenPath, err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "leonard: trivial bypass recorded for %s\n", cleanRel)
			fmt.Fprintf(out, "         reason: %s\n", trivialReason)
			fmt.Fprintf(out, "         token expires in %s; consumed on next matching edit.\n", trivialTokenTTL)
			_ = projectRoot
			return nil
		},
	}
	cmd.Flags().StringVar(&trivialReason, "trivial", "", "mark the edit trivial (typo, whitespace) with a short reason; bypasses require-tier rationale gate")
	return cmd
}

// trivialTokenFilename returns the per-path filename for a trivial
// bypass token. SHA-256 of the relative path so paths with slashes
// or unusual characters land cleanly in a flat directory. Suffix
// .json keeps the file content-type obvious.
func trivialTokenFilename(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:])[:32] + ".json"
}
