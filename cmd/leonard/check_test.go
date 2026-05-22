package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkFixture sets up a project with a populated ground-truth tree
// and writes a target file with the supplied content. Returns the
// project root + absolute file path.
func checkFixture(t *testing.T, fileName, content, rules string) (string, string) {
	t.Helper()
	root := t.TempDir()
	gtDir := filepath.Join(root, dataDirName, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal stories/filters/facts so Init succeeds.
	for name, body := range map[string]string{
		"facts.yaml":      "tech_stack:\n  primary_language: Go\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": rules,
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, fileName)
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, target
}

func TestCheck_CleanFile(t *testing.T) {
	root, target := checkFixture(t, "safe.md",
		"This is a paragraph about nothing in particular.",
		"# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "check", target)
	if err != nil {
		t.Fatalf("check: %v\nout=%s", err, out)
	}
	if !strings.Contains(out, "clean") {
		t.Errorf("clean file: %s", out)
	}
}

func TestCheck_ForbiddenFindingExitsTwo(t *testing.T) {
	root, target := checkFixture(t, "marketing.md",
		"Our platform is HIPAA-compliant for healthcare.",
		"## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "check", target)
	if err == nil {
		t.Fatal("expected non-zero exit on forbidden finding")
	}
	var ec *exitCode
	if !errors.As(err, &ec) {
		t.Fatalf("error type: want *exitCode, got %T", err)
	}
	if ec.code != 2 {
		t.Errorf("exit code: want 2, got %d", ec.code)
	}
}

func TestCheck_UnverifiedFindingExitsOne(t *testing.T) {
	root, target := checkFixture(t, "blog.md",
		"We have 50,000 users on the platform.",
		"# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "check", target)
	if err == nil {
		t.Fatal("expected non-zero exit on unverified finding")
	}
	var ec *exitCode
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Errorf("exit code: want 1, got %v", err)
	}
}

func TestCheck_JSONFormat(t *testing.T) {
	root, target := checkFixture(t, "x.md",
		"We have a mobile app.",
		"## Capabilities\n\n- ❌ \"We have a mobile app\" — Web only.\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, _ := runRoot(t, rt, "check", target, "--format=json")
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"file", "claims", "summary"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("JSON missing %q: %v", key, payload)
		}
	}
}

func TestCheck_RejectsBadFormat(t *testing.T) {
	root, target := checkFixture(t, "x.md", "safe content", "# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "check", target, "--format=yaml")
	if err == nil || !strings.Contains(err.Error(), "--format") {
		t.Errorf("want format error, got %v", err)
	}
}

func TestCheck_RejectsBadAdapter(t *testing.T) {
	root, target := checkFixture(t, "x.md", "safe", "# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "check", target, "--adapter=kebab")
	if err == nil || !strings.Contains(err.Error(), "--adapter") {
		t.Errorf("want adapter error, got %v", err)
	}
}

func TestCheck_MissingFile(t *testing.T) {
	root, _ := checkFixture(t, "x.md", "safe", "# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "check", filepath.Join(root, "ghost.md"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}
