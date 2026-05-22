package groundtruth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// overrideTestTokenName mirrors override.go's overrideTokenFilename.
// Reimplemented in the test file so we don't reach into the
// adapter's unexported helpers.
func overrideTestTokenName(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:])[:32] + ".json"
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
