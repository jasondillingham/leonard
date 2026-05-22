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

// PendingTokenPath returns the path where a single-use bypass token
// lives. v1.0 (bughunt-11 F1) relocates these out of .leonard/ for
// the same reason bughunt-9 relocated the verifier trust file:
// .leonard/-write attacks (bash obfuscation, multi-step writes) can
// otherwise plant a forged token.
//
//	kind:        "trivial" (selflog bypass) or "override" (filter bypass)
//	projectRoot: absolute path of the project
//	rel:         project-relative target file path that the token authorizes
//
// File layout:
//
//	$XDG_CONFIG_HOME/leonard/pending-<kind>/<project-hash>.<rel-hash>.json
//
// Both hashes are SHA-256 hex (truncated to 32 chars for filename
// length). The double-hash key prevents a token written for project
// A from being mis-read when reading project B's token directory.
//
// Returns an error on invalid kind so callers can't forge a path
// component via attacker-controlled values.
func PendingTokenPath(kind, projectRoot, rel string) (string, error) {
	if kind != "trivial" && kind != "override" {
		return "", fmt.Errorf("trust: invalid token kind %q (allowed: trivial, override)", kind)
	}
	if projectRoot == "" || rel == "" {
		return "", fmt.Errorf("trust: PendingTokenPath: projectRoot and rel are required")
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("trust: resolve project root: %w", err)
	}
	// Canonicalize through symlinks so /var/... and /private/var/...
	// (the macOS /var symlink + the same temp-folder shape) hash to
	// the same digest. Without this, the CLI writer's hash diverges
	// from the adapter reader's hash when one of them is invoked
	// from a chdir'd path that the OS canonicalizes.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	projectHash := sha256Hex(abs)[:32]
	relHash := sha256Hex(rel)[:32]

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("trust: resolve user config dir: %w", err)
	}
	return filepath.Join(cfgDir, "leonard", "pending-"+kind, projectHash+"."+relHash+".json"), nil
}

// sha256Hex is an internal helper used by PendingTokenPath /
// TrustFilePath / AdapterTrustFilePath. Kept package-private so the
// callers can't smuggle in a non-hex algorithm.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// AdapterTrustFilePath returns the trust file path for an adapter
// scoped to projectRoot. Used by v0.7+ adapters (ground-truth, and
// future blocking adapters) that need a per-adapter trust signal
// distinct from the verifier fingerprint.
//
// The filename is "<project-hash>.<adapter>.trust" so granting
// verifier trust does not accidentally grant adapter trust, and
// each adapter's trust is independently revocable by `rm`'ing the
// matching file.
//
// adapterName is restricted to characters safe in a path component:
// returns an error on anything other than [a-z0-9-]. The check is
// intentionally strict so adapter authors can't pick a name that
// confuses path resolution.
func AdapterTrustFilePath(projectRoot, adapterName string) (string, error) {
	if !validAdapterName(adapterName) {
		return "", fmt.Errorf("trust: invalid adapter name %q (allowed: lowercase letters, digits, hyphens)", adapterName)
	}
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
	return filepath.Join(cfgDir, "leonard", "trust", projectHash+"."+adapterName+".trust"), nil
}

// validAdapterName whitelists name characters. Matches the existing
// adapter naming convention (code, ground-truth, self-logging).
func validAdapterName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

// WriteAdapterTrust grants adapterName the right to take blocking
// actions in projectRoot. Idempotent — re-running overwrites the
// existing marker with a fresh timestamp.
//
// Mirrors the WriteTrustedFingerprint security posture: the marker
// lives under $XDG_CONFIG_HOME (out of the project tree), parent
// dir 0o700, file 0o600, refuses to overwrite a symlink.
func WriteAdapterTrust(projectRoot, adapterName string) error {
	path, err := AdapterTrustFilePath(projectRoot, adapterName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("trust: mkdir %s: %w", filepath.Dir(path), err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trust: %s is a symlink; refusing to overwrite — delete it first", path)
	}
	// Marker content: project + adapter context. Useful when an
	// operator audits ~/.config/leonard/trust/ to see what they've
	// authorized. Not security-bearing — the file's existence is
	// the trust signal; content is informational.
	body := fmt.Sprintf("project: %s\nadapter: %s\n", projectRoot, adapterName)
	return os.WriteFile(path, []byte(body), 0o600)
}

// AdapterTrusted reports whether adapterName has been granted
// blocking authorization for projectRoot. The trust signal is the
// marker file's existence (any non-empty content). Symlink refusal
// matches ReadTrustedFingerprint's bughunt-9 F2 defense.
//
// Returns (false, nil) when no trust has been granted; (true, nil)
// when the marker is present; (false, err) on I/O failures or
// symlink refusal.
func AdapterTrusted(projectRoot, adapterName string) (bool, error) {
	path, err := AdapterTrustFilePath(projectRoot, adapterName)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("trust: lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("trust: %s is a symlink; refusing to follow", path)
	}
	return info.Size() > 0, nil
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
