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

// sessionStartFixture builds a minimal project root with a ground-truth
// tree and optional .md files. Returns the adapter (already Init'd).
func sessionStartFixture(t *testing.T, factsYAML, rules string, mdFiles map[string]string) *groundtruth.GroundTruthAdapter {
	t.Helper()
	root := t.TempDir()
	gtDir := filepath.Join(root, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      factsYAML,
		"stories.md":      "# Stories\n",
		"do-not-claim.md": rules,
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for rel, content := range mdFiles {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: root}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter)
}

// TestSessionStart_EmptyTruthTree confirms a project with no facts and
// no rules stays silent (no AdditionalContext).
func TestSessionStart_EmptyTruthTree(t *testing.T) {
	a := sessionStartFixture(t,
		"",     // empty facts
		"# rules\n", // no rules
		map[string]string{
			"doc.md": "Ordinary prose, nothing to flag.",
		},
	)
	res, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if res.AdditionalContext != "" {
		t.Errorf("empty truth tree: want silent output, got %q", res.AdditionalContext)
	}
}

// TestSessionStart_CleanProject confirms that a project with a non-empty
// truth tree but no findings emits a "no claim findings" line rather than
// staying silent — operators need to know Leonard ran.
func TestSessionStart_CleanProject(t *testing.T) {
	a := sessionStartFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		"# rules\n",
		map[string]string{
			"README.md": "Ordinary prose with no verifiable claims.",
		},
	)
	res, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if !strings.Contains(res.AdditionalContext, "no claim findings") {
		t.Errorf("clean project: want 'no claim findings' summary, got %q", res.AdditionalContext)
	}
}

// TestSessionStart_UnverifiedFindings confirms that .md files with
// unverified claims are counted and the summary nudges toward the
// list-stale-claims command.
func TestSessionStart_UnverifiedFindings(t *testing.T) {
	a := sessionStartFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		"# rules\n",
		map[string]string{
			"blog.md": "We have 50,000 users on the platform.",
		},
	)
	res, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if !strings.Contains(res.AdditionalContext, "unverified") {
		t.Errorf("unverified findings: want 'unverified' in output, got %q", res.AdditionalContext)
	}
	if !strings.Contains(res.AdditionalContext, "list-stale-claims") {
		t.Errorf("unverified findings: want list-stale-claims hint, got %q", res.AdditionalContext)
	}
}

// TestSessionStart_ForbiddenFindings confirms forbidden-claim files are
// flagged with higher urgency than unverified claims.
func TestSessionStart_ForbiddenFindings(t *testing.T) {
	a := sessionStartFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		"## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		map[string]string{
			"marketing.md": "Our platform is HIPAA-compliant for healthcare.",
		},
	)
	res, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if !strings.Contains(res.AdditionalContext, "forbidden") {
		t.Errorf("forbidden findings: want 'forbidden' in output, got %q", res.AdditionalContext)
	}
}

// TestSessionStart_ExcludesTruthDir confirms the truth tree itself is
// not scanned for claims (incident-1): do-not-claim.md matches its own
// rules by definition, and audit-log.md is machine-appended and grows
// without bound. Uses a non-hidden truth_dir — the default
// .leonard/ground-truth location is already pruned as a dotdir, which
// masked this for every project that kept the default.
func TestSessionStart_ExcludesTruthDir(t *testing.T) {
	root := t.TempDir()
	gtDir := filepath.Join(root, "source-of-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "tech_stack:\n  primary_language: Go\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		"filters.yaml":    "",
		// Machine-appended log inside the truth dir repeating the
		// forbidden phrase — must not be scanned.
		"audit-log.md": "logged: HIPAA-compliant claim denied\n",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Ordinary prose.\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	a := groundtruth.New()
	err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: root,
		Raw:         map[string]any{"truth_dir": "source-of-truth/"},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	res, err := a.(*groundtruth.GroundTruthAdapter).SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if strings.Contains(res.AdditionalContext, "forbidden") {
		t.Errorf("truth dir should be excluded from the scan; got forbidden in output: %q", res.AdditionalContext)
	}
	// Only README.md should have been scanned — the five truth-tree
	// files must not count toward the total.
	if !strings.Contains(res.AdditionalContext, "1 .md files") {
		t.Errorf("want exactly 1 file scanned (README.md), got %q", res.AdditionalContext)
	}
}

// TestSessionStart_CancelledContextReportsPartialScan confirms the scan
// observes ctx cancellation between files (incident-1: the scan ignored
// ctx entirely, so SIGTERM was trapped by main's NotifyContext while
// the loop spun on).
func TestSessionStart_CancelledContextReportsPartialScan(t *testing.T) {
	a := sessionStartFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		"# rules\n",
		map[string]string{
			"doc.md": "Ordinary prose.",
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := a.SessionStart(ctx, adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if !strings.Contains(res.AdditionalContext, "partial") {
		t.Errorf("cancelled ctx: want partial-scan note, got %q", res.AdditionalContext)
	}
	if !strings.Contains(res.AdditionalContext, "0 .md files") {
		t.Errorf("cancelled ctx: want 0 files scanned, got %q", res.AdditionalContext)
	}
}

// TestSessionStart_SkipsOversizeFiles confirms files past the
// per-file size cap are skipped rather than fed to the detector
// (incident-1: an 834 KB machine-generated .md dominated scan cost).
func TestSessionStart_SkipsOversizeFiles(t *testing.T) {
	big := strings.Repeat("Our platform is HIPAA-compliant for healthcare.\n", 1<<20/48+1)
	a := sessionStartFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		"## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		map[string]string{
			"huge-generated-log.md": big,
			"README.md":             "Ordinary prose.",
		},
	)
	res, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if strings.Contains(res.AdditionalContext, "forbidden") {
		t.Errorf("oversize file should be skipped; got forbidden in output: %q", res.AdditionalContext)
	}
	if !strings.Contains(res.AdditionalContext, "1 .md files") {
		t.Errorf("want exactly 1 file scanned (README.md), got %q", res.AdditionalContext)
	}
}

// TestSessionStart_SkipsBuildDirs confirms that directories like
// node_modules and vendor are excluded from the scan.
func TestSessionStart_SkipsBuildDirs(t *testing.T) {
	a := sessionStartFixture(t,
		"tech_stack:\n  primary_language: Go\n",
		"## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n",
		map[string]string{
			// Forbidden claim buried in a build dir — should be skipped.
			"node_modules/lib/README.md": "Our platform is HIPAA-compliant for healthcare.",
			// Clean file in the project root — no findings expected.
			"README.md": "Ordinary prose.",
		},
	)
	res, err := a.SessionStart(context.Background(), adapters.SessionStartPayload{})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if strings.Contains(res.AdditionalContext, "forbidden") {
		t.Errorf("node_modules should be skipped; got forbidden in output: %q", res.AdditionalContext)
	}
}
