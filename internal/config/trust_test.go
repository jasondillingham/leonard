package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrustFileLivesOutsideProjectTree pins bughunt-9 F1 (CRIT):
// the v0.51 trust file at .leonard/trusted-verifier.sha256 lived in
// the same dir the bash lexical scanner couldn't fully protect.
// v0.52 moves it to the user config dir; this test asserts the
// returned path does NOT contain the project root.
func TestTrustFileLivesOutsideProjectTree(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	trustPath, err := TrustFilePath(projectRoot)
	if err != nil {
		t.Fatalf("TrustFilePath: %v", err)
	}
	if strings.HasPrefix(trustPath, projectRoot) {
		t.Errorf("trust file %q is inside project root %q — relocation regressed", trustPath, projectRoot)
	}
	if !strings.Contains(trustPath, "leonard/trust/") && !strings.Contains(trustPath, "leonard\\trust\\") {
		t.Errorf("trust path %q doesn't look like user-config-dir layout", trustPath)
	}
}

// TestReadTrustedFingerprint_RefusesSymlink pins bughunt-9 F2 (CRIT):
// the v0.51 ReadTrustedFingerprint used os.ReadFile which follows
// symlinks. v0.52 Lstat's first and refuses symlinks.
func TestReadTrustedFingerprint_RefusesSymlink(t *testing.T) {
	// Set HOME to a controlled tmpdir so the trust file lives where
	// we expect, then plant a symlink at the trust path.
	tmpHome := t.TempDir()

	t.Setenv("HOME", tmpHome)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	projectRoot := t.TempDir()
	trustPath, err := TrustFilePath(projectRoot)
	if err != nil {
		t.Fatalf("TrustFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trustPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink to a different file containing a malicious
	// fingerprint.
	decoy := filepath.Join(tmpHome, "decoy.sha256")
	if err := os.WriteFile(decoy, []byte("attacker-fingerprint\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, trustPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, err := ReadTrustedFingerprint(projectRoot); err == nil {
		t.Error("ReadTrustedFingerprint accepted a symlinked trust file; CRIT regression")
	}
}

// TestWriteTrustedFingerprint_RefusesOverwriteSymlink confirms the
// write path also refuses to clobber a planted symlink.
func TestWriteTrustedFingerprint_RefusesOverwriteSymlink(t *testing.T) {
	tmpHome := t.TempDir()

	t.Setenv("HOME", tmpHome)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	projectRoot := t.TempDir()
	trustPath, err := TrustFilePath(projectRoot)
	if err != nil {
		t.Fatalf("TrustFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trustPath), 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(tmpHome, "decoy.sha256")
	if err := os.WriteFile(decoy, []byte("attacker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, trustPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := WriteTrustedFingerprint(projectRoot, FingerprintCommand("echo trust")); err == nil {
		t.Error("WriteTrustedFingerprint clobbered a symlink without refusal")
	}
}

// TestVerifyCommandTrusted_RoundTrip is the happy-path contract.
func TestVerifyCommandTrusted_RoundTrip(t *testing.T) {
	tmpHome := t.TempDir()

	t.Setenv("HOME", tmpHome)

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	projectRoot := t.TempDir()
	cmd := "cargo check --workspace"
	if err := WriteTrustedFingerprint(projectRoot, FingerprintCommand(cmd)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ok, err := VerifyCommandTrusted(projectRoot, cmd)
	if err != nil || !ok {
		t.Fatalf("expected trusted, got ok=%v err=%v", ok, err)
	}
	// Different command must NOT verify.
	if ok, _ := VerifyCommandTrusted(projectRoot, cmd+" --release"); ok {
		t.Error("a modified command verified as trusted")
	}
}
