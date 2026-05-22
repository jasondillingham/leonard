package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

// withXDG points os.UserConfigDir at a tempdir for these tests so
// they don't pollute ~/.config/leonard/trust/.
func withXDG(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
}

func TestConfigTrustGroundTruth_WritesMarker(t *testing.T) {
	withXDG(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	if _, err := runRoot(t, rt, "config", "trust", "ground-truth", "--yes"); err != nil {
		t.Fatalf("trust: %v", err)
	}

	// macOS: t.TempDir() returns the unresolved form
	// (/var/folders/...) but os.Getwd() returns the resolved form
	// (/private/var/folders/...). The trust hash is keyed off the
	// resolved path the command used, so look it up the same way.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := config.AdapterTrusted(resolved, "ground-truth")
	if err != nil {
		t.Fatalf("AdapterTrusted: %v", err)
	}
	if !got {
		t.Error("trust marker not present after `config trust ground-truth`")
	}
}

func TestConfigTrustGroundTruth_DefaultTargetIsVerifier(t *testing.T) {
	withXDG(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a minimal config.toml with an empty verifier so we hit
	// the "command is empty" branch (proving target=verifier is the
	// default) instead of a generic "no such file" error.
	cfgPath := filepath.Join(root, dataDirName, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[post_edit.verify]\ncommand = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "config", "trust", "--yes")
	if err == nil {
		t.Error("expected verifier-empty error when target omitted")
		return
	}
	if !strings.Contains(err.Error(), "[post_edit.verify].command") {
		t.Errorf("error should mention verifier config, got %v", err)
	}
}

func TestConfigTrustGroundTruth_RejectsUnknownTarget(t *testing.T) {
	withXDG(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "config", "trust", "kebab", "--yes")
	if err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Errorf("expected unknown-target error, got %v", err)
	}
}

func TestConfigTrustGroundTruth_RequiresYesOrInteractive(t *testing.T) {
	withXDG(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	// Without --yes the prompt reads from stdin; runRoot supplies an
	// empty stdin so the read errors. The command should abort
	// (NOT silently grant) when confirmation is missing.
	_, err := runRoot(t, rt, "config", "trust", "ground-truth")
	if err == nil {
		t.Error("trust without --yes should fail on empty stdin")
	}
	got, _ := config.AdapterTrusted(root, "ground-truth")
	if got {
		t.Error("trust should NOT be granted when confirmation fails")
	}
}
