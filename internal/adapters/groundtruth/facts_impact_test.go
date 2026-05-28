package groundtruth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// impactFixture builds a temp project with a facts.yaml and optional .md files.
func impactFixture(t *testing.T, factsYAML string, mdFiles map[string]string) (root string, facts *groundtruth.Facts) {
	t.Helper()
	root = t.TempDir()
	factsPath := filepath.Join(root, "facts.yaml")
	if err := os.WriteFile(factsPath, []byte(factsYAML), 0o644); err != nil {
		t.Fatalf("write facts.yaml: %v", err)
	}
	for rel, content := range mdFiles {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	f, err := groundtruth.LoadFacts(factsPath)
	if err != nil {
		t.Fatalf("LoadFacts: %v", err)
	}
	return root, f
}

// --- ResolveFactKey ---

func TestResolveFactKey_Scalar(t *testing.T) {
	_, facts := impactFixture(t, "tech_stack:\n  primary_language: Go\n", nil)
	leaves, err := groundtruth.ResolveFactKey(facts, "tech_stack.primary_language")
	if err != nil {
		t.Fatalf("ResolveFactKey: %v", err)
	}
	if len(leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d", len(leaves))
	}
	if leaves[0].Value != "Go" {
		t.Errorf("value: want %q, got %q", "Go", leaves[0].Value)
	}
	if leaves[0].Path != "tech_stack.primary_language" {
		t.Errorf("path: want %q, got %q", "tech_stack.primary_language", leaves[0].Path)
	}
}

func TestResolveFactKey_SubtreeReturnsAllLeaves(t *testing.T) {
	_, facts := impactFixture(t, "tech_stack:\n  primary_language: Go\n  framework: Cobra\n", nil)
	leaves, err := groundtruth.ResolveFactKey(facts, "tech_stack")
	if err != nil {
		t.Fatalf("ResolveFactKey: %v", err)
	}
	if len(leaves) != 2 {
		t.Errorf("want 2 leaves (primary_language + framework), got %d", len(leaves))
	}
}

func TestResolveFactKey_ArrayIndex(t *testing.T) {
	_, facts := impactFixture(t, "tools:\n  - hammer\n  - screwdriver\n", nil)
	leaves, err := groundtruth.ResolveFactKey(facts, "tools[1]")
	if err != nil {
		t.Fatalf("ResolveFactKey: %v", err)
	}
	if len(leaves) != 1 || leaves[0].Value != "screwdriver" {
		t.Errorf("want [screwdriver], got %+v", leaves)
	}
}

func TestResolveFactKey_ArraySubtree(t *testing.T) {
	_, facts := impactFixture(t, "tools:\n  - hammer\n  - screwdriver\n", nil)
	leaves, err := groundtruth.ResolveFactKey(facts, "tools")
	if err != nil {
		t.Fatalf("ResolveFactKey: %v", err)
	}
	if len(leaves) != 2 {
		t.Errorf("want 2 leaves for array, got %d", len(leaves))
	}
}

func TestResolveFactKey_MissingKey(t *testing.T) {
	_, facts := impactFixture(t, "tech_stack:\n  primary_language: Go\n", nil)
	_, err := groundtruth.ResolveFactKey(facts, "does_not_exist")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestResolveFactKey_EmptyFacts(t *testing.T) {
	_, facts := impactFixture(t, "", nil)
	_, err := groundtruth.ResolveFactKey(facts, "anything")
	if err == nil {
		t.Error("expected error for empty facts")
	}
}

func TestResolveFactKey_NumericValue(t *testing.T) {
	_, facts := impactFixture(t, "metrics:\n  message_count: 9319\n", nil)
	leaves, err := groundtruth.ResolveFactKey(facts, "metrics.message_count")
	if err != nil {
		t.Fatalf("ResolveFactKey: %v", err)
	}
	if len(leaves) != 1 || leaves[0].Value != "9319" {
		t.Errorf("want value %q, got %+v", "9319", leaves)
	}
}

// --- FindImpactedFiles ---

func TestFindImpactedFiles_HitsAndMisses(t *testing.T) {
	root, _ := impactFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		map[string]string{
			"README.md":   "We use Go for all backend services.",
			"frontend.md": "The UI is written in TypeScript.",
		})
	results, err := groundtruth.FindImpactedFiles(root, "Go", "tech_stack.primary_language", nil)
	if err != nil {
		t.Fatalf("FindImpactedFiles: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("want 1 result (README.md), got %d: %+v", len(results), results)
	}
	if len(results) > 0 && !strings.HasSuffix(results[0].Path, "README.md") {
		t.Errorf("expected README.md, got %s", results[0].Path)
	}
}

func TestFindImpactedFiles_SkipsBuildDirs(t *testing.T) {
	root, _ := impactFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		map[string]string{
			"node_modules/pkg/README.md": "Uses Go under the hood.",
			"docs/intro.md":              "Ordinary text with no claims.",
		})
	results, err := groundtruth.FindImpactedFiles(root, "Go", "tech_stack.primary_language", nil)
	if err != nil {
		t.Fatalf("FindImpactedFiles: %v", err)
	}
	for _, r := range results {
		if strings.Contains(r.Path, "node_modules") {
			t.Errorf("node_modules should be skipped, got %s", r.Path)
		}
	}
}

func TestFindImpactedFiles_FirstLineAndHitCount(t *testing.T) {
	root, _ := impactFixture(t,
		"metrics:\n  message_count: 9319\n",
		map[string]string{
			"report.md": "Line one.\nWe processed 9319 messages.\nAlso 9319 in Q2.\n",
		})
	results, err := groundtruth.FindImpactedFiles(root, "9319", "metrics.message_count", nil)
	if err != nil {
		t.Fatalf("FindImpactedFiles: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].FirstLine != 2 {
		t.Errorf("first line: want 2, got %d", results[0].FirstLine)
	}
	if results[0].Hits != 2 {
		t.Errorf("hits: want 2, got %d", results[0].Hits)
	}
}

func TestFindImpactedFiles_WordBoundary(t *testing.T) {
	// "Go" should not match "Google" or "Going".
	root, _ := impactFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		map[string]string{
			"marketing.md": "Google is our partner. We are going forward.",
		})
	results, err := groundtruth.FindImpactedFiles(root, "Go", "tech_stack.primary_language", nil)
	if err != nil {
		t.Fatalf("FindImpactedFiles: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("word-boundary: 'Go' should not match 'Google'/'going'; got %d results", len(results))
	}
}

func TestFindImpactedFiles_StrongHits_Numeric(t *testing.T) {
	root, _ := impactFixture(t,
		"team:\n  size: 42\n",
		map[string]string{
			"overview.md": "The team has 42 members across two offices.",
			"scores.md":   "We scored 42 points in the last round.",
		})
	results, err := groundtruth.FindImpactedFiles(root, "42", "team.size", nil)
	if err != nil {
		t.Fatalf("FindImpactedFiles: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	byFile := map[string]groundtruth.ImpactResult{}
	for _, r := range results {
		byFile[filepath.Base(r.Path)] = r
	}
	if byFile["overview.md"].StrongHits != 1 {
		t.Errorf("overview.md: want StrongHits=1 (context has 'team'), got %d", byFile["overview.md"].StrongHits)
	}
	if byFile["scores.md"].StrongHits != 0 {
		t.Errorf("scores.md: want StrongHits=0 (no key-path word in context), got %d", byFile["scores.md"].StrongHits)
	}
}

func TestFindImpactedFiles_RespectsIgnore(t *testing.T) {
	root, _ := impactFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		map[string]string{
			"README.md":        "We use Go for the backend.",
			"internal/notes.md": "Also uses Go internally.",
		})
	ig := ignore.CompileIgnoreLines("internal/")
	results, err := groundtruth.FindImpactedFiles(root, "Go", "tech_stack.primary_language", ig)
	if err != nil {
		t.Fatalf("FindImpactedFiles: %v", err)
	}
	for _, r := range results {
		if strings.Contains(r.Path, "internal") {
			t.Errorf("internal/ should be skipped by ignore rules, got %s", r.Path)
		}
	}
	if len(results) != 1 {
		t.Errorf("want 1 result (README.md only), got %d", len(results))
	}
}
