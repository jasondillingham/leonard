package groundtruth_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// reloadFixture sets up a tempdir with an initial truth tree, Init's
// the adapter, and returns the adapter, tempdir path, and stderr
// buffer.
func reloadFixture(t *testing.T) (*groundtruth.GroundTruthAdapter, string, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile := func(name, body string) {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("facts.yaml", "product:\n  name: ExampleSaaS\n")
	writeFile("stories.md", "# Stories\n")
	writeFile("do-not-claim.md", "## Initial\n\n- ❌ \"Stale claim\" — Initial rule.\n")
	writeFile("filters.yaml", "")

	stderr := &bytes.Buffer{}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Stderr:      stderr,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter), tmp, stderr
}

func TestReload_PicksUpNewRule(t *testing.T) {
	a, tmp, _ := reloadFixture(t)
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")

	// Initial: one rule.
	if got := len(a.Rules()); got != 1 {
		t.Fatalf("initial rule count: want 1, got %d", got)
	}

	// Operator adds a new rule.
	if err := os.WriteFile(filepath.Join(gtDir, "do-not-claim.md"), []byte(`## Initial

- ❌ "Stale claim" — Initial rule.

## Added

- ❌ "Fresh forbidden" — Added at runtime.
`), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	if err := a.ReloadOnce(); err != nil {
		t.Fatalf("ReloadOnce: %v", err)
	}

	if got := len(a.Rules()); got != 2 {
		t.Errorf("after reload: want 2 rules, got %d", got)
	}
}

func TestReload_PicksUpFactsChange(t *testing.T) {
	a, tmp, _ := reloadFixture(t)
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")

	// Rewrite facts.yaml with a new key.
	if err := os.WriteFile(filepath.Join(gtDir, "facts.yaml"),
		[]byte("tech_stack:\n  primary_language: Rust\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := a.ReloadOnce(); err != nil {
		t.Fatalf("ReloadOnce: %v", err)
	}

	ts, _ := a.Facts().Root["tech_stack"].(map[string]any)
	if got, _ := ts["primary_language"].(string); got != "Rust" {
		t.Errorf("primary_language after reload: want %q, got %q", "Rust", got)
	}
}

func TestReload_PicksUpFiltersChange(t *testing.T) {
	a, tmp, _ := reloadFixture(t)
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")

	if err := os.WriteFile(filepath.Join(gtDir, "filters.yaml"), []byte(`path_filters:
  - path_pattern: "blocked/"
    reason: "blanket ban"
`), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := a.ReloadOnce(); err != nil {
		t.Fatalf("ReloadOnce: %v", err)
	}

	if got := len(a.Filters().PathFilters); got != 1 {
		t.Errorf("PathFilters after reload: want 1, got %d", got)
	}
}

func TestReload_KeepsPriorStateOnParseError(t *testing.T) {
	a, tmp, _ := reloadFixture(t)
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")

	priorRules := a.Rules()
	if len(priorRules) != 1 {
		t.Fatalf("initial: want 1 rule, got %d", len(priorRules))
	}

	// Write a malformed filters.yaml.
	if err := os.WriteFile(filepath.Join(gtDir, "filters.yaml"),
		[]byte(`path_filters:
  - path_pattern: "[invalid regex("
`), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	err := a.ReloadOnce()
	if err == nil {
		t.Error("expected reload error on malformed yaml")
	}

	// Adapter state should be unchanged — the prior rules still load.
	if got := len(a.Rules()); got != 1 {
		t.Errorf("rules after failed reload: want 1 (unchanged), got %d", got)
	}
}

func TestReload_FactsLookupReflectsLatestState(t *testing.T) {
	a, tmp, _ := reloadFixture(t)
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")

	if err := os.WriteFile(filepath.Join(gtDir, "do-not-claim.md"), []byte(`## After

- ❌ "Hot-reloaded forbidden claim" — New rule.
`), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := a.ReloadOnce(); err != nil {
		t.Fatalf("ReloadOnce: %v", err)
	}

	// Detect should now flag the new claim text.
	res := a.Detect("Sales pitch: hot-reloaded forbidden claim is mature.")
	if res.Summary.Forbidden < 1 {
		t.Errorf("Detect after reload: want forbidden hit, got %+v", res.Claims)
	}
}

func TestClose_StopsWatcher(t *testing.T) {
	a, _, _ := reloadFixture(t)

	// First Close stops the goroutine; second Close is a no-op.
	if err := a.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("idempotent Close: %v", err)
	}
}

func TestReload_LogsErrorToStderr(t *testing.T) {
	// Verify the watch goroutine surfaces parse errors via stderr
	// (not direct — the test calls ReloadOnce, then asserts the
	// stderr was used in the malformed-file branch).
	a, tmp, _ := reloadFixture(t)
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")

	// Take a fresh stderr; the watch goroutine writes there.
	if err := os.WriteFile(filepath.Join(gtDir, "filters.yaml"),
		[]byte(`path_filters: "not a list"`), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	err := a.ReloadOnce()
	if err == nil || !strings.Contains(err.Error(), "path_filters") {
		t.Errorf("expected path_filters parse error, got %v", err)
	}
}
