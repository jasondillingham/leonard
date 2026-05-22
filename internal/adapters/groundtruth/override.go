package groundtruth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/jasondillingham/leonard/internal/config"
)

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
// Misses, expirations, corrupt tokens, or symlinks return false.
//
// Token path: $XDG_CONFIG_HOME/leonard/pending-override/<projHash>.<relHash>.json
//
// v1.0 (bughunt-11 F1): relocated OUT of .leonard/ because the
// bash-obfuscation attack class can plant a forged token there.
// Same reasoning as bughunt-9 moving the verifier trust file.
//
// v1.0 (bughunt-11 F2): refuses symlinked tokens so an attacker
// who plants a symlink at the canonical token path can't redirect
// the read to attacker-controlled content.
func consumeOverrideToken(projectRoot, rel string) bool {
	if projectRoot == "" || rel == "" {
		return false
	}
	tokenPath, err := config.PendingTokenPath("override", projectRoot, rel)
	if err != nil {
		return false
	}

	// bughunt-11 F2: refuse symlinks before reading.
	info, err := os.Lstat(tokenPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}

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
