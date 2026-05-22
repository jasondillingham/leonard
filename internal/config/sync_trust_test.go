package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

// TestSyncPluginTrust_RoundTrip exercises the bughunt-11 F3 trust
// fingerprint round-trip: empty → write → verify → tamper-detect.
func TestSyncPluginTrust_RoundTrip(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()

	// Not trusted initially.
	ok, err := config.VerifySyncPluginTrusted(root, "github", "/usr/local/bin/leonard-sync-github")
	if err != nil {
		t.Fatalf("VerifySyncPluginTrusted (fresh): %v", err)
	}
	if ok {
		t.Error("fresh project should not be trusted")
	}

	// Grant.
	if err := config.WriteSyncPluginTrust(root, "github", "/usr/local/bin/leonard-sync-github"); err != nil {
		t.Fatalf("WriteSyncPluginTrust: %v", err)
	}

	// Same command → trusted.
	ok, err = config.VerifySyncPluginTrusted(root, "github", "/usr/local/bin/leonard-sync-github")
	if err != nil {
		t.Fatalf("VerifySyncPluginTrusted: %v", err)
	}
	if !ok {
		t.Error("granted plugin should be trusted")
	}

	// Different command → not trusted (fingerprint mismatch).
	ok, err = config.VerifySyncPluginTrusted(root, "github", "/usr/local/bin/EVIL-leonard-sync-github")
	if err != nil {
		t.Fatalf("VerifySyncPluginTrusted (mismatch): %v", err)
	}
	if ok {
		t.Error("different command should not be trusted")
	}
}

func TestSyncPluginTrust_RefusesSymlink(t *testing.T) {
	xdg := withXDGConfigHome(t)
	root := t.TempDir()

	// Plant a symlink at the canonical trust path.
	trustPath, err := config.SyncPluginTrustPath(root, "github")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(trustPath), 0o700); err != nil {
		t.Fatal(err)
	}
	stash := filepath.Join(xdg, "elsewhere")
	if err := os.WriteFile(stash, []byte("forged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stash, trustPath); err != nil {
		t.Fatal(err)
	}

	// Verify refuses to follow.
	_, err = config.VerifySyncPluginTrusted(root, "github", "anything")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("want symlink-refusal error, got %v", err)
	}

	// Write refuses to overwrite the symlink.
	err = config.WriteSyncPluginTrust(root, "github", "anything")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("write: want symlink-refusal error, got %v", err)
	}
}

func TestSyncPluginTrust_RejectsBadName(t *testing.T) {
	root := t.TempDir()
	_, err := config.SyncPluginTrustPath(root, "../escape")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("want invalid-name error, got %v", err)
	}
	_, err = config.SyncPluginTrustPath(root, "")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("empty name: want invalid error, got %v", err)
	}
}

func TestSyncPluginTrust_PerPluginIsolation(t *testing.T) {
	// Granting trust to plugin A doesn't authorize plugin B.
	withXDGConfigHome(t)
	root := t.TempDir()

	if err := config.WriteSyncPluginTrust(root, "github", "/path/a"); err != nil {
		t.Fatal(err)
	}
	ok, _ := config.VerifySyncPluginTrusted(root, "github", "/path/a")
	if !ok {
		t.Error("github should be trusted")
	}
	ok, _ = config.VerifySyncPluginTrusted(root, "linear", "/path/a")
	if ok {
		t.Error("linear should NOT inherit github's trust")
	}
}

func TestSyncPluginTrust_EmptyCommandRejected(t *testing.T) {
	withXDGConfigHome(t)
	root := t.TempDir()
	err := config.WriteSyncPluginTrust(root, "github", "")
	if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "empty") {
		t.Errorf("want empty-command error, got %v", err)
	}
}
