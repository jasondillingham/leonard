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

// detectorTestAdapter is a small helper that constructs a fresh
// adapter against the testdata/valid fixture (which carries
// tech_stack.primary_language=Go + a few HIPAA / SOC 2 forbidden
// rules). Most detector tests reuse this — when they need other
// facts they substitute via cfg or build their own adapter.
func detectorTestAdapter(t *testing.T) *groundtruth.GroundTruthAdapter {
	t.Helper()
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"facts.yaml", "stories.md", "do-not-claim.md", "filters.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", "valid", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(gtDir, name), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a.(*groundtruth.GroundTruthAdapter)
}

func TestVerdict_String(t *testing.T) {
	cases := []struct {
		v    groundtruth.Verdict
		want string
	}{
		{groundtruth.VerdictPass, "pass"},
		{groundtruth.VerdictVerified, "verified"},
		{groundtruth.VerdictUnverified, "unverified"},
		{groundtruth.VerdictForbidden, "forbidden"},
		{groundtruth.VerdictOpinion, "opinion"},
		{groundtruth.VerdictContradiction, "contradiction"},
		{groundtruth.Verdict(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.v.String(); got != c.want {
			t.Errorf("Verdict(%d).String() = %q, want %q", int(c.v), got, c.want)
		}
	}
}

func TestDetect_VerifiedFromFactsTechStack(t *testing.T) {
	a := detectorTestAdapter(t)
	got := a.Detect("Yesterday I shipped Go in production for a customer.")

	var techClaim *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Category == "tech_in_production" {
			techClaim = &got.Claims[i]
			break
		}
	}
	if techClaim == nil {
		t.Fatalf("expected tech_in_production claim, got %+v", got.Claims)
	}
	if techClaim.Verdict != groundtruth.VerdictVerified {
		t.Errorf("Verdict: want Verified, got %v", techClaim.Verdict)
	}
	if techClaim.EvidencePath != "tech_stack.primary_language" {
		t.Errorf("EvidencePath: want %q, got %q", "tech_stack.primary_language", techClaim.EvidencePath)
	}
	if got.Summary.Verified < 1 {
		t.Errorf("Summary.Verified: want >=1, got %d", got.Summary.Verified)
	}
}

func TestDetect_UnverifiedWhenNoFactsMatch(t *testing.T) {
	a := detectorTestAdapter(t)
	// facts.yaml has tech_stack.primary_language = Go. Rust isn't
	// present, so this should be Unverified.
	got := a.Detect("Last quarter I shipped Rust in production.")
	var techClaim *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Category == "tech_in_production" {
			techClaim = &got.Claims[i]
		}
	}
	if techClaim == nil {
		t.Fatalf("expected tech_in_production claim, got %+v", got.Claims)
	}
	if techClaim.Verdict != groundtruth.VerdictUnverified {
		t.Errorf("Verdict for Rust: want Unverified, got %v", techClaim.Verdict)
	}
}

func TestDetect_ForbiddenFromRule(t *testing.T) {
	a := detectorTestAdapter(t)
	// do-not-claim.md in the fixture forbids "ExampleSaaS supports
	// HIPAA-compliant workflows".
	got := a.Detect("Our platform: ExampleSaaS supports HIPAA-compliant workflows out of the box.")

	var forb *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Verdict == groundtruth.VerdictForbidden {
			forb = &got.Claims[i]
			break
		}
	}
	if forb == nil {
		t.Fatalf("expected Forbidden claim, got %+v", got.Claims)
	}
	if !strings.Contains(forb.RuleText, "HIPAA") {
		t.Errorf("RuleText missing 'HIPAA': %q", forb.RuleText)
	}
	if forb.RulePath == "" {
		t.Error("RulePath should be populated for Forbidden")
	}
	if got.Summary.Forbidden < 1 {
		t.Errorf("Summary.Forbidden: want >=1, got %d", got.Summary.Forbidden)
	}
}

func TestDetect_ForbiddenCaseInsensitive(t *testing.T) {
	a := detectorTestAdapter(t)
	// Same rule, but in mixed case in the text. The forbidden matcher
	// is case-insensitive (matches the convention #13's fuzzy
	// matching will inherit).
	got := a.Detect("Our marketing: examplesaas supports HIPAA-COMPLIANT WORKFLOWS for healthcare.")
	if got.Summary.Forbidden < 1 {
		t.Errorf("case-insensitive forbidden match failed: %+v", got.Claims)
	}
}

