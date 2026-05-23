package groundtruth_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
	"github.com/jasondillingham/leonard/internal/config"
)

// denyReasonFixture writes a do-not-claim.md with a single rule
// whose body contains a distinctive substring the test can assert
// on. Returns the adapter + project root.
func denyReasonFixture(t *testing.T, ruleBody string) (*groundtruth.GroundTruthAdapter, string) {
	t.Helper()
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
		"do-not-claim.md": ruleBody,
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, _ := filepath.EvalSymlinks(root)
	if err := config.WriteAdapterTrust(resolved, "ground-truth"); err != nil {
		t.Fatal(err)
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: resolved}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter), resolved
}

// TestDenyReason_IncludesRuleBody covers issue #88: when the matcher
// fires, the deny reason must include the source rule body so the
// operator (and the model) can rewrite without context-switching to
// open do-not-claim.md.
func TestDenyReason_IncludesRuleBody(t *testing.T) {
	ruleBody := `## Compliance

- ❌ "HIPAA-compliant" — Not certified. Customers may add their own BAA layer.
`
	a, root := denyReasonFixture(t, ruleBody)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "draft.md"),
		Content:  "Our platform is HIPAA-compliant for healthcare.",
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if out.Decision != adapters.Deny {
		t.Fatalf("want Deny, got %v", out.Decision)
	}
	// The rule body's distinctive phrase should appear in the reason.
	if !strings.Contains(out.Reason, "Not certified") {
		t.Errorf("deny reason should include the rule body. got: %q", out.Reason)
	}
	// The rule reference should still be present.
	if !strings.Contains(out.Reason, "Compliance#1") {
		t.Errorf("deny reason should include the rule reference. got: %q", out.Reason)
	}
}

// TestDenyReason_IncludesLineLocation covers issue #86: when a
// multi-line edit has a forbidden claim deep inside, the deny
// message should include line + column info so the operator can
// localize which entry needs rewriting (rather than re-doing the
// whole multi-entry payload).
func TestDenyReason_IncludesLineLocation(t *testing.T) {
	ruleBody := `## Compliance

- ❌ "HIPAA-compliant" — Not certified.
`
	a, root := denyReasonFixture(t, ruleBody)

	// Four-entry payload; the third entry has the forbidden claim.
	// Operator should see "line 7" (or thereabouts) in the deny so
	// they know which entry to edit.
	content := `## Watchlist update

### Sophos
Filed 2026-05-22. Pass.

### MegaCorp
Our platform is HIPAA-compliant for healthcare.

### Underdog
Filed 2026-05-22. Pass.
`
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "draft.md"),
		Content:  content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != adapters.Deny {
		t.Fatalf("want Deny, got %v", out.Decision)
	}
	if !strings.Contains(out.Reason, "line ") {
		t.Errorf("deny reason should include line number, got: %q", out.Reason)
	}
	if !strings.Contains(out.Reason, "col ") {
		t.Errorf("deny reason should include column, got: %q", out.Reason)
	}
}

// TestDenyReason_MultiFindingHint covers issue #86's "1 of N" hint
// — when there are multiple forbidden findings, the operator should
// know that fixing this one will reveal another.
func TestDenyReason_MultiFindingHint(t *testing.T) {
	// Two rules. Content hits both. Operator should see "1 of 2".
	ruleBody := `## Compliance

- ❌ "HIPAA-compliant" — Not certified.

## Capability

- ❌ "mobile app" — Web only.
`
	a, root := denyReasonFixture(t, ruleBody)
	content := "We have a mobile app and we are HIPAA-compliant."
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "draft.md"),
		Content:  content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != adapters.Deny {
		t.Fatalf("want Deny, got %v", out.Decision)
	}
	if !strings.Contains(out.Reason, "of 2 forbidden") {
		t.Errorf("deny reason should include 'of N forbidden' hint, got: %q", out.Reason)
	}
}

// TestDenyReason_SingleFindingHasNoMultiHint confirms the multi-
// finding hint is suppressed when there's only one match. Keeps
// the message terse for the common case.
func TestDenyReason_SingleFindingHasNoMultiHint(t *testing.T) {
	ruleBody := `## Compliance

- ❌ "HIPAA-compliant" — Not certified.
`
	a, root := denyReasonFixture(t, ruleBody)
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "draft.md"),
		Content:  "We are HIPAA-compliant.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Reason, "forbidden findings") {
		t.Errorf("single-finding case should not include the multi-finding hint, got: %q", out.Reason)
	}
}

// TestDenyReason_TruncatesLongRuleBody confirms the rule snippet is
// capped at ruleSnippetMax (200 chars) with an ellipsis marker.
func TestDenyReason_TruncatesLongRuleBody(t *testing.T) {
	long := strings.Repeat("very long rule body that goes on and on. ", 20) // ~840 chars
	ruleBody := "## Compliance\n\n- ❌ \"HIPAA-compliant\" — " + long + "\n"
	a, root := denyReasonFixture(t, ruleBody)

	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "draft.md"),
		Content:  "Our platform is HIPAA-compliant for healthcare.",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reason field carries: prefix + claim + rule reference + snippet
	// (capped) + suffix. The capped snippet should NOT include the
	// full long body.
	if len(out.Reason) > 600 {
		t.Errorf("deny reason should be capped (rule snippet truncated), got %d chars", len(out.Reason))
	}
	if !strings.Contains(out.Reason, "…") {
		t.Errorf("truncated reason should include ellipsis marker, got: %q", out.Reason)
	}
}
