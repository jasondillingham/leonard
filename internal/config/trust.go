// Package config — trust-gate for [post_edit.verify].command.
//
// Security review #4 / bughunt-8 established that lexical scanning
// of Bash commands for `.leonard/` is unbounded (variable
// indirection, command substitution, parameter expansion, etc.
// defeat any enumeration). The v0.51 trust-gate moved authorization
// to the operator: a SHA-256 fingerprint of the verifier command
// is stored when the operator runs `leonard config trust`. The
// post-edit hook recomputes the fingerprint and refuses to invoke
// `sh -c` unless it matches.
//
// Bughunt-9 iteration-3 (2 CRIT): the v0.51 implementation stored
// the fingerprint at `.leonard/trusted-verifier.sha256`. The
// attacker who can write `.leonard/config.toml` (via Bash
// obfuscation) can also write the trust file — fingerprint match
// passes, exploit succeeds.
//
// v0.52 fix: relocate the trust file OUT OF the project tree
// entirely. The fingerprint now lives at
// `$XDG_CONFIG_HOME/leonard/trust/<project-hash>.sha256` (default
// `~/.config/leonard/trust/...`). The `.leonard/` bash-scanner
// gaps can no longer poison it because the attacker can't write
// to the user's home dir from a Claude Code session-scoped tool
// without explicit auth. The trust file's parent dir is created
// with mode 0o700.
//
// Project-hash = SHA-256 of the absolute project-root path. So
// trust is per-project, attached by canonical path. Moving the
// project (mv ./foo ./bar) invalidates trust — the operator must
// re-run `leonard config trust`, which is the correct behavior
// (you wouldn't want trust to follow a rename you didn't intend).
//
// v0.52 also adds os.Lstat-based symlink refusal as defense in
// depth: if the trust file path resolves through a symlink, we
// refuse to read it (closes the bughunt-9 F2 vector).
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

// FingerprintCommand returns the canonical sha256 hex digest of a
// verifier command. Leading/trailing whitespace is trimmed so a
// minor edit (extra newline) doesn't invalidate trust; internal
// whitespace IS significant (operator can't trust `cargo check`
// then have `cargo check --release` slip in).
func FingerprintCommand(command string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

// TrustFilePath returns the absolute path of the trust fingerprint
// file for the project rooted at projectRoot. The path lives under
// the user's config dir, deterministic from the project's
// canonical absolute path.
//
// Returns an error only if the home/config dir can't be resolved
// (rare; happens in unusual cron contexts where $HOME is unset).
func TrustFilePath(projectRoot string) (string, error) {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("trust: resolve project root: %w", err)
	}
	hash := sha256.Sum256([]byte(abs))
	projectHash := hex.EncodeToString(hash[:])

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("trust: resolve user config dir: %w", err)
	}
	return filepath.Join(cfgDir, "leonard", "trust", projectHash+".sha256"), nil
}

// ReadTrustedFingerprint reads the operator-granted trust
// fingerprint for the project at projectRoot. Returns ("", nil)
// when no trust has been granted. Refuses to follow symlinks —
// bughunt-9 F2 (CRITICAL): an attacker who can plant a symlink at
// the trust path could redirect reads to an arbitrary file with a
// fingerprint matching their malicious command. We Lstat first
// and bail if the entry is a symlink.
func ReadTrustedFingerprint(projectRoot string) (string, error) {
	path, err := TrustFilePath(projectRoot)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("trust: lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("trust: %s is a symlink; refusing to follow", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("trust: read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteTrustedFingerprint writes the operator-granted trust
// fingerprint. Called by `leonard config trust`. Creates parent
// dirs with mode 0o700 and writes the file with 0o600 so other
// users on a multi-user machine can't observe or tamper.
//
// Refuses to overwrite an existing symlink at the target path
// (bughunt-9 F2 defense in depth).
func WriteTrustedFingerprint(projectRoot, fingerprint string) error {
	path, err := TrustFilePath(projectRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("trust: mkdir %s: %w", filepath.Dir(path), err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trust: %s is a symlink; refusing to overwrite — delete it first", path)
	}
	return os.WriteFile(path, []byte(fingerprint+"\n"), 0o600)
}

// VerifyCommandTrusted reports whether `command` matches the
// stored trust fingerprint for projectRoot. Returns:
//
//   - (true, nil)  — trust granted; command may execute
//   - (false, nil) — trust missing or fingerprint mismatch
//   - (false, err) — symlink or I/O failure reading the trust file
//
// Empty command always returns (false, nil) since there's nothing
// to authorize.
func VerifyCommandTrusted(projectRoot, command string) (bool, error) {
	if strings.TrimSpace(command) == "" {
		return false, nil
	}
	stored, err := ReadTrustedFingerprint(projectRoot)
	if err != nil {
		return false, err
	}
	if stored == "" {
		return false, nil
	}
	return stored == FingerprintCommand(command), nil
}