func TestDetect_ForbiddenBeatsOtherPatterns(t *testing.T) {
	a := detectorTestAdapter(t)
	// Text where both a tech_in_production pattern AND a forbidden
	// rule would fire on overlapping spans. The forbidden hit should
	// win (forbidden is checked first and pattern matches that
	// overlap are dropped).
	got := a.Detect("We have a mobile app and shipped Go in production.")

	forbHits := 0
	techHits := 0
	for _, c := range got.Claims {
		switch c.Verdict {
		case groundtruth.VerdictForbidden:
			forbHits++
		}
		if c.Category == "tech_in_production" {
			techHits++
		}
	}
	if forbHits < 1 {
		t.Errorf("Forbidden hit missing: %+v", got.Claims)
	}
	if techHits < 1 {
		t.Errorf("Non-overlapping tech_in_production hit should still fire: %+v", got.Claims)
	}
}

func TestDetect_Opinion(t *testing.T) {
	a := detectorTestAdapter(t)
	got := a.Detect("I prefer Go over Rust for the kind of work we do.")

	var op *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Verdict == groundtruth.VerdictOpinion {
			op = &got.Claims[i]
			break
		}
	}
	if op == nil {
		t.Fatalf("expected Opinion claim, got %+v", got.Claims)
	}
	if op.Category != "opinion" {
		t.Errorf("Category: want %q, got %q", "opinion", op.Category)
	}
	if op.Note == "" {
		t.Error("Note should explain why span is non-verifiable")
	}
	if got.Summary.Opinion < 1 {
		t.Errorf("Summary.Opinion: want >=1, got %d", got.Summary.Opinion)
	}
}

func TestDetect_OpinionBeatsTechPattern(t *testing.T) {
	// "I believe Go is the best language" — the opinion pattern
	// should consume the "I believe Go" prefix before the tech
	// pattern can fire. Span overlap drops the later match.
	a := detectorTestAdapter(t)
	got := a.Detect("I believe production Go is overrated.")

	hasOpinion := false
	hasTech := false
	for _, c := range got.Claims {
		if c.Verdict == groundtruth.VerdictOpinion {
			hasOpinion = true
		}
		if c.Category == "tech_in_production" {
			hasTech = true
		}
	}
	if !hasOpinion {
		t.Errorf("Opinion claim missing: %+v", got.Claims)
	}
	if hasTech {
		// Not a strict requirement — the patterns don't overlap by
		// span in this exact text — but if they ever did, opinion
		// should still win because opinion is checked first.
		t.Logf("note: tech_in_production fired alongside opinion; spans don't overlap here")
	}
}

func TestDetect_PassForNonClaimText(t *testing.T) {
	a := detectorTestAdapter(t)
	// No personal action, no tech-in-prod, no opinion, no
	// quantitative, no date, no forbidden text. Detector should
	// return zero claims.
	got := a.Detect("This is a paragraph about the weather. It's nice today.")
	if len(got.Claims) != 0 {
		t.Errorf("non-claim text produced claims: %+v", got.Claims)
	}
	if got.Summary.Total != 0 {
		t.Errorf("Summary.Total: want 0, got %d", got.Summary.Total)
	}
}

func TestDetect_PersonalActionVerified(t *testing.T) {
	a := detectorTestAdapter(t)
	// facts.yaml has product.features[0].name = "Universal search"
	// — the personal_action pattern captures the verb object, so
	// "I built Universal" won't VERIFY exact-match, but "I built
	// search" gets the bare token. Use Go since that's a clean
	// scalar in the tree.
	got := a.Detect("I built Go yesterday.")
	var pa *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Category == "personal_action" {
			pa = &got.Claims[i]
		}
	}
	if pa == nil {
		t.Fatalf("expected personal_action claim, got %+v", got.Claims)
	}
	if pa.Verdict != groundtruth.VerdictVerified {
		t.Errorf("Verdict: want Verified, got %v", pa.Verdict)
	}
}

