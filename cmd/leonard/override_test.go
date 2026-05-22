package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

func TestOverride_WritesToken(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "override", "--once", "--reason", "Acme is back; legal cleared", "applications/acme-corp/cover-letter.md")
	if err != nil {
		t.Fatalf("override: %v\nout=%s", err, out)
	}

	tokenPath, err := config.PendingTokenPath("override", root, "applications/acme-corp/cover-letter.md")
	if err != nil {
		t.Fatalf("PendingTokenPath: %v", err)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token at %s: %v", tokenPath, err)
	}
	var tok OverrideToken
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tok.FilePath != "applications/acme-corp/cover-letter.md" {
		t.Errorf("FilePath: got %q", tok.FilePath)
	}
	if tok.Reason != "Acme is back; legal cleared" {
		t.Errorf("Reason: got %q", tok.Reason)
	}
	if tok.Timestamp == "" {
		t.Error("Timestamp should be populated")
	}
}

func TestOverride_RequiresOnceFlag(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "override", "--reason", "x", "path.md")
	if err == nil || !strings.Contains(err.Error(), "--once") {
		t.Errorf("want --once error, got %v", err)
	}
}

func TestOverride_RequiresReason(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "override", "--once", "path.md")
	if err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Errorf("want --reason error, got %v", err)
	}
}

func TestOverride_RejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "override", "--once", "--reason", "x", "/etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("want absolute-path error, got %v", err)
	}
}

func TestOverride_RejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "override", "--once", "--reason", "x", "../escape.md")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("want escape error, got %v", err)
	}
}

func TestOverride_RequiresPathArg(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "override", "--once", "--reason", "x")
	if err == nil {
		t.Error("expected missing-arg error")
	}
}
