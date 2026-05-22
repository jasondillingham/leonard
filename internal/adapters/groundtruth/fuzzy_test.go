package groundtruth

import "testing"

func TestLevenshtein_Identical(t *testing.T) {
	if got := levenshtein("hello", "hello", 5); got != 0 {
		t.Errorf("identical: want 0, got %d", got)
	}
}

func TestLevenshtein_SingleEdit(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"hello", "hallo", 1}, // substitution
		{"hello", "helo", 1},  // deletion
		{"hello", "helloo", 1}, // insertion
		{"hello", "", 5},      // all deletes
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b, 10); got != c.want {
			t.Errorf("levenshtein(%q, %q): want %d, got %d", c.a, c.b, c.want, got)
		}
	}
}

func TestLevenshtein_CapEarlyExit(t *testing.T) {
	// Strings far apart with a low cap should return cap+1.
	got := levenshtein("abcdefghij", "1234567890", 2)
	if got != 3 {
		t.Errorf("cap exit: want 3 (cap+1), got %d", got)
	}
}

func TestFindFuzzyOccurrences_ExactMatchPreferred(t *testing.T) {
	// When the input contains an exact substring of the needle,
	// the returned span should be the exact match — never an
	// extended fuzzy window with surrounding context.
	hay := "Our platform: HIPAA-compliant workflows out of the box."
	needle := "HIPAA-compliant workflows"
	got := findFuzzyOccurrences(hay, needle, 3)
	if len(got) != 1 {
		t.Fatalf("want 1 match, got %d (%+v)", len(got), got)
	}
	if hay[got[0].start:got[0].end] != needle {
		t.Errorf("exact-preferred: want %q, got %q", needle, hay[got[0].start:got[0].end])
	}
}

func TestFindFuzzyOccurrences_CatchesNearParaphrase(t *testing.T) {
	// Needle is "HIPAA-compliant"; input has "HIPPA-compliant"
	// (transposition). Should be a near-paraphrase, distance 2.
	hay := "Our platform is HIPPA-compliant for healthcare."
	needle := "HIPAA-compliant"
	got := findFuzzyOccurrences(hay, needle, 3)
	if len(got) < 1 {
		t.Fatalf("fuzzy near-paraphrase missed: %+v", got)
	}
}

func TestFindFuzzyOccurrences_ThresholdZeroIsExactOnly(t *testing.T) {
	hay := "we are HIPPA-compliant"
	got := findFuzzyOccurrences(hay, "HIPAA-compliant", 0)
	if len(got) != 0 {
		t.Errorf("threshold 0 should not match paraphrase, got %+v", got)
	}
}

func TestFindFuzzyOccurrences_CaseInsensitive(t *testing.T) {
	hay := "WE ARE hipaa-COMPLIANT"
	got := findFuzzyOccurrences(hay, "HIPAA-compliant", 0)
	if len(got) != 1 {
		t.Errorf("case-insensitive: want 1 match, got %d", len(got))
	}
}

func TestFindFuzzyOccurrences_EmptyNeedle(t *testing.T) {
	got := findFuzzyOccurrences("hi there", "", 3)
	if got != nil {
		t.Errorf("empty needle should return nil, got %+v", got)
	}
}

func TestFindFuzzyOccurrences_EmptyHaystack(t *testing.T) {
	got := findFuzzyOccurrences("", "needle", 3)
	if len(got) != 0 {
		t.Errorf("empty haystack should return no matches, got %+v", got)
	}
}

func TestExtractFuzzAnnotation_FuzzN(t *testing.T) {
	body, fuzz := extractFuzzAnnotation(`"We are HIPAA-compliant" — Not certified. {fuzz: 5}`)
	if fuzz != 5 {
		t.Errorf("threshold: want 5, got %d", fuzz)
	}
	if want := `"We are HIPAA-compliant" — Not certified.`; body != want {
		t.Errorf("body: want %q, got %q", want, body)
	}
}

func TestExtractFuzzAnnotation_Exact(t *testing.T) {
	body, fuzz := extractFuzzAnnotation(`"Mobile app launching" {fuzz: exact}`)
	if fuzz != 0 {
		t.Errorf("exact: want 0, got %d", fuzz)
	}
	if want := `"Mobile app launching"`; body != want {
		t.Errorf("body: want %q, got %q", want, body)
	}
}

func TestExtractFuzzAnnotation_NoAnnotation(t *testing.T) {
	body, fuzz := extractFuzzAnnotation(`"We are HIPAA-compliant" — Not certified.`)
	if fuzz != DefaultFuzzThreshold {
		t.Errorf("default: want %d, got %d", DefaultFuzzThreshold, fuzz)
	}
	if want := `"We are HIPAA-compliant" — Not certified.`; body != want {
		t.Errorf("body unchanged: want %q, got %q", want, body)
	}
}

func TestExtractFuzzAnnotation_BadValueFallsBack(t *testing.T) {
	body, fuzz := extractFuzzAnnotation(`"text" {fuzz: kebab}`)
	if fuzz != DefaultFuzzThreshold {
		t.Errorf("unparseable value: want default %d, got %d", DefaultFuzzThreshold, fuzz)
	}
	// Body keeps the annotation so operator sees their typo
	// when they re-read the file.
	if body == `"text"` {
		t.Error("unparseable annotation should NOT be stripped")
	}
}

func TestExtractFuzzAnnotation_NotAFuzzAnnotation(t *testing.T) {
	// {something} that isn't fuzz: should not be consumed.
	body, fuzz := extractFuzzAnnotation(`"text" {note: keep this}`)
	if fuzz != DefaultFuzzThreshold {
		t.Errorf("non-fuzz annotation: want default %d, got %d", DefaultFuzzThreshold, fuzz)
	}
	if body != `"text" {note: keep this}` {
		t.Errorf("non-fuzz annotation should pass through: %q", body)
	}
}

func TestParseRuleBody_PicksUpFuzz(t *testing.T) {
	rule := parseRuleBody(`"We are HIPAA-compliant" — Not certified. {fuzz: 5}`)
	if rule.FuzzThreshold != 5 {
		t.Errorf("FuzzThreshold: want 5, got %d", rule.FuzzThreshold)
	}
	if rule.Text != `We are HIPAA-compliant` {
		t.Errorf("Text: want %q, got %q", "We are HIPAA-compliant", rule.Text)
	}
}

func TestParseRuleBody_ExactKeepsTextClean(t *testing.T) {
	rule := parseRuleBody(`"Mobile app launching" {fuzz: exact}`)
	if rule.FuzzThreshold != 0 {
		t.Errorf("FuzzThreshold for exact: want 0, got %d", rule.FuzzThreshold)
	}
	if rule.Text != "Mobile app launching" {
		t.Errorf("Text: want %q, got %q", "Mobile app launching", rule.Text)
	}
}

func TestOrderedWindowSizes_TargetFirst(t *testing.T) {
	got := orderedWindowSizes(10, 7, 13)
	if got[0] != 10 {
		t.Errorf("target first: want 10, got %d", got[0])
	}
	// Should include sizes 11, 9, 12, 8, 13, 7 in some interleaved order.
	want := map[int]bool{10: true, 9: true, 11: true, 8: true, 12: true, 7: true, 13: true}
	have := map[int]bool{}
	for _, w := range got {
		have[w] = true
	}
	for w := range want {
		if !have[w] {
			t.Errorf("missing size %d", w)
		}
	}
}
