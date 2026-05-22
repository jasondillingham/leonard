package groundtruth_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// postEditFixture sets up a tempdir with the valid ground-truth tree
// AND a writable working file under the project root. Returns the
// adapter, project root, and the file path the test should target.
func postEditFixture(t *testing.T, fileName, content string) (*groundtruth.GroundTruthAdapter, string, string) {
	t.Helper()
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir gt: %v", err)
	}
	for _, name := range []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", "valid", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, name), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	target := filepath.Join(tmp, fileName)
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter), tmp, target
}

// readAuditLog returns each JSON line from .leonard/pending-audit.log
// as a decoded map. Missing file returns nil.
func readAuditLog(t *testing.T, projectRoot string) []map[string]any {
	t.Helper()
	logPath := filepath.Join(projectRoot, ".leonard", "pending-audit.log")
	f, err := os.Open(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			t.Fatalf("bad json line: %v", err)
		}
		out = append(out, entry)
	}
	return out
}

func TestPostEdit_WritesForbiddenFindings(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"Our platform: ExampleSaaS supports HIPAA-compliant workflows out of the box.")

	res, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Write",
		FilePath:  target,
	})
	if err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if res.AdditionalContext != "" || res.SystemMessage != "" {
		t.Errorf("PostEdit is non-blocking; result should be empty, got %+v", res)
	}

	entries := readAuditLog(t, tmp)
	if len(entries) != 1 {
		t.Fatalf("entry count: want 1, got %d (%+v)", len(entries), entries)
	}
	e := entries[0]
	if e["file_path"] != "page.md" {
		t.Errorf("file_path: want relative %q, got %q", "page.md", e["file_path"])
	}
	findings, _ := e["findings"].([]any)
	if len(findings) < 1 {
		t.Fatalf("findings missing: %+v", e)
	}
}

func TestPostEdit_DropsVerifiedAndOpinion(t *testing.T) {
	// "I shipped Go in production" → Verified.
	// "I believe Tuesday is better" → Opinion.
	// Both should be dropped from the audit log; only Unverified or
	// Forbidden findings get persisted.
	a, tmp, target := postEditFixture(t, "page.md",
		"Today I shipped Go in production. I believe Tuesday releases are better.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	entries := readAuditLog(t, tmp)
	if len(entries) != 0 {
		t.Errorf("verified + opinion alone should produce no audit entry, got %+v", entries)
	}
}

func TestPostEdit_AppendsAcrossCalls(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"We have a mobile app already.")

	for i := 0; i < 3; i++ {
		if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
			SessionID: "s1",
			Tool:      "Write",
			FilePath:  target,
		}); err != nil {
			t.Fatalf("PostEdit %d: %v", i, err)
		}
	}

	entries := readAuditLog(t, tmp)
	if len(entries) != 3 {
		t.Errorf("append: want 3 entries, got %d", len(entries))
	}
}

func TestPostEdit_SkipsFilesOutsideProject(t *testing.T) {
	a, tmp, _ := postEditFixture(t, "page.md", "irrelevant")
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(outside, []byte("ExampleSaaS supports HIPAA-compliant workflows"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: outside,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readAuditLog(t, tmp); len(entries) != 0 {
		t.Errorf("outside-project file should not be audited, got %+v", entries)
	}
}

func TestPostEdit_RespectsVerifyTargets(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", "valid", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, name), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	a := groundtruth.New()
	// Restrict verify_targets to *.md only.
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Raw: map[string]any{
			"verify_targets": []any{"*.md"},
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gta := a.(*groundtruth.GroundTruthAdapter)

	// Forbidden text in a .py file — should be skipped because *.py
	// isn't in verify_targets.
	pyPath := filepath.Join(tmp, "x.py")
	if err := os.WriteFile(pyPath, []byte("ExampleSaaS supports HIPAA-compliant workflows"), 0o644); err != nil {
		t.Fatalf("write .py: %v", err)
	}
	if _, err := gta.PostEdit(context.Background(), adapters.PostEditPayload{FilePath: pyPath}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readAuditLog(t, tmp); len(entries) != 0 {
		t.Errorf(".py file should be skipped by verify_targets, got %+v", entries)
	}

	// Same content in a .md file — should be picked up.
	mdPath := filepath.Join(tmp, "x.md")
	if err := os.WriteFile(mdPath, []byte("ExampleSaaS supports HIPAA-compliant workflows"), 0o644); err != nil {
		t.Fatalf("write .md: %v", err)
	}
	if _, err := gta.PostEdit(context.Background(), adapters.PostEditPayload{FilePath: mdPath}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readAuditLog(t, tmp); len(entries) != 1 {
		t.Errorf(".md file should produce one entry, got %d", len(entries))
	}
}

func TestPostEdit_MissingFileIsNonFatal(t *testing.T) {
	a, _, _ := postEditFixture(t, "page.md", "irrelevant")
	// Pass a path that doesn't exist; PostEdit should swallow the
	// error and produce no audit entry.
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		FilePath: "ghost.md",
	}); err != nil {
		t.Errorf("PostEdit on missing file should be silent, got %v", err)
	}
}

func TestPostEdit_EntriesAreValidJSONLines(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"We have a mobile app already. ExampleSaaS supports HIPAA-compliant workflows.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "session-abc",
		Tool:      "Edit",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	logPath := filepath.Join(tmp, ".leonard", "pending-audit.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
		for _, required := range []string{"ts", "file_path", "summary", "findings"} {
			if _, ok := entry[required]; !ok {
				t.Errorf("line %d missing required field %q: %v", i, required, entry)
			}
		}
	}
}

func TestPostEdit_NoFactsOrRulesShortCircuits(t *testing.T) {
	tmp := t.TempDir()
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	target := filepath.Join(tmp, "x.md")
	if err := os.WriteFile(target, []byte("ExampleSaaS supports HIPAA-compliant workflows"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{FilePath: target}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if entries := readAuditLog(t, tmp); len(entries) != 0 {
		t.Errorf("empty ground-truth tree should produce no audit entries, got %+v", entries)
	}
}
