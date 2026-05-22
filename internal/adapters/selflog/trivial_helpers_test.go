package selflog_test

import (
	"crypto/sha256"
	"encoding/hex"
)

// computeSha256Prefix32 mirrors the filename derivation used by both
// cmd/leonard/truth_edit.go and internal/adapters/selflog/trivial.go.
// Kept in a tiny helper file so the main test file's imports stay
// focused on test semantics rather than crypto details.
func computeSha256Prefix32(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}
