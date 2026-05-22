package groundtruth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// AuditLogPresent reports whether the audit log exists. v0.6 doesn't
// parse audit-log.md entries — it's an append-only ledger that the
// post-edit hook writes to in #29. Init only checks that the file is
// readable when present, so an unreadable audit log (permissions)
// surfaces at startup rather than at first write.
func auditLogPresent(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	// Open-and-close to verify readability without holding a handle.
	f, err := os.Open(path)
	if err != nil {
		return true, fmt.Errorf("open %s: %w", path, err)
	}
	_ = f.Close()
	return true, nil
}
