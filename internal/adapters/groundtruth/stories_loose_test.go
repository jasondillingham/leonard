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

// TestStories_LooseHeading covers the v0.53 relaxation: the
// parser used to require "## STORY: <name>" and silently ignored
// every story under a bare "## <NAME>" heading. Real-world
// operator-authored stories.md files use the bare form. Now both
// shapes parse.
func TestStories_LooseHeading(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimum-viable adjacent files so the adapter's Init succeeds.
	for name, body := range map[string]string{
		"facts.yaml":      "x: 1\n",
		"do-not-claim.md": "# Rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Mirror the real-world shape: bare "## NAME" heading, sensitivity
	// inlined as a bold paragraph (not as a ### subsection — that part
	// won't parse, which is fine; the story itself still loads).
	stories := `# Stories

## 2019 RANSOMWARE RESPONSE

**Sensitivity:** Private artifacts only.

### Short version (~50 words)

In 2019, I caught an active SQL Server ransomware intrusion during a
routine early-morning health check.

### Long version (paragraph)

Long form here. Multiple sentences. Defense-in-depth via OS
heterogeneity worked.

### Do NOT add drift

- Don't claim "I prevented the encryption" — it had already spread.
- Don't specify the ransomware strain.

---

## STORY: PHISHGUARD

### Short version

PhishGuard is the Go-based anti-phishing classifier. 9,319 emails at
99.8% accuracy.
`
	if err := os.WriteFile(filepath.Join(gtDir, "stories.md"), []byte(stories), 0o644); err != nil {
		t.Fatal(err)
	}

	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	gta := a.(*groundtruth.GroundTruthAdapter)

	// Both stories should parse — the bare-heading one AND the
	// STORY:-prefixed one (backwards compat).
	if got := len(gta.Stories()); got != 2 {
		t.Fatalf("want 2 stories parsed, got %d (%+v)", got, gta.Stories())
	}

	ransomware, ok := gta.Stories().Get("2019 RANSOMWARE RESPONSE")
	if !ok {
		t.Fatal("loose-heading story not found")
	}
	if !strings.Contains(ransomware.Short, "SQL Server ransomware") {
		t.Errorf("short version missing expected text: %q", ransomware.Short)
	}
	if !strings.Contains(ransomware.Long, "Defense-in-depth") {
		t.Errorf("long version missing expected text: %q", ransomware.Long)
	}
	if len(ransomware.DoNotDrift) != 2 {
		t.Errorf("DoNotDrift: want 2 bullets, got %d (%v)", len(ransomware.DoNotDrift), ransomware.DoNotDrift)
	}

	// Backwards-compat: STORY: prefix still works and the prefix is
	// stripped from the name.
	phish, ok := gta.Stories().Get("PHISHGUARD")
	if !ok {
		t.Fatal("STORY:-prefixed lookup failed")
	}
	if !strings.Contains(phish.Short, "Go-based anti-phishing") {
		t.Errorf("PHISHGUARD short version missing expected text: %q", phish.Short)
	}
}
