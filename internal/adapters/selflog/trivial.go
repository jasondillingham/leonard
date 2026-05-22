package selflog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
)

// trivialTokenTTL caps how long a token is honored after the CLI
// wrote it. Matches cmd/leonard's value. Tokens older than the TTL
// are deleted on read so they don't accumulate.
const trivialTokenTTL = 5 * time.Minute

// trivialToken is the on-disk shape — must match the CLI's
// TrivialToken type. Decoded by consumeTrivialToken.
type trivialToken struct {
	FilePath      string `json:"file_path"`
	TrivialReason string `json:"trivial_reason"`
	Timestamp     string `json:"ts"`
}

// consumeTrivialToken looks for a per-path token written by
// `leonard truth-edit --trivial`. On hit: validates the file_path
// matches, checks the token age, deletes the file (single-use), and
// returns the decoded token. On miss / expiry / mismatch / symlink:
// returns ok=false.
//
// Token path: $XDG_CONFIG_HOME/leonard/pending-trivial/<projHash>.<relHash>.json
//
// v1.0 (bughunt-11 F1): relocated OUT of .leonard/ because the
// bash-obfuscation attack class can plant a forged token there.
// Same reasoning as bughunt-9 moving the verifier trust file.
//
// v1.0 (bughunt-11 F2): refuses symlinked tokens so an attacker
// who plants a symlink at the canonical token path can't redirect
// the read to attacker-controlled content.
func consumeTrivialToken(projectRoot, rel string) (trivialToken, bool) {
	if projectRoot == "" || rel == "" {
		return trivialToken{}, false
	}
	tokenPath, err := config.PendingTokenPath("trivial", projectRoot, rel)
	if err != nil {
		return trivialToken{}, false
	}

	// bughunt-11 F2: refuse symlinks before reading.
	info, err := os.Lstat(tokenPath)
	if errors.Is(err, fs.ErrNotExist) {
		return trivialToken{}, false
	}
	if err != nil {
		return trivialToken{}, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Don't auto-delete — operator visibility on the planted
		// symlink is more useful than silent cleanup.
		return trivialToken{}, false
	}

	data, err := os.ReadFile(tokenPath)
	if errors.Is(err, fs.ErrNotExist) {
		return trivialToken{}, false
	}
	if err != nil {
		return trivialToken{}, false
	}

	var tok trivialToken
	if err := json.Unmarshal(data, &tok); err != nil {
		// Malformed token. Delete it (single-use semantics) and
		// treat as miss; operator must re-issue the CLI command.
		_ = os.Remove(tokenPath)
		return trivialToken{}, false
	}

	// Verify the path stored in the token matches what we were
	// asked to authorize. Belt-and-suspenders against a hash
	// collision (vanishingly unlikely with sha256, but cheap to
	// check).
	if tok.FilePath != rel {
		_ = os.Remove(tokenPath)
		return trivialToken{}, false
	}

	ts, err := time.Parse(time.RFC3339, tok.Timestamp)
	if err != nil {
		_ = os.Remove(tokenPath)
		return trivialToken{}, false
	}
	if time.Since(ts) > trivialTokenTTL {
		_ = os.Remove(tokenPath)
		return trivialToken{}, false
	}

	// Consume — delete the token before returning so a repeat
	// invocation must re-grant.
	_ = os.Remove(tokenPath)
	return tok, true
}

// appendTrivialDraftEntry writes a draft entry to
// .leonard/pending-decisions.log with the trivial flag + reason so
// `leonard truth-history` (#28) can render the bypass alongside
// non-trivial entries. Reuses appendPendingDecision so the on-disk
// shape stays consistent across draft and trivial entries.
func appendTrivialDraftEntry(projectRoot string, p adapters.PreEditPayload, rel string, tok trivialToken) error {
	entry := pendingDecisionEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		SessionID: p.SessionID,
		Tool:      p.Tool,
		FilePath:  rel,
		Scope:     string(ScopeDomain), // override below if the rule fired toolkit
		Tier:      string(TierRequire),
		Draft: pendingDecisionDraft{
			MotivatedBy: fmt.Sprintf("Trivial bypass: %s", tok.TrivialReason),
		},
	}
	if scope, _, ok := classify(rel); ok {
		entry.Scope = string(scope)
	}
	return appendPendingDecision(projectRoot, entry)
}
