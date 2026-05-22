package selflog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// pendingTrivialDirName mirrors cmd/leonard/truth_edit.go's
// constant. Kept duplicated rather than imported because cmd/* and
// internal/* shouldn't cross-import — the format is part of the
// on-disk contract between the two.
const pendingTrivialDirName = "pending-trivial"

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
// returns the decoded token. On miss / expiry / mismatch: returns
// ok=false.
//
// Token path: <projectRoot>/.leonard/pending-trivial/<sha256(rel)>.json
// The 32-char hex prefix keeps the filename short while staying
// unique enough for the v0.7 use case (no two project paths collide).
func consumeTrivialToken(projectRoot, rel string) (trivialToken, bool) {
	if projectRoot == "" || rel == "" {
		return trivialToken{}, false
	}
	tokenPath := filepath.Join(projectRoot, ".leonard", pendingTrivialDirName, tokenFilename(rel))

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

// tokenFilename produces the same per-path name the CLI writes.
// Keep this in sync with cmd/leonard/truth_edit.go.
func tokenFilename(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:])[:32] + ".json"
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
