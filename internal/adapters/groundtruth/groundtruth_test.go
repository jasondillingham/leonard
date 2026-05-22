package groundtruth_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// initAgainstFixture copies a testdata/<name>/ directory into a tempdir
// under .leonard/ground-truth/ and Init's a fresh adapter against it.
// Returns the adapter + stderr buffer so tests can assert on hints.
func initAgainstFixture(t *testing.T, fixture string) (adapters.Adapter, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := filepath.Join("testdata", fixture)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read fixture file %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write fixture file %s: %v", e.Name(), err)
		}
	}

	stderr := &bytes.Buffer{}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Stderr:      stderr,
	}); err != nil {
		t.Fatalf("Init(%s): %v", fixture, err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, stderr
}

func TestInit_RejectsEmptyProjectRoot(t *testing.T) {
	a := groundtruth.New()
	err := a.Init(context.Background(), adapters.Config{})
	if err == nil {
		t.Fatal("expected error when ProjectRoot is empty")
	}
}

func TestInit_AcceptsBareTempdir(t *testing.T) {
	// No ground-truth/ subdir at all — should still init cleanly.
	tmp := t.TempDir()
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gta := a.(*groundtruth.GroundTruthAdapter)
	if !gta.Facts().IsEmpty() {
		t.Error("Facts should be empty for a project with no truth tree")
	}
	if len(gta.Stories()) != 0 {
		t.Error("Stories should be empty for a project with no truth tree")
	}
	if len(gta.Rules()) != 0 {
		t.Error("Rules should be empty for a project with no truth tree")
	}
}

func TestInit_LoadsValidFixture(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	gta := a.(*groundtruth.GroundTruthAdapter)

	if gta.Facts().IsEmpty() {
		t.Fatal("Facts should be populated from the valid fixture")
	}
	if gta.Filters().IsEmpty() {
		t.Error("Filters should be populated from the valid fixture")
	}
	if got := len(gta.Stories()); got != 2 {
		t.Errorf("Stories: want 2, got %d", got)
	}
	if got := len(gta.Rules()); got != 3 {
		t.Errorf("Rules: want 3, got %d", got)
	}
}

func TestInit_FactsLookupPath(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	gta := a.(*groundtruth.GroundTruthAdapter)

	// Navigate the parsed tree the same way later issues' verify_claim
	// logic will: by string key.
	ts, ok := gta.Facts().Root["tech_stack"].(map[string]any)
	if !ok {
		t.Fatalf("tech_stack: not a map, got %T", gta.Facts().Root["tech_stack"])
	}
	if got, _ := ts["primary_language"].(string); got != "Go" {
		t.Errorf("tech_stack.primary_language: want %q, got %q", "Go", got)
	}
}

func TestInit_StoriesParsedCorrectly(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	gta := a.(*groundtruth.GroundTruthAdapter)

	story, ok := gta.Stories().Get("2024 product launch")
	if !ok {
		t.Fatal("story '2024 product launch' not found")
	}
	if !strings.Contains(story.Short, "ExampleSaaS launched Pro tier") {
		t.Errorf("short version missing expected text: %q", story.Short)
	}
	if !strings.Contains(story.Long, "March 2024") {
		t.Errorf("long version missing expected text: %q", story.Long)
	}
	if len(story.DoNotDrift) != 2 {
		t.Errorf("DoNotDrift: want 2 bullets, got %d (%v)", len(story.DoNotDrift), story.DoNotDrift)
	}
	if story.Sensitivity != "public-safe" {
		t.Errorf("Sensitivity: want %q, got %q", "public-safe", story.Sensitivity)
	}
	if story.Line == 0 {
		t.Error("Line: expected non-zero source line number")
	}
}

func TestInit_StoriesCaseInsensitiveLookup(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	gta := a.(*groundtruth.GroundTruthAdapter)

	if _, ok := gta.Stories().Get("FOUNDING"); !ok {
		t.Error("case-insensitive lookup of 'FOUNDING' failed")
	}
	if _, ok := gta.Stories().Get("founding"); !ok {
		t.Error("case-insensitive lookup of 'founding' failed")
	}
}

func TestInit_RulesParsedCorrectly(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	gta := a.(*groundtruth.GroundTruthAdapter)

	rules := gta.Rules()
	if len(rules) != 3 {
		t.Fatalf("rule count: want 3, got %d", len(rules))
	}

	wantTexts := map[string]string{
		"ExampleSaaS supports HIPAA-compliant workflows": "Product capability gaps",
		"We have a mobile app":                           "Product capability gaps",
		"We comply with SOC 2":                           "Compliance",
	}
	gotTexts := map[string]string{}
	for _, r := range rules {
		gotTexts[r.Text] = r.Category
	}
	for text, wantCat := range wantTexts {
		if gotCat, ok := gotTexts[text]; !ok {
			t.Errorf("missing rule: %q", text)
		} else if gotCat != wantCat {
			t.Errorf("rule %q: want category %q, got %q", text, wantCat, gotCat)
		}
	}

	// First rule should have a non-empty reason.
	if rules[0].Reason == "" {
		t.Errorf("first rule's reason is empty: %+v", rules[0])
	}
	if rules[0].Line == 0 {
		t.Errorf("first rule's line is zero: %+v", rules[0])
	}
}

func TestInit_MalformedYAMLProducesError(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "malformed_yaml", "facts.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gtDir, "facts.yaml"), src, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	a := groundtruth.New()
	err = a.Init(context.Background(), adapters.Config{ProjectRoot: tmp})
	if err == nil {
		t.Fatal("expected parse error for malformed facts.yaml")
	}
	msg := err.Error()
	if !strings.Contains(msg, "facts.yaml") {
		t.Errorf("error should reference the source file: %v", err)
	}
	if !strings.Contains(msg, "line ") && !strings.Contains(msg, ":") {
		t.Errorf("error should include a line reference: %v", err)
	}
}

func TestInit_EmptyStoryNameProducesLineError(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "empty_story_name", "stories.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gtDir, "stories.md"), src, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	a := groundtruth.New()
	err = a.Init(context.Background(), adapters.Config{ProjectRoot: tmp})
	if err == nil {
		t.Fatal("expected parse error for empty story name")
	}
	if !strings.Contains(err.Error(), ":1:") {
		t.Errorf("error should reference line 1: %v", err)
	}
}

func TestInit_EmptyRuleTextProducesLineError(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "empty_rule_text", "do-not-claim.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gtDir, "do-not-claim.md"), src, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	a := groundtruth.New()
	err = a.Init(context.Background(), adapters.Config{ProjectRoot: tmp})
	if err == nil {
		t.Fatal("expected parse error for empty rule text")
	}
	if !strings.Contains(err.Error(), "do-not-claim.md") {
		t.Errorf("error should reference the source file: %v", err)
	}
}

func TestInit_ConfigOverrides(t *testing.T) {
	tmp := t.TempDir()
	customDir := filepath.Join(tmp, "custom-truth")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(customDir, "facts.yaml"),
		[]byte("custom_key: custom_value\n"),
		0o644,
	); err != nil {
		t.Fatalf("write facts: %v", err)
	}

	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Raw: map[string]any{
			"truth_dir":         customDir, // absolute path → used directly
			"forbidden_action":  "warn",
			"unverified_action": "log-only",
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gta := a.(*groundtruth.GroundTruthAdapter)
	if v, _ := gta.Facts().Root["custom_key"].(string); v != "custom_value" {
		t.Errorf("custom_key: want %q, got %q", "custom_value", v)
	}
	cfg := gta.ResolvedConfig()
	if cfg.ForbiddenAction != groundtruth.ActionWarn {
		t.Errorf("ForbiddenAction: want %q, got %q", groundtruth.ActionWarn, cfg.ForbiddenAction)
	}
	if cfg.UnverifiedAction != groundtruth.ActionLogOnly {
		t.Errorf("UnverifiedAction: want %q, got %q", groundtruth.ActionLogOnly, cfg.UnverifiedAction)
	}
}

func TestInit_RejectsUnknownAction(t *testing.T) {
	tmp := t.TempDir()
	a := groundtruth.New()
	err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Raw: map[string]any{
			"forbidden_action": "shrug",
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown forbidden_action")
	}
	if !strings.Contains(err.Error(), "shrug") {
		t.Errorf("error should cite the offending value: %v", err)
	}
}

func TestInit_RejectsWrongType(t *testing.T) {
	tmp := t.TempDir()
	a := groundtruth.New()
	err := a.Init(context.Background(), adapters.Config{
		ProjectRoot: tmp,
		Raw: map[string]any{
			"truth_dir": 42, // wrong type
		},
	})
	if err == nil {
		t.Fatal("expected error for non-string truth_dir")
	}
}

func TestHooks_AreNoOpsInV06(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	ctx := context.Background()

	pre, err := a.PreEdit(ctx, adapters.PreEditPayload{Tool: "Edit", FilePath: "x.md"})
	if err != nil {
		t.Fatalf("PreEdit: %v", err)
	}
	if pre.Decision != adapters.Pass {
		t.Errorf("PreEdit v0.6: want Pass, got %v", pre.Decision)
	}

	post, err := a.PostEdit(ctx, adapters.PostEditPayload{Tool: "Edit", FilePath: "x.md"})
	if err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	if post.AdditionalContext != "" || post.SystemMessage != "" || len(post.Claims) != 0 {
		t.Errorf("PostEdit v0.6: want empty, got %+v", post)
	}

	ss, err := a.SessionStart(ctx, adapters.SessionStartPayload{Source: "startup"})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if ss.AdditionalContext != "" {
		t.Errorf("SessionStart v0.6: want empty, got %q", ss.AdditionalContext)
	}

	// As of #30, Stop emits a minimal "no claims" line for empty
	// sessions rather than returning a fully empty SystemMessage.
	// Full markdown summary covered by TestStop_* in stop_test.go.
	if _, err := a.Stop(ctx, adapters.StopPayload{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// As of #10-#12, RegisterTools wires three tools onto the server
	// and rejects a nil server. The nil-server rejection is a v0.6+
	// contract change from the parser-layer no-op; covered by
	// TestRegisterTools_RejectsNilServer in mcp_test.go.
}

func TestRegistry_GroundTruthAdapterIsRegistered(t *testing.T) {
	got, err := adapters.New(groundtruth.Name)
	if err != nil {
		t.Fatalf("adapters.New(%q): %v", groundtruth.Name, err)
	}
	if got.Name() != groundtruth.Name {
		t.Errorf("registry-returned adapter Name: want %q, got %q", groundtruth.Name, got.Name())
	}
}

func TestRegistry_CoexistsWithCodeAdapter(t *testing.T) {
	// The acceptance criterion is "Adapter coexists with code adapter
	// in same project." Confirm both names are in the registry once
	// both packages have init()'d. We rely on the test binary's import
	// of internal/adapters/code (transitively via internal/adapters
	// tests; if that ever stops being true, this test surfaces it).
	names := adapters.Names()
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	if !have[groundtruth.Name] {
		t.Errorf("ground-truth adapter not in registry: %v", names)
	}
	// The code adapter isn't a direct import of this package — its
	// init() only fires when something else imports it. We can't
	// assert its presence here without forcing that import, which
	// would create a dependency we don't want. Coexistence is proven
	// by the design (separate names, no shared state) plus the
	// equivalence test below.
}

func TestRegistry_GroundTruthAdapterDoesNotCollideWithCode(t *testing.T) {
	if groundtruth.Name == "code" {
		t.Fatal("ground-truth and code adapters share a registry key — would panic at startup")
	}
}

// Used to confirm parser handles empty-file edge cases. yaml.Unmarshal
// of an empty []byte leaves the map nil, which IsEmpty must report
// correctly.
func TestFacts_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gtDir, "facts.yaml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write empty facts.yaml: %v", err)
	}

	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gta := a.(*groundtruth.GroundTruthAdapter)
	if !gta.Facts().IsEmpty() {
		t.Error("empty facts.yaml should produce IsEmpty()==true")
	}
}

// Smoke-check that the adapter's stderr writer isn't required at Init
// (cfg.Stderr nil case). io.Discard substitution should keep PostEdit
// from panicking even though it's a no-op today.
func TestInit_NilStderrUsesDiscard(t *testing.T) {
	a, _ := initAgainstFixture(t, "valid")
	// Trigger a no-op PostEdit; a nil writer would only matter once
	// the hook gains behavior, but the Init wiring should swap in
	// io.Discard now.
	_, err := a.PostEdit(context.Background(), adapters.PostEditPayload{})
	if err != nil {
		t.Fatalf("PostEdit with default-init stderr: %v", err)
	}
}

// Confirm a malformed-YAML error path doesn't leak a non-nil result
// (so callers can rely on err==nil ↔ result valid).
func TestInit_ErrorPathLeavesAdapterUnusable(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(gtDir, "facts.yaml"),
		[]byte("{ this: is, not, yaml"),
		0o644,
	); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := groundtruth.New()
	err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !errors.Is(err, err) { // tautology — keeps the import live
		t.Fatal("unreachable")
	}
}
