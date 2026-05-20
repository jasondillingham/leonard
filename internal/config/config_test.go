package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultRoundTrip(t *testing.T) {
	t.Parallel()
	want := Default()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(want, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\nwant %+v\n got %+v", want, got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}

func TestLoadOrDefaultMissingReturnsDefault(t *testing.T) {
	t.Parallel()
	got, err := LoadOrDefault(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("expected Default(), got %+v", got)
	}
}

func TestLoadMalformedSurfacesError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeFile(path, "this is not = valid = toml = at all\n[[["); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestSaveCreatesParentDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "sub", "config.toml")
	if err := Save(Default(), path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
}

func writeFile(path, contents string) error {
	return writeBytes(path, []byte(contents))
}

func TestLoadDecodesPostEditVerify(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const body = `
[hooks]
inject_decisions_at_session_start = 10
surface_unverified_claims_at_stop = 20

[post_edit.verify]
command = "cargo check --workspace"
working_dir = "crates/server"
timeout = "90s"
`
	if err := writeFile(path, body); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := VerifyConfig{
		Command:    "cargo check --workspace",
		WorkingDir: "crates/server",
		Timeout:    "90s",
	}
	if !reflect.DeepEqual(got.PostEdit.Verify, want) {
		t.Fatalf("verify config mismatch\nwant %+v\n got %+v", want, got.PostEdit.Verify)
	}
}

// Default() omits [post_edit.verify] so projects opting out of the
// feature have nothing to disable — the absent section is the off
// switch. Guards against future Default() bloat that would resurrect
// the "hidden default that vanishes on init re-runs" footgun the
// Bughunt-2 trim explicitly called out.
func TestDefaultLeavesPostEditVerifyEmpty(t *testing.T) {
	t.Parallel()
	c := Default()
	if c.PostEdit.Verify.Command != "" {
		t.Errorf("Default().PostEdit.Verify.Command = %q, want empty", c.PostEdit.Verify.Command)
	}
	if c.PostEdit.Verify.WorkingDir != "" {
		t.Errorf("Default().PostEdit.Verify.WorkingDir = %q, want empty", c.PostEdit.Verify.WorkingDir)
	}
	if c.PostEdit.Verify.Timeout != "" {
		t.Errorf("Default().PostEdit.Verify.Timeout = %q, want empty", c.PostEdit.Verify.Timeout)
	}
}
