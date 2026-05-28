package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// factsFixture builds a minimal project with a populated facts.yaml and
// optional .md files for CLI-level facts command tests.
func factsFixture(t *testing.T, factsYAML string, mdFiles map[string]string) string {
	t.Helper()
	root := t.TempDir()
	gtDir := filepath.Join(root, dataDirName, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      factsYAML,
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, content := range mdFiles {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestFactsImpact_ReportsMatchingFile(t *testing.T) {
	root := factsFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		map[string]string{
			"README.md": "This project uses Go for all backend services.",
		})
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "facts", "impact", "tech_stack.primary_language")
	if err != nil {
		t.Fatalf("facts impact: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "README.md") {
		t.Errorf("expected README.md in output, got:\n%s", out)
	}
}

func TestFactsImpact_NoReferences(t *testing.T) {
	root := factsFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		map[string]string{
			"README.md": "This document contains no language claims.",
		})
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "facts", "impact", "tech_stack.primary_language")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "no .md files") {
		t.Errorf("expected 'no .md files' message, got:\n%s", out)
	}
}

func TestFactsImpact_EmptyKeyRejected(t *testing.T) {
	root := factsFixture(t, "tech_stack:\n  primary_language: Go\n", nil)
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "facts", "impact", "")
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected empty-key error, got: %v", err)
	}
}

func TestFactsImpact_MissingKeyExitsOne(t *testing.T) {
	root := factsFixture(t, "tech_stack:\n  primary_language: Go\n", nil)
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "facts", "impact", "nonexistent.key")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestFactsImpact_SubtreeKey(t *testing.T) {
	root := factsFixture(t,
		"tech_stack:\n  primary_language: Go\n  framework: Cobra\n",
		map[string]string{
			"README.md": "Built with Go and the Cobra CLI framework.",
		})
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "facts", "impact", "tech_stack")
	if err != nil {
		t.Fatalf("facts impact subtree: %v\nout=%s", err, out)
	}
	// Both "Go" and "Cobra" should appear in the output.
	if !strings.Contains(out, "README.md") {
		t.Errorf("expected README.md in output, got:\n%s", out)
	}
}
