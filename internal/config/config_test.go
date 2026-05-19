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
