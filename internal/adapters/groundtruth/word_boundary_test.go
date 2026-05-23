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

// boundaryFixture sets up a project with a single forbidden rule.
// truthDir is the default ".leonard/ground-truth/". Returns the
// adapter + the resolved project root.
func boundaryFixture(t *testing.T, rule string) (*groundtruth.GroundTruthAdapter, string) {
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
		"do-not-claim.md": rule,
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

// runPreEdit is a tiny convenience that returns the decision for a
// given content string. Hides the boilerplate of constructing the
// payload + writing a target file path.
func runPreEdit(t *testing.T, a *groundtruth.GroundTruthAdapter, root, content string) adapters.Decision {
	t.Helper()
	out, err := a.PreEdit(context.Background(), adapters.PreEditPayload{
		Tool:     "Write",
		FilePath: filepath.Join(root, "draft.md"),
		Content:  content,
	})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	return out.Decision
}

// TestBoundary_RuleMatchesWholeWord confirms an exact whole-word
// match still fires. Sanity check that the new boundary logic
// doesn't break the happy path.
func TestBoundary_RuleMatchesWholeWord(t *testing.T) {
	rule := "## Compliance\n\n- ❌ \"HIPAA-compliant\" — Not certified.\n"
	a, root := boundaryFixture(t, rule)
	got := runPreEdit(t, a, root, "Our platform is HIPAA-compliant for healthcare.")
	if got != adapters.Deny {
		t.Errorf("whole-word match: want Deny, got %v", got)
	}
}

// TestBoundary_RuleDoesNotMatchWordStem covers the dogfood #85
// scenario: a rule needle that ends with a word char shouldn't
// match when the haystack continues into another word.
//
// Specifically: rule "Terraform" should NOT match haystack
// "Terraforming code" — the m extends the word past the needle
// boundary.
func TestBoundary_RuleDoesNotMatchWordStem(t *testing.T) {
	rule := "## Technology\n\n- ❌ \"Terraform\" — Zero production Terraform.\n"
	a, root := boundaryFixture(t, rule)

	// Match: "I shipped production Terraform." → standalone word
	if got := runPreEdit(t, a, root, "I shipped production Terraform."); got != adapters.Deny {
		t.Errorf("standalone 'Terraform': want Deny, got %v", got)
	}

	// No match: "Terraforming code" — boundary missing (next char is 'i')
	if got := runPreEdit(t, a, root, "We work on Terraforming code daily."); got != adapters.Pass {
		t.Errorf("'Terraforming' should NOT trigger 'Terraform' rule: got %v", got)
	}

	// No match: "preterraform legacy" — boundary missing (prev char is 'e')
	if got := runPreEdit(t, a, root, "Our preterraform legacy is documented."); got != adapters.Pass {
		t.Errorf("'preterraform' should NOT trigger 'Terraform' rule: got %v", got)
	}
}

// TestBoundary_QuotedRuleAllowsMidPunctuation: when the rule needle
// has internal punctuation, that doesn't affect boundary anchoring.
// The rule "I shipped Go" should still match "I shipped Go in 2024."
// because the boundary is at "Go" (word) → space (non-word).
func TestBoundary_QuotedRuleAllowsMidPunctuation(t *testing.T) {
	rule := "## Tech\n\n- ❌ \"I shipped Go\" — Aspirational.\n"
	a, root := boundaryFixture(t, rule)
	if got := runPreEdit(t, a, root, "I shipped Go in 2024."); got != adapters.Deny {
		t.Errorf("standalone 'Go': want Deny, got %v", got)
	}
	if got := runPreEdit(t, a, root, "I shipped Golang in 2024."); got != adapters.Pass {
		t.Errorf("'Golang' should NOT trigger 'Go' rule: got %v", got)
	}
}

// TestBoundary_PunctuationEdgesSkipBoundary: when the rule needle
// starts or ends with a non-word character, no boundary is required
// on that side. This handles rules like "❌ \"...phrase\"" where the
// quote marks are part of the needle.
func TestBoundary_PunctuationEdgesSkipBoundary(t *testing.T) {
	// Needle " — Not certified" starts with space (non-word), so
	// left boundary isn't required. Right boundary is at 'd' so
	// we need the next char to be non-word.
	rule := "## Compliance\n\n- ❌ \" — Not certified\" — Reason.\n"
	a, root := boundaryFixture(t, rule)

	// This contains the needle verbatim with newline after — should
	// match (newline is non-word).
	content := "Some claim — Not certified\nMore text."
	if got := runPreEdit(t, a, root, content); got != adapters.Deny {
		t.Errorf("punctuation-edge needle: want Deny, got %v", got)
	}
}
