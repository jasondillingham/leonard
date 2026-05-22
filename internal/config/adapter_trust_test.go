package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

// withXDGConfigHome points os.UserConfigDir at a tempdir so trust
// tests don't pollute the real ~/.config/leonard/trust/. Mirrors
// the pattern in trust_test.go (used by the existing verifier
// trust tests).
func withXDGConfigHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// On macOS UserConfigDir reads HOME first; clear it to force
	// XDG_CONFIG_HOME resolution.
	t.Setenv("HOME", tmp)
	return tmp
}

func TestAdapterTrust_RoundTrip(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()

	// Initially not trusted.
	got, err := config.AdapterTrusted(root, "ground-truth")
	if err != nil {
		t.Fatalf("AdapterTrusted before grant: %v", err)
	}
	if got {
		t.Error("fresh project should not be trusted")
	}

	// Grant.
	if err := config.WriteAdapterTrust(root, "ground-truth"); err != nil {
		t.Fatalf("WriteAdapterTrust: %v", err)
	}

	// Now trusted.
	got, err = config.AdapterTrusted(root, "ground-truth")
	if err != nil {
		t.Fatalf("AdapterTrusted after grant: %v", err)
	}
	if !got {
		t.Error("after grant, adapter should be trusted")
	}
}

func TestAdapterTrust_PerAdapter(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()

	if err := config.WriteAdapterTrust(root, "ground-truth"); err != nil {
		t.Fatalf("grant ground-truth: %v", err)
	}

	// A different adapter should NOT inherit the trust.
	got, err := config.AdapterTrusted(root, "self-logging")
	if err != nil {
		t.Fatalf("AdapterTrusted: %v", err)
	}
	if got {
		t.Error("ground-truth trust must not leak to self-logging")
	}
}

func TestAdapterTrust_PerProject(t *testing.T) {
	withXDGConfigHome(t)
	rootA := t.TempDir()
	rootB := t.TempDir()

	if err := config.WriteAdapterTrust(rootA, "ground-truth"); err != nil {
		t.Fatalf("grant A: %v", err)
	}
	got, err := config.AdapterTrusted(rootB, "ground-truth")
	if err != nil {
		t.Fatalf("AdapterTrusted B: %v", err)
	}
	if got {
		t.Error("project A's trust must not leak to project B")
	}
}

func TestAdapterTrust_Idempotent(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := config.WriteAdapterTrust(root, "ground-truth"); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}
	got, err := config.AdapterTrusted(root, "ground-truth")
	if err != nil || !got {
		t.Errorf("idempotent grant should still be trusted: ok=%v err=%v", got, err)
	}
}

func TestAdapterTrust_DoesntFollowSymlink(t *testing.T) {
	cfgRoot := withXDGConfigHome(t)
	root := t.TempDir()

	// Pre-create a symlink at the trust file path.
	trustPath, err := config.AdapterTrustFilePath(root, "ground-truth")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trustPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bait := filepath.Join(cfgRoot, "bait.txt")
	if err := os.WriteFile(bait, []byte("malicious"), 0o600); err != nil {
		t.Fatalf("write bait: %v", err)
	}
	if err := os.Symlink(bait, trustPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// AdapterTrusted must refuse to follow.
	if _, err := config.AdapterTrusted(root, "ground-truth"); err == nil {
		t.Error("AdapterTrusted should refuse symlink")
	}
	// WriteAdapterTrust must refuse to overwrite a symlink.
	if err := config.WriteAdapterTrust(root, "ground-truth"); err == nil {
		t.Error("WriteAdapterTrust should refuse to overwrite a symlink")
	}
}

func TestAdapterTrustFilePath_RejectsInvalidName(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()
	for _, bad := range []string{"", "Bad-Case", "with space", "../escape", "name/slash"} {
		if _, err := config.AdapterTrustFilePath(root, bad); err == nil {
			t.Errorf("invalid adapter name %q should be rejected", bad)
		}
	}
}

func TestAdapterTrust_MissingMarkerNotTrusted(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()
	got, err := config.AdapterTrusted(root, "ground-truth")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got {
		t.Error("no marker → not trusted")
	}
}

func TestAdapterTrustFilePath_UsesXDGConfigHome(t *testing.T) {
	cfgRoot := withXDGConfigHome(t)
	root := t.TempDir()
	path, err := config.AdapterTrustFilePath(root, "ground-truth")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if !strings.HasPrefix(path, cfgRoot) {
		t.Errorf("trust path %q is not under XDG_CONFIG_HOME %q", path, cfgRoot)
	}
	if !strings.HasSuffix(path, ".ground-truth.trust") {
		t.Errorf("trust path %q missing expected suffix", path)
	}
}

func TestAdapterTrust_DoesntCollideWithVerifier(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()

	// Granting adapter trust must not satisfy verifier trust.
	if err := config.WriteAdapterTrust(root, "ground-truth"); err != nil {
		t.Fatalf("grant adapter: %v", err)
	}
	got, err := config.VerifyCommandTrusted(root, "go vet ./...")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Logf("verifier check err: %v", err)
	}
	if got {
		t.Error("adapter trust must NOT satisfy verifier trust")
	}
}
