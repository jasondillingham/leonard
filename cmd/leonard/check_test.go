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

// TestCheck_ShowsLineNumber verifies that plain output reports "line N"
// rather than raw byte offsets for claims.
func TestCheck_ShowsLineNumber(t *testing.T) {
	content := "Introduction.\n\nWe have 50,000 users on the platform.\n"
	root, target := checkFixture(t, "blog.md", content, "# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, _ := runRoot(t, rt, "check", target)
	if !strings.Contains(out, "line ") {
		t.Errorf("output should contain 'line N'; got:\n%s", out)
	}
	if strings.Contains(out, "bytes ") {
		t.Errorf("output should not contain raw byte offsets; got:\n%s", out)
	}
	// The claim is on line 3 (after two preceding lines).
	if !strings.Contains(out, "line 3") {
		t.Errorf("expected claim on line 3; got:\n%s", out)
	}
}

// TestListStaleClaims_CleanProject exits 0 and produces no findings.
func TestListStaleClaims_CleanProject(t *testing.T) {
	root, _ := checkFixture(t, "safe.md", "This is ordinary prose.", "# rules\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	out, err := runRoot(t, rt, "list-stale-claims")
	if err != nil {
		t.Fatalf("list-stale-claims: %v\nout=%s", err, out)
	}
}

// TestListStaleClaims_ForbiddenExitsTwo detects a forbidden claim across files.
func TestListStaleClaims_ForbiddenExitsTwo(t *testing.T) {
	root, _ := checkFixture(t, "marketing.md",
		"Our platform is HIPAA-compliant for healthcare.",
		"## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n")
	withCwd(t, root)
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "list-stale-claims")
	var ec *exitCode
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Errorf("want exit 2, got %v", err)
	}
}

// TestListStaleClaims_RespectsLeonardignore verifies that files matched by
// .leonardignore are excluded from the default walk. This prevents operator-
// internal directories (audit logs, red-team runlogs, etc.) from flooding
// the results when listed in .leonardignore.
func TestListStaleClaims_RespectsLeonardignore(t *testing.T) {
	root, _ := checkFixture(t, "clean.md", "Ordinary text.", "## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n")
	withCwd(t, root)

	// Write a forbidden claim in audits/runlog.md.
	if err := os.MkdirAll(filepath.Join(root, "audits"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "audits", "runlog.md"),
		[]byte("Our platform is HIPAA-compliant for healthcare."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without .leonardignore: audits/ is scanned → exit 2.
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "list-stale-claims")
	var ec *exitCode
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("without .leonardignore: want exit 2 (forbidden found), got %v", err)
	}

	// Add .leonardignore excluding audits/.
	if err := os.WriteFile(filepath.Join(root, ".leonardignore"), []byte("audits/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Now the walk skips audits/ → exit 0 (only clean.md is scanned).
	out, err := runRoot(t, rt, "list-stale-claims")
	if err != nil {
		t.Errorf("with .leonardignore: want exit 0, got %v\nout=%s", err, out)
	}
}

// TestListStaleClaims_ScopeGlob restricts scanning to the matched files.
func TestListStaleClaims_ScopeGlob(t *testing.T) {
	// Write a forbidden claim only in subdir/bad.md; docs/safe.md is clean.
	root := t.TempDir()
	gtDir := filepath.Join(root, dataDirName, "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "tech_stack:\n  primary_language: Go\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "subdir", "bad.md"),
		[]byte("Our platform is HIPAA-compliant for healthcare."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "safe.md"),
		[]byte("Ordinary text."), 0o644); err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		// docs/ may not exist; ignore — we only care that subdir/bad.md is found
	}

	withCwd(t, root)
	rt := &fakeRuntime{}

	// Scoping to docs/*.md should skip subdir/bad.md → clean exit.
	out, err := runRoot(t, rt, "list-stale-claims", "--scope=docs/*.md")
	if err != nil {
		// docs/*.md may match nothing — that's a clean exit too.
		var ec *exitCode
		if errors.As(err, &ec) {
			t.Errorf("scoped to docs/ should be clean, got exit %d\nout=%s", ec.code, out)
		}
	}

	// Scoping to subdir/*.md should find the forbidden claim.
	_, err = runRoot(t, rt, "list-stale-claims", "--scope=subdir/*.md")
	var ec *exitCode
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Errorf("subdir scope: want exit 2, got %v", err)
	}
}
