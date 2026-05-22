package groundtruth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/adapters/groundtruth"
)

// mcpAdapter constructs an adapter initialized against the named
// fixture under testdata/ — same shape as detectorTestAdapter but
// fixture-agnostic so list_facts tests can swap in a tree with
// private entries.
func mcpAdapter(t *testing.T, fixture string) *groundtruth.GroundTruthAdapter {
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
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init(%s): %v", fixture, err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter)
}

// ----- verify_claim -----

func TestVerifyClaim_VerifiedCarriesProvenance(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callVerifyClaim(t, a, "I shipped Go in production yesterday.")

	if got, want := out.Summary.Verified, 1; got < want {
		t.Fatalf("Summary.Verified: want >=%d, got %d", want, got)
	}
	var verified *groundtruth.VerifyClaimEntry
	for i := range out.Claims {
		if out.Claims[i].Verdict == "verified" {
			verified = &out.Claims[i]
			break
		}
	}
	if verified == nil {
		t.Fatalf("expected a verified claim, got %+v", out.Claims)
	}
	if verified.EvidencePath == "" {
		t.Error("verified claim must carry evidence_path")
	}
}

func TestVerifyClaim_ForbiddenCarriesRule(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callVerifyClaim(t, a, "We comply with SOC 2 already.")
	if out.Summary.Forbidden < 1 {
		t.Fatalf("expected forbidden hit, got %+v", out)
	}
	var f *groundtruth.VerifyClaimEntry
	for i := range out.Claims {
		if out.Claims[i].Verdict == "forbidden" {
			f = &out.Claims[i]
			break
		}
	}
	if f == nil || f.RulePath == "" || f.RuleText == "" {
		t.Errorf("forbidden claim missing provenance: %+v", f)
	}
}

func TestVerifyClaim_VerdictsAreLowercaseStrings(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callVerifyClaim(t, a, "I prefer Go. We have 50,000 users.")
	for _, c := range out.Claims {
		switch c.Verdict {
		case "verified", "unverified", "forbidden", "opinion":
			// expected
		default:
			t.Errorf("unexpected verdict on wire: %q (claim=%q)", c.Verdict, c.Text)
		}
	}
}

func TestVerifyClaim_EmptyTextReturnsEmpty(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callVerifyClaim(t, a, "")
	if len(out.Claims) != 0 {
		t.Errorf("empty text should produce zero claims, got %+v", out.Claims)
	}
}

// TestVerifyClaim_RejectsOversizedInput covers bughunt-11 F6:
// the MCP wrapper caps verify_claim input at 256 KiB so a runaway
// client can't make Detect chew on multi-MB payloads.
func TestVerifyClaim_RejectsOversizedInput(t *testing.T) {
	a := mcpAdapter(t, "valid")
	huge := strings.Repeat("x", 1<<19) // 512 KiB — well over the 256 KiB cap
	_, err := a.VerifyClaimCheckedForTest(groundtruth.VerifyClaimInput{Text: huge})
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("want cap-exceeded error, got %v", err)
	}
}

// ----- list_facts -----

func TestListFacts_EmptyCategoryReturnsTopLevelKeys(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callListFacts(t, a, groundtruth.ListFactsInput{})

	want := []string{"customers", "product", "tech_stack"}
	if !reflect.DeepEqual(out.Keys, want) {
		t.Errorf("top-level keys: want %v, got %v", want, out.Keys)
	}
	if out.Path != "" {
		t.Errorf("Path on empty category: want \"\", got %q", out.Path)
	}
}

func TestListFacts_SpecificCategoryReturnsSubtree(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callListFacts(t, a, groundtruth.ListFactsInput{Category: "tech_stack"})

	if got := out.Facts["primary_language"]; got != "Go" {
		t.Errorf("tech_stack.primary_language: want %q, got %v", "Go", got)
	}
	if out.Path != "tech_stack" {
		t.Errorf("Path: want %q, got %q", "tech_stack", out.Path)
	}
}

func TestListFacts_DottedCategoryDescends(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callListFacts(t, a, groundtruth.ListFactsInput{Category: "product"})

	// product is a map containing 'name' and 'features'
	if _, ok := out.Facts["name"]; !ok {
		t.Errorf("product subtree missing 'name': %+v", out.Facts)
	}
}

func TestListFacts_UnknownCategoryReturnsEmpty(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callListFacts(t, a, groundtruth.ListFactsInput{Category: "no_such_key"})

	if len(out.Facts) != 0 {
		t.Errorf("unknown category should produce empty Facts, got %+v", out.Facts)
	}
	if out.Path != "no_such_key" {
		t.Errorf("Path should echo requested category, got %q", out.Path)
	}
}

