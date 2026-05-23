package groundtruth_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// TestTruthTreeEdit_LogsToAuditLog covers issue #89: when the
// operator edits a truth-tree file (facts.yaml, stories.md,
// do-not-claim.md, filters.yaml), the post-edit hook should
// append a "tree edit" section to audit-log.md. Pre-#89 those
// edits left no trace in the operator-facing ledger.
func TestTruthTreeEdit_LogsToAuditLog(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	gtDir := filepath.Join(root, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "x: 1\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, _ := filepath.EvalSymlinks(root)
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: resolved}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	// Simulate PostEdit on a truth-tree file (facts.yaml).
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Edit",
		FilePath:  filepath.Join(resolved, ".leonard", "ground-truth", "facts.yaml"),
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	// audit-log.md should now exist with a "tree edit" section.
	logPath := filepath.Join(resolved, ".leonard", "ground-truth", "audit-log.md")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read audit-log.md: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"tree edit",
		"facts.yaml",
		"**Tool:** Edit",
		"**Session:** s1",
		"**Type:** truth-tree edit (operator-authored)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("audit-log.md missing %q in:\n%s", want, got)
		}
	}
}

// TestTruthTreeEdit_NonTruthFileSkipsTreeEntry confirms a regular
// project file (not inside truth_dir) goes through the normal
// finding-detection path, not the tree-edit path. We don't want
// the new code path to over-fire.
func TestTruthTreeEdit_NonTruthFileSkipsTreeEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	gtDir := filepath.Join(root, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "x: 1\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# Rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, _ := filepath.EvalSymlinks(root)
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: resolved}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	// Write a regular project file with no forbidden claims.
	target := filepath.Join(resolved, "draft.md")
	if err := os.WriteFile(target, []byte("Some safe content."), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	// audit-log.md should NOT exist (no findings + no tree-edit
	// because this isn't a truth-tree file).
	logPath := filepath.Join(resolved, ".leonard", "ground-truth", "audit-log.md")
	if _, err := os.Stat(logPath); err == nil {
		body, _ := os.ReadFile(logPath)
		if strings.Contains(string(body), "tree edit") {
			t.Errorf("regular-file edit shouldn't produce a tree-edit entry, got:\n%s", body)
		}
	}
}

// TestTruthTreeEdit_AppendsAcrossEdits confirms multiple truth-tree
// edits accumulate as separate sections in the same audit-log.md
// (append-only ledger semantics preserved).
func TestTruthTreeEdit_AppendsAcrossEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	gtDir := filepath.Join(root, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "x: 1\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# Rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, _ := filepath.EvalSymlinks(root)
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: resolved}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	// Three edits to different truth-tree files.
	for _, file := range []string{"facts.yaml", "stories.md", "do-not-claim.md"} {
		if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
			SessionID: "s1",
			Tool:      "Edit",
			FilePath:  filepath.Join(resolved, ".leonard", "ground-truth", file),
		}); err != nil {
			t.Fatalf("PostEdit %s: %v", file, err)
		}
	}

	body, err := os.ReadFile(filepath.Join(resolved, ".leonard", "ground-truth", "audit-log.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if n := strings.Count(got, "— tree edit\n"); n != 3 {
		t.Errorf("want 3 tree-edit sections, got %d in:\n%s", n, got)
	}
	for _, file := range []string{"facts.yaml", "stories.md", "do-not-claim.md"} {
		if !strings.Contains(got, file) {
			t.Errorf("audit-log.md missing %q in:\n%s", file, got)
		}
	}
}
