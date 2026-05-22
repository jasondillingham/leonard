package groundtruth_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// stopFixture posts a forbidden-finding PostEdit then runs Stop.
// Returns the Stop result for assertions on the markdown.
func stopFixture(t *testing.T, sessionID, content string) adapters.StopResult {
	t.Helper()
	a, _, target := postEditFixture(t, "page.md", content)
	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: sessionID,
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	out, err := a.Stop(context.Background(), adapters.StopPayload{
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	return out
}

func TestStop_EmitsMarkdownSummary(t *testing.T) {
	out := stopFixture(t, "s1", "Our marketing: ExampleSaaS supports HIPAA-compliant workflows.")
	if out.SystemMessage == "" {
		t.Fatal("Stop should emit a SystemMessage for sessions with findings")
	}
	for _, want := range []string{
		"## Ground-truth session summary",
		"finding(s)",
		"forbidden",
		"page.md",
		"leonard truth-story",
		"leonard truth-history",
	} {
		if !strings.Contains(out.SystemMessage, want) {
			t.Errorf("Stop summary missing %q in:\n%s", want, out.SystemMessage)
		}
	}
}

func TestStop_EmptySessionMinimal(t *testing.T) {
	a, _, _ := postEditFixture(t, "page.md", "irrelevant safe content")
	out, err := a.Stop(context.Background(), adapters.StopPayload{SessionID: "no-such-session"})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !strings.Contains(out.SystemMessage, "no claims flagged") {
		t.Errorf("empty session: want 'no claims flagged' minimal line, got %q", out.SystemMessage)
	}
	// Should NOT have section headers.
	if strings.Contains(out.SystemMessage, "## ") {
		t.Errorf("empty session should not emit sections, got:\n%s", out.SystemMessage)
	}
}

func TestStop_ScopesBySession(t *testing.T) {
	// Two PostEdits, two sessions. Stop on one shouldn't see
	// findings from the other.
	a, _, target := postEditFixture(t, "page.md",
		"ExampleSaaS supports HIPAA-compliant workflows.")
	for _, sid := range []string{"alpha", "beta"} {
		if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
			SessionID: sid, Tool: "Write", FilePath: target,
		}); err != nil {
			t.Fatalf("PostEdit %s: %v", sid, err)
		}
	}
	out, err := a.Stop(context.Background(), adapters.StopPayload{SessionID: "alpha"})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Exactly 1 finding (from alpha session), not 2.
	if !strings.Contains(out.SystemMessage, "**1** finding") {
		t.Errorf("Stop should scope to session alpha (1 finding); got:\n%s", out.SystemMessage)
	}
}

func TestStop_PerFileRollup(t *testing.T) {
	a, _, _ := postEditFixture(t, "page.md", "We have a mobile app already.")
	// Same adapter, post-edits to two different files. Use a
	// unique session so we isolate.
	sid := "rollup-session"
	files := map[string]string{
		"a.md": "ExampleSaaS supports HIPAA-compliant workflows.",
		"b.md": "We have a mobile app already.",
	}
	for name, content := range files {
		// Reuse postEditFixture's underlying adapter — write the
		// file directly into the project root.
		root := getProjectRoot(t, a)
		writeFileBytes(t, root, name, content)
		if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
			SessionID: sid, Tool: "Write", FilePath: pathJoin(root, name),
		}); err != nil {
			t.Fatalf("PostEdit %s: %v", name, err)
		}
	}
	out, err := a.Stop(context.Background(), adapters.StopPayload{SessionID: sid})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !strings.Contains(out.SystemMessage, "### By file") {
		t.Errorf("rollup section missing: %s", out.SystemMessage)
	}
	for _, name := range []string{"a.md", "b.md"} {
		if !strings.Contains(out.SystemMessage, name) {
			t.Errorf("file %q missing from rollup: %s", name, out.SystemMessage)
		}
	}
}
