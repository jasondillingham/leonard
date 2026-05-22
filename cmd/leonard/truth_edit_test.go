package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/config"
)

func TestTruthEdit_TrivialWritesToken(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "truth-edit", "--trivial", "fix typo", ".leonard/ground-truth/facts.yaml")
	if err != nil {
		t.Fatalf("truth-edit: %v\nout=%s", err, out)
	}

	tokenPath, err := config.PendingTokenPath("trivial", root, ".leonard/ground-truth/facts.yaml")
	if err != nil {
		t.Fatalf("PendingTokenPath: %v", err)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token at %s: %v", tokenPath, err)
	}
	var tok TrivialToken
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if tok.FilePath != ".leonard/ground-truth/facts.yaml" {
		t.Errorf("FilePath: got %q", tok.FilePath)
	}
	if tok.TrivialReason != "fix typo" {
		t.Errorf("TrivialReason: got %q", tok.TrivialReason)
	}
	if tok.Timestamp == "" {
		t.Error("Timestamp should be populated")
	}
}

func TestTruthEdit_RejectsEmptyReason(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-edit", ".leonard/ground-truth/facts.yaml")
	if err == nil || !strings.Contains(err.Error(), "non-empty reason") {
		t.Errorf("want non-empty-reason error, got %v", err)
	}
}

func TestTruthEdit_RejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-edit", "--trivial", "fix", "/etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("want absolute-path error, got %v", err)
	}
}

func TestTruthEdit_RejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-edit", "--trivial", "fix", "../escape.md")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("want path-escape error, got %v", err)
	}
}

func TestTruthEdit_RejectsMissingPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "truth-edit", "--trivial", "fix")
	if err == nil {
		t.Error("expected error for missing path arg")
	}
}

func TestTruthEdit_Idempotent(t *testing.T) {
	// Repeat invocations overwrite the existing token (same filename
	// is sha256-of-path, so the second call clobbers the first).
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	for _, reason := range []string{"first", "second"} {
		if _, err := runRoot(t, rt, "truth-edit", "--trivial", reason, ".leonard/ground-truth/facts.yaml"); err != nil {
			t.Fatalf("trial %q: %v", reason, err)
		}
	}
	tokenPath, err := config.PendingTokenPath("trivial", root, ".leonard/ground-truth/facts.yaml")
	if err != nil {
		t.Fatalf("PendingTokenPath: %v", err)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Errorf("token missing after re-run: %v", err)
	}
}
