package groundtruth

import "strings"

// findFuzzyOccurrences scans haystack for windows whose
// Levenshtein distance to needle is at most threshold. Returns the
// matched span(s) — start/end byte offsets into haystack — in
// source order, non-overlapping.
//
// Two-pass strategy so exact matches always win over near-paraphrases:
//
//  1. First scan: case-insensitive exact substring matches.
//     Cheap (findAllOccurrences) and produces the tightest possible
//     span. An exact substring of the rule is always preferred over
//     a longer fuzzy window that includes extra surrounding text.
//
//  2. Second scan: fuzzy windows. Skip any candidate that overlaps
//     a span already emitted by the exact pass. The exact pass owns
//     the "obvious" matches; fuzzy fills in genuine paraphrases.
//
// Threshold 0 returns only the exact pass — no reason to allocate
// the DP table.
func findFuzzyOccurrences(haystack, needle string, threshold int) []span {
	if needle == "" {
		return nil
	}
	exact := findAllOccurrences(haystack, needle)
	if threshold <= 0 {
		return exact
	}

	hLower := strings.ToLower(haystack)
	nLower := strings.ToLower(needle)
	hLen := len(hLower)
	nLen := len(nLower)
	if hLen == 0 {
		return exact
	}

	minW := nLen - threshold
	if minW < 1 {
		minW = 1
	}
	maxW := nLen + threshold
	if maxW > hLen {
		maxW = hLen
	}

	// Try window sizes from the rule's exact length outward: nLen,
	// nLen+1, nLen-1, nLen+2, nLen-2, ... This makes the exact-
	// length window the preferred match (distance 0 short-circuits
	// the search), so an exact substring of the rule produces an
	// exact-length span rather than a truncated one.
	sizes := orderedWindowSizes(nLen, minW, maxW)

	var fuzzy []span
	i := 0
	for i <= hLen-minW {
		// Skip past any exact-pass span at this position. Fuzzy
		// shouldn't re-emit what exact already caught, and
		// shouldn't overlap an exact match with a slightly
		// different window.
		if overlap, ok := spanOverlap(i, i+minW, exact); ok {
			i = overlap.end
			continue
		}

		matched := false
		for _, w := range sizes {
			if i+w > hLen {
				continue
			}
			if _, hits := spanOverlap(i, i+w, exact); hits {
				continue
			}
			window := hLower[i : i+w]
			if levenshtein(window, nLower, threshold) <= threshold {
				// #85: same word-boundary rule as findAllOccurrences
				// — fuzzy windows that extend past a word edge of
				// the needle shouldn't claim a match. The needle's
				// edges drive the requirement; punctuation-edged
				// needles skip the check on that side.
				if !hasWordBoundaries(haystack, i, i+w, needle) {
					continue
				}
				// F016: when the window grew beyond the needle's
				// length (one-insertion match), the first extra
				// character at the right edge must not be a word
				// char while the needle itself ends on a word char.
				// Without this check, "Scrum mastery" (w=13)
				// matches rule "Scrum master" (nLen=12) because
				// hasWordBoundaries looks at haystack[end] = ' '
				// (space after "mastery") instead of the 'y' that
				// extends the match into the longer word.
				if w > nLen && isWordByte(needle[nLen-1]) {
					if edgePos := i + nLen; edgePos < hLen && isWordByte(haystack[edgePos]) {
						continue
					}
				}
				// F014: reject numeric near-misses. A 1-edit
				// change that modifies a digit (e.g. "53 releases"
				// vs rule "52 releases") is a semantically
				// different fact, not a paraphrase.
				if containsNumericChange(window, nLower) {
					continue
				}
				fuzzy = append(fuzzy, span{start: i, end: i + w})
				i += w
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}

	if len(fuzzy) == 0 {
		return exact
	}
	return mergeSpans(exact, fuzzy)
}

// spanOverlap checks whether [start, end) intersects any span in
// spans. Returns the first overlapping span and true on hit.
func spanOverlap(start, end int, spans []span) (span, bool) {
	for _, s := range spans {
		if start < s.end && end > s.start {
			return s, true
		}
	}
	return span{}, false
}

// mergeSpans returns a slice containing both inputs in source order.
// Both inputs are assumed already non-overlapping internally; the
// only overlap concern (exact vs fuzzy) is enforced at fuzzy-emit
// time by spanOverlap above.
func mergeSpans(a, b []span) []span {
	out := make([]span, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].start <= b[j].start {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// orderedWindowSizes returns window sizes in preference order:
// the target length first, then alternating one-step expansions
// (target+1, target-1, target+2, target-2, ...), clamped to
// [minW, maxW]. Ensures exact-length matches are preferred over
// nearby fuzzy ones.
func orderedWindowSizes(target, minW, maxW int) []int {
	if target < minW {
		target = minW
	}
	if target > maxW {
		target = maxW
	}
	out := []int{target}
	for step := 1; ; step++ {
		added := false
		if hi := target + step; hi <= maxW {
			out = append(out, hi)
			added = true
		}
		if lo := target - step; lo >= minW {
			out = append(out, lo)
			added = true
		}
		if !added {
			break
		}
	}
	return out
}

// levenshtein computes the Levenshtein distance between a and b,
// bounded above by `cap`. When the partial distance exceeds cap the
// function returns cap+1 (early exit). Standard two-row DP, O(min(n,m))
// space.
func levenshtein(a, b string, cap int) int {
	if a == b {
		return 0
	}
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Diff in length is a lower bound on the distance — short-circuit
	// before allocating the DP table.
	diff := la - lb
	if diff < 0 {
		diff = -diff
	}
	if diff > cap {
		return cap + 1
	}

	// Ensure a is the longer string so the row buffer scales with min.
	if la < lb {
		a, b = b, a
		la, lb = lb, la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			c := del
			if ins < c {
				c = ins
			}
			if sub < c {
				c = sub
			}
			curr[j] = c
			if c < rowMin {
				rowMin = c
			}
		}
		// Early exit: if every value in this row exceeds cap, no
		// cell below can drop below it (distance is non-decreasing
		// in i for the rest of the column).
		if rowMin > cap {
			return cap + 1
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// containsNumericChange reports whether the digit sequences in window
// differ from those in needle. When the only Levenshtein edit modifies a
// digit character (e.g. "53 releases" vs rule "52 releases"), the match is
// a numeric near-miss — a semantically different fact, not a typo — and the
// fuzzy pass should reject it.
func containsNumericChange(window, needle string) bool {
	needleRuns := digitRuns(needle)
	if len(needleRuns) == 0 {
		return false
	}
	windowRuns := digitRuns(window)
	if len(needleRuns) != len(windowRuns) {
		return true
	}
	for i := range needleRuns {
		if needleRuns[i] != windowRuns[i] {
			return true
		}
	}
	return false
}

// digitRuns returns the contiguous digit substrings of s in order.
func digitRuns(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			out = append(out, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}