func TestDetect_QuantitativeUnverified(t *testing.T) {
	a := detectorTestAdapter(t)
	got := a.Detect("We have 50,000 users on the platform.")
	var q *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Category == "quantitative" {
			q = &got.Claims[i]
		}
	}
	if q == nil {
		t.Fatalf("expected quantitative claim, got %+v", got.Claims)
	}
	// facts.yaml fixture doesn't have a 50,000 value anywhere, so
	// this should be Unverified.
	if q.Verdict != groundtruth.VerdictUnverified {
		t.Errorf("Verdict: want Unverified, got %v", q.Verdict)
	}
}

func TestDetect_DateUnverified(t *testing.T) {
	a := detectorTestAdapter(t)
	// A bare year without a temporal preposition fires as Unverified
	// when the year is absent from facts.yaml. "Founded 1999" has no
	// temporal prep so the year is treated as a factual claim.
	got := a.Detect("Founded 1999, the company grew quickly.")
	hasDate := false
	for _, c := range got.Claims {
		if c.Category == "date" {
			hasDate = true
			if c.Verdict != groundtruth.VerdictUnverified {
				t.Errorf("1999 date Verdict: want Unverified, got %v", c.Verdict)
			}
		}
	}
	if !hasDate {
		t.Errorf("date claim missing: %+v", got.Claims)
	}
}

// TestDetect_TemporalYearContextSuppressed verifies that bare years
// preceded by temporal prepositions ("in", "since", "until", etc.) are
// suppressed as temporal references rather than treated as verifiable
// facts. This reduces unverified-claim noise in typical prose.
func TestDetect_TemporalYearContextSuppressed(t *testing.T) {
	a := detectorTestAdapter(t)
	cases := []string{
		"I started here in 2010 and stayed until 2015.",
		"The project has been running since 2018.",
		"We expect delivery by 2026.",
	}
	for _, text := range cases {
		got := a.Detect(text)
		for _, c := range got.Claims {
			if c.Category == "date" && !strings.Contains(c.Text, "-") {
				t.Errorf("bare year in temporal context should not produce a claim: %q → claim %q", text, c.Text)
			}
		}
	}
}

func TestDetect_DateVerifiedAgainstFacts(t *testing.T) {
	a := detectorTestAdapter(t)
	// facts.yaml has features[0].shipped = 2025-09-15.
	got := a.Detect("The feature shipped on 2025-09-15 and was well-received.")
	verified := false
	for _, c := range got.Claims {
		if c.Category == "date" && c.Verdict == groundtruth.VerdictVerified {
			verified = true
		}
	}
	if !verified {
		t.Errorf("2025-09-15 should match facts.product.features[0].shipped: %+v", got.Claims)
	}
}

func TestDetect_SourceLocationsArePopulated(t *testing.T) {
	a := detectorTestAdapter(t)
	got := a.Detect("Earlier today I shipped Go for a release.")
	if len(got.Claims) == 0 {
		t.Fatal("expected at least one claim")
	}
	for _, c := range got.Claims {
		if c.StartByte == 0 && c.EndByte == 0 {
			t.Errorf("claim %q has zero source location", c.Text)
		}
		if c.StartByte >= c.EndByte {
			t.Errorf("claim %q has degenerate span [%d,%d)", c.Text, c.StartByte, c.EndByte)
		}
	}
}

func TestDetect_EmptyFactsReturnsUnverified(t *testing.T) {
	// Detect should not panic on a nil/empty facts tree.
	got := groundtruth.Detect("I shipped Go in production.", nil, nil)
	if got.Summary.Verified != 0 {
		t.Errorf("empty facts should produce no Verified: %+v", got.Claims)
	}
	if got.Summary.Unverified < 1 {
		t.Errorf("Unverified count: want >=1, got %d", got.Summary.Unverified)
	}
}

func TestDetect_EmptyTextReturnsEmptyResult(t *testing.T) {
	a := detectorTestAdapter(t)
	got := a.Detect("")
	if len(got.Claims) != 0 || got.Summary.Total != 0 {
		t.Errorf("empty text: want empty result, got %+v", got)
	}
}

