package groundtruth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// pendingOverrideDirName is the subdirectory under .leonard/ where
// `leonard override --once` (#17) drops single-use bypass tokens
// scoped to a file path. Mirrors the trivial-bypass mechanism but
// targets path/content filter rules rather than self-logging
// require-tier blocks.
const pendingOverrideDirName = "pending-override"

// overrideTokenTTL caps how long an override is honored after the
// CLI wrote it. Same value as the trivial-bypass TTL for operator
// muscle-memory: "I issue the command, then make the edit within
// 5 minutes."
const overrideTokenTTL = 5 * time.Minute

// overrideToken is the on-disk JSON shape of a single-use bypass.
// Field names match cmd/leonard/override.go's encoding side.
type overrideToken struct {
	FilePath  string `json:"file_path"`
	Reason    string `json:"reason"`
	Timestamp string `json:"ts"`
}

// consumeOverrideToken looks for a token matching rel. On hit,
// validates the path + timestamp, deletes the file (single-use),
// and returns true so the filter guard short-circuits to Pass.
// Misses, expirations, and corrupt tokens all return false; expired
// or malformed tokens are deleted so they don't accumulate.
func consumeOverrideToken(projectRoot, rel string) bool {
	if projectRoot == "" || rel == "" {
		return false
	}
	tokenPath := filepath.Join(projectRoot, ".leonard", pendingOverrideDirName, overrideTokenFilename(rel))

	data, err := os.ReadFile(tokenPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return false
	}

	var tok overrideToken
	if err := json.Unmarshal(data, &tok); err != nil {
		_ = os.Remove(tokenPath)
		return false
	}
	if tok.FilePath != rel {
		_ = os.Remove(tokenPath)
		return false
	}
	ts, err := time.Parse(time.RFC3339, tok.Timestamp)
	if err != nil {
		_ = os.Remove(tokenPath)
		return false
	}
	if time.Since(ts) > overrideTokenTTL {
		_ = os.Remove(tokenPath)
		return false
	}

	_ = os.Remove(tokenPath)
	return true
}

// overrideTokenFilename is the per-path filename. Same shape as the
// trivial-bypass token name (sha256(rel)[:32] + .json) so operators
// who know the trivial format don't need to learn a second one.
func overrideTokenFilename(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:])[:32] + ".json"
}

