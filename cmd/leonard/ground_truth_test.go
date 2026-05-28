package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gtFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	gtDir := filepath.Join(root, dataDirName, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGroundTruthLint_OK(t *testing.T) {
	root := gtFixture(t, map[string]string{
		"facts.yaml":      "tech_stack:\n  primary_language: Go\n",
		"stories.md":      "## STORY: launch\n\n### Short version\nShipped 1.0.\n\n### Sensitivity\npublic\n",
		"do-not-claim.md": "## C\n\n- ❌ \"X\" — reason\n",
		"filters.yaml":    "",
	})
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "ground-truth", "lint")
	if err != nil {
		t.Fatalf("lint: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("lint: want OK, got %s", out)
	}
	for _, line := range []string{"facts:", "stories:", "rules:", "filters:"} {
		if !strings.Contains(out, line) {
			t.Errorf("lint missing %q: %s", line, out)
		}
	}
}

func TestGroundTruthLint_MalformedYAML(t *testing.T) {
	root := gtFixture(t, map[string]string{
		"facts.yaml": "{ this: is, not, yaml",
	})
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "ground-truth", "lint")
	if err == nil {
		t.Fatal("expected lint failure")
	}
	var ec *exitCode
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Errorf("exit: want 1, got %v", err)
	}
	if !strings.Contains(out, "facts.yaml") {
		t.Errorf("lint output should reference facts.yaml; got: %q", out)
	}
}

func TestGroundTruthStats_Populated(t *testing.T) {
	root := gtFixture(t, map[string]string{
		"facts.yaml": "tech_stack:\n  primary_language: Go\n  database: PostgreSQL\nproduct:\n  name: ExampleSaaS\n",
		"stories.md": "## STORY: alpha\n\n### Short version\na\n\n## STORY: beta\n\n### Short version\nb\n",
		"do-not-claim.md": `## A

- ❌ "x" — r

## B

- ❌ "y" — r
- ❌ "z" — r
`,
		"filters.yaml": `path_filters:
  - path_pattern: "blocked/"
content_filters:
  - content_pattern: "(?i)bid"
    required: "disclose"
`,
	})
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "ground-truth", "stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	for _, want := range []string{
		"facts.yaml",
		"stories.md",
		"do-not-claim.md",
		"filters.yaml",
		"path_filters:    1",
		"content_filters: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stats missing %q in:\n%s", want, out)
		}
	}
}

func TestGroundTruthStats_Empty(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "ground-truth", "stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !strings.Contains(out, "(empty)") {
		t.Errorf("empty stats should report (empty): %s", out)
	}
}
