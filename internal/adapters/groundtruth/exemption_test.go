package groundtruth_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
	"github.com/jasondillingham/leonard/internal/config"
)

// exemptionFixture sets up a project with a populated truth tree at
// truth_dir and a do-not-claim rule the test relies on. Returns the
// adapter + the resolved (canonical) project root so tests can construct
// matching absolute paths.
func exemptionFixture(t *testing.T, truthSubdir string, exemptPaths []string) (*groundtruth.GroundTruthAdapter, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	gtDir := filepath.Join(root, truthSubdir)
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
	if err := config.WriteAdapterTrust(resolved, "ground-truth"); err != nil {
		t.Fatalf("WriteAdapterTrust: %v", err)
	}

	raw := map[string]any{"truth_dir": truthSubdir}
	if len(exemptPaths) > 0 {
		paths := make([]any, len(exemptPaths))
		for i, p := range exemptPaths {
			paths[i] = p
		}
		raw["exempt_paths"] = paths
	}

	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: resolved,
		Raw:         raw,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter), resolved
}

// TestExempt_TruthFileBypassesMatcher covers issue #84: a forbidden
// phrase that appears inside the truth tree (e.g. as part of a rule
// the operator is editing) must not trigger the matcher when the
// edit's TARGET is itself a truth-tree file.
func TestExempt_TruthFileBypassesMatcher(t *testing.T) {
	a, root := exemptionFixture(t, "source-of-truth", nil)

	// PreEdit targeting the do-not-claim.md file itself with content
	// that quotes the existing rule's forbidden phrase. Without the
	// exemption, the matcher would deny — the rule's own quoted text
	// is the forbidden phrase. With the exemption, edits to truth
	// files always Pass.
	target := filepath.Join(root, "source-of-truth", "do-not-claim.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Edit",
		FilePath: target,
		Content:  `## Compliance` + "\n\n" + `- ❌ "HIPAA-compliant" — Updated reason text.` + "\n",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("truth-file edit: want Pass, got %v (reason=%q)", out.Decision, out.Reason)
	}
}

// TestExempt_NonTruthFileStillBlocks confirms the matcher still fires
// on files OUTSIDE the truth tree. We don't want the exemption to
// over-reach.
func TestExempt_NonTruthFileStillBlocks(t *testing.T) {
	a, root := exemptionFixture(t, "source-of-truth", nil)

	target := filepath.Join(root, "applications", "draft.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: target,
		Content:  "Our platform is HIPAA-compliant for healthcare.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("normal-file edit with forbidden claim: want Deny, got %v", out.Decision)
	}
}

// TestExempt_OperatorAllowlist covers issue #8: the exempt_paths
// config setting lets operators name additional meta-files that
// legitimately quote forbidden phrases as commentary (dogfood log,
// audit notes, etc.).
func TestExempt_OperatorAllowlist(t *testing.T) {
	a, root := exemptionFixture(t, "source-of-truth", []string{"leonard-dogfood.md"})

	target := filepath.Join(root, "leonard-dogfood.md")
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: target,
		Content:  "Notes on the HIPAA-compliant rule — example reproducer for the matcher.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("exempt_paths-listed file: want Pass, got %v (reason=%q)", out.Decision, out.Reason)
	}
}

// TestExempt_GlobMatching covers the exempt_paths glob shapes:
//   - exact filename
//   - "**/" prefix for "any depth"
//   - directory wildcards
func TestExempt_GlobMatching(t *testing.T) {
	a, root := exemptionFixture(t, "source-of-truth", []string{
		"docs/**/notes.md",
		"audits/*.md",
	})

	// docs/x/y/notes.md — matches "docs/**/notes.md"
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "docs", "x", "y", "notes.md"),
		Content:  "HIPAA-compliant rule discussion.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("docs/x/y/notes.md should match docs/**/notes.md: got %v", out.Decision)
	}

	// audits/x.md — matches "audits/*.md"
	out, err = a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "audits", "x.md"),
		Content:  "HIPAA-compliant rule discussion.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != adapters.Pass {
		t.Errorf("audits/x.md should match audits/*.md: got %v", out.Decision)
	}

	// audits/sub/x.md — does NOT match "audits/*.md" (only direct children)
	out, err = a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "audits", "sub", "x.md"),
		Content:  "HIPAA-compliant rule discussion.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("audits/sub/x.md should NOT match audits/*.md (deeper than direct children): got %v", out.Decision)
	}
}

// TestExempt_EmptyConfigFallsBack confirms the exemption is purely
// additive — projects without exempt_paths configured behave as
// before for non-truth-dir files.
func TestExempt_EmptyConfigFallsBack(t *testing.T) {
	a, root := exemptionFixture(t, "source-of-truth", nil)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "leonard-dogfood.md"),
		Content:  "HIPAA-compliant.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != adapters.Deny {
		t.Errorf("no exempt_paths configured: leonard-dogfood.md should still be matched, got %v", out.Decision)
	}
}
