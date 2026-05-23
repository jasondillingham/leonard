package groundtruth

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// Rule is one entry from do-not-claim.md.
type Rule struct {
	// Category is the "## <heading>" the rule appears under
	// (e.g., "Product capability gaps").
	Category string

	// Text is the forbidden claim text. Stripped of leading "- ❌ "
	// markers, the optional {fuzz: ...} annotation, and trimmed.
	Text string

	// Reason is the explanation that follows the claim (separated
	// by " — " or " - " in the source). Surfaced to Claude as the
	// rule citation on deny.
	Reason string

	// FuzzThreshold is the Levenshtein distance tolerated when
	// matching this rule. 0 means exact match (case-insensitive).
	// Set by an optional `{fuzz: N}` or `{fuzz: exact}` annotation
	// at the end of the rule body in do-not-claim.md. The default
	// (when no annotation is present) is DefaultFuzzThreshold.
	FuzzThreshold int

	// Path / Line locate the rule in do-not-claim.md for error
	// messages. Line is 1-based.
	Path string
	Line int
}

// DefaultFuzzThreshold dropped from 3 to 1 in v0.53 (#85). The
// looser 3-edit threshold over-matched: "Terraforming" matched the
// "Terraform" rule (3 edits) because fuzzy expanded the window past
// the needle's right boundary even with word-boundary anchoring on
// the haystack edge. 1 catches typos ("Terraforn" → "Terraform")
// without grabbing word stems. Operators who want looser matching
// can opt in per-rule via {fuzz: N} annotation.
const DefaultFuzzThreshold = 1

// Rules is the parsed do-not-claim.md content.
type Rules []Rule

// loadRules reads path and parses it. A missing file returns an empty
// Rules (not an error).
//
// Parser shape:
//
//   - "## <heading>" lines set the current Category.
//   - "- ❌ <claim>" or "- ❌ \"<claim>\"" (with various dash chars and
//     quoting) define a Rule. The text after the marker, up to a
//     " — ", " - ", or end of line, is the claim Text. Anything after
//     the dash separator is the Reason.
//   - Lines without "- ❌" inside a section are ignored (operator
//     can write descriptive prose between rules).
//   - Multi-line rules: if the next line is indented (starts with two
//     spaces and isn't a new bullet or header), it's appended to the
//     prior rule's Reason — handles the common case where operators
//     break a long reason across lines.
func loadRules(path string) (Rules, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Rules{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	return parseRules(path, f)
}

// forbiddenMarkers are the bullet prefixes recognized as a forbidden-
// claim rule. ❌ is canonical; X, x, and the no-entry symbol are
// recognized too so an operator without easy access to ❌ can still
// author rules.
var forbiddenMarkers = []string{
	"❌",
	"🚫",
	"X",
	"x",
}

func parseRules(path string, r io.Reader) (Rules, error) {
	var out Rules
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		category string
		lineNum  int
	)

	for sc.Scan() {
		lineNum++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "## "):
			category = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))

		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			body := strings.TrimSpace(trimmed[2:])
			marker, rest, ok := detectMarker(body)
			if !ok {
				continue
			}
			rule := parseRuleBody(rest)
			rule.Category = category
			rule.Path = path
			rule.Line = lineNum
			_ = marker
			if rule.Text == "" {
				return nil, fmt.Errorf("%s:%d: rule has empty claim text", path, lineNum)
			}
			out = append(out, rule)

		case strings.HasPrefix(line, "  ") && len(out) > 0:
			// Continuation of the previous rule's reason.
			tail := strings.TrimSpace(line)
			if tail == "" {
				continue
			}
			last := &out[len(out)-1]
			if last.Reason == "" {
				last.Reason = tail
			} else {
				last.Reason += " " + tail
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// detectMarker checks whether body begins with a forbidden marker
// (with or without a trailing space). Returns the marker, the body
// past it, and a found flag.
func detectMarker(body string) (string, string, bool) {
	for _, m := range forbiddenMarkers {
		if strings.HasPrefix(body, m+" ") {
			return m, strings.TrimSpace(body[len(m)+1:]), true
		}
		if strings.HasPrefix(body, m) && len(body) == len(m) {
			return m, "", true
		}
	}
	return "", "", false
}

// parseRuleBody splits a body like `"We are HIPAA-compliant" — We are
// NOT HIPAA-certified.` into Text and Reason. The separator is " — "
// (em-dash), " -- " (double-hyphen), or " - " (en-dash / hyphen with
// spaces). If the text is quoted, the quotes are stripped.
//
// An optional `{fuzz: N}` or `{fuzz: exact}` annotation may appear at
// the very end of the body (after the reason, if any). Parsed
// independently and stripped from the visible Text/Reason fields:
//
//   - ❌ "We are HIPAA-compliant" — Not certified. {fuzz: 5}
//   - ❌ "Mobile app launching" {fuzz: exact}
//
// Unparseable annotations are silently ignored — the rule falls back
// to DefaultFuzzThreshold so a typo doesn't break the rule entirely.
func parseRuleBody(body string) Rule {
	body = strings.TrimSpace(body)
	body, fuzz := extractFuzzAnnotation(body)

	rule := Rule{FuzzThreshold: fuzz}

	// Look for a separator.
	for _, sep := range []string{" — ", " -- ", " - "} {
		if idx := strings.Index(body, sep); idx >= 0 {
			rule.Text = unquote(strings.TrimSpace(body[:idx]))
			rule.Reason = strings.TrimSpace(body[idx+len(sep):])
			return rule
		}
	}
	rule.Text = unquote(body)
	return rule
}

// extractFuzzAnnotation looks for `{fuzz: ...}` at the end of body.
// Returns the body with the annotation stripped and the resolved
// threshold (DefaultFuzzThreshold when no annotation present, 0 for
// `exact`, or the parsed integer for `{fuzz: N}`). Unparseable
// annotation strings are silently dropped — the rule falls back to
// the default rather than losing the rule entirely.
func extractFuzzAnnotation(body string) (string, int) {
	body = strings.TrimSpace(body)
	if !strings.HasSuffix(body, "}") {
		return body, DefaultFuzzThreshold
	}
	open := strings.LastIndex(body, "{")
	if open < 0 {
		return body, DefaultFuzzThreshold
	}
	annotation := body[open+1 : len(body)-1]
	rest := strings.TrimRight(body[:open], " ")

	parts := strings.SplitN(annotation, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) != "fuzz" {
		// Not a fuzz annotation; leave the braces as part of the
		// rule text. Defensive — operators may write {something}
		// for other reasons.
		return body, DefaultFuzzThreshold
	}

	val := strings.TrimSpace(parts[1])
	if val == "exact" {
		return rest, 0
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 0 {
		// Unparseable threshold → fall back to default. Don't
		// strip the annotation in this case so the operator sees
		// their typo on a re-read.
		return body, DefaultFuzzThreshold
	}
	return rest, n
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}
