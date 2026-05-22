package groundtruth

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// Rule is one entry from do-not-claim.md. v0.6 captures the rule's
// text, the section heading it lives under, and the line number for
// later issues' error messages. The hook code that matches proposed
// text against these rules lands in #23 (hard reject).
type Rule struct {
	// Category is the "## <heading>" the rule appears under
	// (e.g., "Product capability gaps").
	Category string

	// Text is the forbidden claim text. Stripped of leading "- ❌ "
	// markers and trimmed; matches still happen against this exact
	// text in v0.6's exact-string matcher, and against fuzzy variants
	// once #13 lands.
	Text string

	// Reason is the explanation that follows the claim (separated
	// by " — " or " - " in the source). Surfaced to Claude as the
	// rule citation on deny.
	Reason string

	// Path / Line locate the rule in do-not-claim.md for error
	// messages. Line is 1-based.
	Path string
	Line int
}

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
func parseRuleBody(body string) Rule {
	body = strings.TrimSpace(body)

	// Look for a separator.
	for _, sep := range []string{" — ", " -- ", " - "} {
		if idx := strings.Index(body, sep); idx >= 0 {
			return Rule{
				Text:   unquote(strings.TrimSpace(body[:idx])),
				Reason: strings.TrimSpace(body[idx+len(sep):]),
			}
		}
	}
	return Rule{Text: unquote(body)}
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}