func TestListFacts_PrivateFilterDropsPrivateEntries(t *testing.T) {
	a := mcpAdapter(t, "private")

	// Without include_private, the internal_metrics object (which is
	// itself marked sensitivity: private) should be filtered out, and
	// the private acme-corp entry inside customers should be dropped
	// from the slice.
	out := callListFacts(t, a, groundtruth.ListFactsInput{})

	if _, ok := out.Facts["internal_metrics"]; ok {
		t.Error("internal_metrics is sensitivity:private and should be filtered")
	}

	customers, ok := out.Facts["customers"].([]any)
	if !ok {
		t.Fatalf("customers not a slice: %T", out.Facts["customers"])
	}
	for _, c := range customers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["id"].(string); id == "acme-corp" {
			t.Errorf("private customer acme-corp leaked through filter: %+v", m)
		}
	}
}

func TestListFacts_IncludePrivateReturnsEverything(t *testing.T) {
	a := mcpAdapter(t, "private")
	out := callListFacts(t, a, groundtruth.ListFactsInput{IncludePrivate: true})

	if _, ok := out.Facts["internal_metrics"]; !ok {
		t.Error("include_private=true should retain internal_metrics")
	}
}

func TestListFacts_EmptyFactsTreeReturnsEmpty(t *testing.T) {
	// Bare tempdir: Init succeeds with no ground-truth/ subdir, all
	// trees empty.
	tmp := t.TempDir()
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	gta := a.(*groundtruth.GroundTruthAdapter)
	out := callListFacts(t, gta, groundtruth.ListFactsInput{})
	if len(out.Facts) != 0 {
		t.Errorf("empty tree should return empty Facts, got %+v", out.Facts)
	}
}

// ----- get_story -----

func TestGetStory_ReturnsCanonicalText(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callGetStory(t, a, "2024 product launch")
	if !strings.Contains(out.Short, "Pro tier") {
		t.Errorf("Short missing expected text: %q", out.Short)
	}
	if out.Sensitivity != "public-safe" {
		t.Errorf("Sensitivity: want %q, got %q", "public-safe", out.Sensitivity)
	}
	if len(out.DoNotDrift) == 0 {
		t.Error("DoNotDrift should not be empty for the fixture story")
	}
	if out.Line == 0 {
		t.Error("Line should carry source location")
	}
}

func TestGetStory_CaseInsensitiveLookup(t *testing.T) {
	a := mcpAdapter(t, "valid")
	out := callGetStory(t, a, "FOUNDING")
	if out.Name != "Founding" {
		t.Errorf("Name should preserve original case, got %q", out.Name)
	}
}

func TestGetStory_MissingReturnsError(t *testing.T) {
	a := mcpAdapter(t, "valid")
	_, err := a.GetStoryForTest(groundtruth.GetStoryInput{Name: "nonexistent"})
	if err == nil {
		t.Fatal("missing story should return error, not empty success")
	}
	if !errors.Is(err, groundtruth.ErrStoryNotFound) {
		t.Errorf("error should match ErrStoryNotFound; got %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the missing story: %v", err)
	}
}

// ----- RegisterTools wiring -----

func TestRegisterTools_RejectsNilServer(t *testing.T) {
	a := mcpAdapter(t, "valid")
	if err := a.RegisterTools(nil); err == nil {
		t.Error("RegisterTools(nil) should return an error")
	}
}

// ----- Helpers -----

// Tests call into the unexported handler methods through these helper
// wrappers. The handlers themselves don't need to be exported on the
// public API surface — they're internal to the MCP wiring — but tests
// need a stable way to invoke them. We expose tiny test shims on the
// adapter in mcp_testhelpers_test.go.

func callVerifyClaim(t *testing.T, a *groundtruth.GroundTruthAdapter, text string) groundtruth.VerifyClaimOutput {
	t.Helper()
	return a.VerifyClaimForTest(groundtruth.VerifyClaimInput{Text: text})
}

func callListFacts(t *testing.T, a *groundtruth.GroundTruthAdapter, in groundtruth.ListFactsInput) groundtruth.ListFactsOutput {
	t.Helper()
	return a.ListFactsForTest(in)
}

func callGetStory(t *testing.T, a *groundtruth.GroundTruthAdapter, name string) groundtruth.GetStoryOutput {
	t.Helper()
	out, err := a.GetStoryForTest(groundtruth.GetStoryInput{Name: name})
	if err != nil {
		t.Fatalf("get_story(%q): %v", name, err)
	}
	return out
}