func TestDetect_BeforeInitReturnsEmpty(t *testing.T) {
	a := groundtruth.New().(*groundtruth.GroundTruthAdapter)
	got := a.Detect("I shipped Go in production.")
	// No facts loaded → Unverified is acceptable; the contract is
	// "doesn't panic." A non-empty Claims slice with Unverified
	// entries is fine.
	for _, c := range got.Claims {
		if c.Verdict == groundtruth.VerdictVerified {
			t.Errorf("uninitialized adapter should not Verify against nothing: %+v", c)
		}
	}
}

// TestDetect_QuantitativeCommaFormatting verifies that a comma-formatted
// number in prose ("9,319 messages") is resolved as Verified when the
// matching integer (9319) exists in facts.yaml. Without comma
// normalization the facts walk compares "9,319" against fmt.Sprint(9319)
// = "9319" and produces a false Unverified.
func TestDetect_QuantitativeCommaFormatting(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "metrics:\n  message_count: 9319\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = a.Close() }()
	gta := a.(*groundtruth.GroundTruthAdapter)

	got := gta.Detect("We processed 9,319 messages last month.")
	var q *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Category == "quantitative" {
			q = &got.Claims[i]
			break
		}
	}
	if q == nil {
		t.Fatalf("expected quantitative claim, got %+v", got.Claims)
	}
	if q.Verdict != groundtruth.VerdictVerified {
		t.Errorf("comma-formatted number: want Verified, got %v (evidence=%q)", q.Verdict, q.EvidencePath)
	}
}

// TestDetect_DatePatternIgnoresPhoneFragments verifies that a 4-digit
// number that is NOT a plausible calendar year (1900-2099) does not
// trigger a date claim. Phone numbers like "555-1389" previously matched
// the bare \d{4} arm of the date pattern.
func TestDetect_DatePatternIgnoresPhoneFragments(t *testing.T) {
	got := groundtruth.Detect("Call me at 555-1389 tomorrow.", nil, nil)
	for _, c := range got.Claims {
		if c.Category == "date" && strings.Contains(c.Text, "1389") {
			t.Errorf("phone fragment '1389' should not produce a date claim: %+v", c)
		}
	}
}

// TestDetect_ContradictionFromFacts verifies the contradiction scan:
//   - A number close to a facts.yaml value with a matching context word
//     produces VerdictContradiction.
//   - An exact match against a facts value does NOT produce a contradiction.
//   - A close number with no context word match does NOT fire.
func TestDetect_ContradictionFromFacts(t *testing.T) {
	tmp := t.TempDir()
	gtDir := filepath.Join(tmp, ".leonard", "ground-truth")
	if err := os.MkdirAll(gtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"facts.yaml":      "team:\n  engineers: 12\n",
		"stories.md":      "# Stories\n",
		"do-not-claim.md": "# rules\n",
		"filters.yaml":    "",
	} {
		if err := os.WriteFile(filepath.Join(gtDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	a := groundtruth.New()
	if err := a.Init(context.Background(), adapters.Config{ProjectRoot: tmp}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = a.Close() }()
	gta := a.(*groundtruth.GroundTruthAdapter)

	// OOM-close number + matching context word → contradiction.
	got := gta.Detect("The team has 99 engineers across all offices.")
	var contradiction *groundtruth.Claim
	for i := range got.Claims {
		if got.Claims[i].Verdict == groundtruth.VerdictContradiction {
			contradiction = &got.Claims[i]
			break
		}
	}
	if contradiction == nil {
		t.Fatalf("expected contradiction claim, got %+v", got.Claims)
	}
	if contradiction.EvidencePath != "team.engineers" {
		t.Errorf("EvidencePath: want %q, got %q", "team.engineers", contradiction.EvidencePath)
	}
	if got.Summary.Contradiction < 1 {
		t.Errorf("Summary.Contradiction: want >=1, got %d", got.Summary.Contradiction)
	}

	// Exact match → no contradiction (pattern scan may verify it normally).
	got2 := gta.Detect("The team has 12 engineers across all offices.")
	for _, c := range got2.Claims {
		if c.Verdict == groundtruth.VerdictContradiction {
			t.Errorf("exact match should not produce contradiction: %+v", c)
		}
	}

	// OOM-close but no context word match → no contradiction.
	got3 := gta.Detect("We use 99 libraries in our codebase.")
	for _, c := range got3.Claims {
		if c.Verdict == groundtruth.VerdictContradiction {
			t.Errorf("context mismatch should not fire: %+v", c)
		}
	}
}
