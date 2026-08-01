package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// exactListMatcher recognizes Claude Code's "exact list" matcher mode: a
// matcher containing only letters, digits, `_`, `-`, space, `,`, and `|` is
// a case-insensitive list of exact tool names. Any character outside this set
// (`.`, `*`, `(`, `^`, `\`, …) switches Claude Code to unanchored regex mode.
// The set is taken verbatim from the hooks documentation.
var exactListMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]*$`)

// Leonard's guards only run for the tools the operator wired into the
// Claude Code hook matcher. That makes the matcher a security control in
// its own right, and a silent one: when it's wrong nothing warns, nothing
// degrades, and nothing logs — the guard simply never executes.
//
// Issue #101: an audit of one machine found 3 of 7 wired projects (including
// Leonard's own repo) with a pre-edit matcher that omitted Bash, silently
// reopening bughunt-7 F2 — a HIGH where a shell redirection could write into
// the operator-protected data directory in a single tool call.
//
// These checks are read-only and filesystem-based; they deliberately do not
// touch the store, so `doctor` can report wiring problems even when the
// index is empty or stale.
const (
	settingsRelPath = ".claude/settings.local.json"
	mcpRelPath      = ".mcp.json"
)

// wiringSeverity orders findings so the security-relevant ones sort first
// and can be rendered distinctly.
type wiringSeverity int

const (
	wiringSecurity wiringSeverity = iota
	wiringWarn
	wiringInfo
)

func (s wiringSeverity) label() string {
	switch s {
	case wiringSecurity:
		return "SECURITY"
	case wiringWarn:
		return "warn"
	default:
		return "info"
	}
}

// WiringFinding is one problem found in the Claude Code hook wiring.
type WiringFinding struct {
	Severity wiringSeverity
	Message  string
	// Detail is an optional second line explaining the consequence. Kept
	// separate so the renderer can indent it consistently.
	Detail string
}

// requiredPreEditTools are the tools the pre-edit guard must see to do its
// whole job. Bash is called out separately because dropping it is a
// security regression rather than a coverage gap.
var requiredPreEditTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "Bash"}

// requiredPostEditTools are the tools whose edits must be re-indexed. An
// omission here silently skips indexing rather than skipping a guard, so
// it's a correctness problem, not a security one.
var requiredPostEditTools = []string{"Edit", "Write", "MultiEdit"}

// checkHookWiring inspects root's Claude Code configuration and reports
// wiring problems. A missing settings file is reported as info, not an
// error — plenty of projects use Leonard purely through the CLI.
func checkHookWiring(root string) []WiringFinding {
	path := filepath.Join(root, filepath.FromSlash(settingsRelPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []WiringFinding{{
				Severity: wiringInfo,
				Message:  fmt.Sprintf("no %s — Leonard's hooks are not wired into Claude Code here", settingsRelPath),
				Detail:   "CLI and MCP still work; the pre-edit/post-edit guards do not run.",
			}}
		}
		return []WiringFinding{{
			Severity: wiringWarn,
			Message:  fmt.Sprintf("could not read %s: %v", settingsRelPath, err),
		}}
	}

	var cfg struct {
		MCPServers map[string]any `json:"mcpServers"`
		Hooks      map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return []WiringFinding{{
			Severity: wiringWarn,
			Message:  fmt.Sprintf("%s is not valid JSON: %v", settingsRelPath, err),
			Detail:   "Claude Code will reject the whole file, so no hooks run at all.",
		}}
	}

	var out []WiringFinding

	// #98: mcpServers is no longer accepted in settings.local.json. This is
	// not a cosmetic problem — a schema-invalid settings file is rejected
	// wholesale, taking the hooks down with it. Leonard then enforces
	// nothing, silently, which looks identical to "Leonard is fine".
	if len(cfg.MCPServers) > 0 {
		out = append(out, WiringFinding{
			Severity: wiringWarn,
			Message:  fmt.Sprintf("%s contains `mcpServers` — current Claude Code rejects that key (#98)", settingsRelPath),
			Detail:   fmt.Sprintf("A rejected settings file disables the hooks too. Move it to %s.", mcpRelPath),
		})
	}

	preMatchers := matchersFor(cfg.Hooks["PreToolUse"], "pre-edit")
	postMatchers := matchersFor(cfg.Hooks["PostToolUse"], "post-edit")

	if len(preMatchers) == 0 {
		out = append(out, WiringFinding{
			Severity: wiringWarn,
			Message:  "no `leonard-hook pre-edit` entry under PreToolUse",
			Detail:   "The fabrication guard and the protected-directory guard never run.",
		})
	}
	for _, m := range preMatchers {
		missing, understood := missingTools(m, requiredPreEditTools)
		if !understood {
			out = append(out, WiringFinding{
				Severity: wiringWarn,
				Message:  fmt.Sprintf("PreToolUse matcher %q could not be evaluated (regex did not compile)", m),
				Detail:   "Verify by hand that it fires for Bash — the protected-directory guard depends on it.",
			})
			continue
		}
		if slicesContains(missing, "Bash") {
			out = append(out, WiringFinding{
				Severity: wiringSecurity,
				Message:  fmt.Sprintf("PreToolUse matcher %q omits Bash — bughunt-7 F2 is reopened", m),
				Detail:   "pre-edit scans Bash command strings for writes into the protected data directory. Without Bash in the matcher that check never runs and nothing warns you.",
			})
		}
		if rest := withoutBash(missing); len(rest) > 0 {
			out = append(out, WiringFinding{
				Severity: wiringWarn,
				Message:  fmt.Sprintf("PreToolUse matcher %q omits %s", m, strings.Join(rest, ", ")),
				Detail:   "Edits made through those tools bypass the pre-edit guard.",
			})
		}
	}

	if len(postMatchers) == 0 {
		out = append(out, WiringFinding{
			Severity: wiringWarn,
			Message:  "no `leonard-hook post-edit` entry under PostToolUse",
			Detail:   "Edits are never re-indexed, so the symbol index drifts from the tree.",
		})
	}
	for _, m := range postMatchers {
		missing, understood := missingTools(m, requiredPostEditTools)
		if !understood {
			out = append(out, WiringFinding{
				Severity: wiringWarn,
				Message:  fmt.Sprintf("PostToolUse matcher %q could not be evaluated (regex did not compile)", m),
				Detail:   "Verify by hand that it fires for Edit/Write/MultiEdit, or edits won't be re-indexed.",
			})
			continue
		}
		if len(missing) > 0 {
			out = append(out, WiringFinding{
				Severity: wiringWarn,
				Message:  fmt.Sprintf("PostToolUse matcher %q omits %s", m, strings.Join(missing, ", ")),
				Detail:   "Edits made through those tools are not re-indexed.",
			})
		}
	}

	// Surface the security-relevant findings first. Without this the
	// mcpServers warning (appended above) can push a [SECURITY] line below
	// it, burying exactly the thing the operator most needs to see.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity < out[j].Severity })
	return out
}

// matchersFor returns the matcher strings of every hook group that invokes
// `leonard-hook <sub>`. A project may legitimately register several groups
// (and other tools' hooks alongside Leonard's), so each Leonard group is
// checked independently and non-Leonard groups are ignored entirely.
//
// The command is parsed into whitespace-separated fields rather than
// substring-matched: a command like `/opt/pre-edit-tools/leonard-hook
// session-start` contains the text "pre-edit" in its path but is not a
// pre-edit hook, and the old substring test misclassified it (F2). We require
// a field whose basename is `leonard-hook` immediately followed by the exact
// subcommand token.
func matchersFor(groups []struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Command string `json:"command"`
	} `json:"hooks"`
}, sub string) []string {
	var out []string
	for _, g := range groups {
		for _, h := range g.Hooks {
			if commandInvokes(h.Command, sub) {
				out = append(out, g.Matcher)
				break
			}
		}
	}
	return out
}

// commandInvokes reports whether cmd runs `leonard-hook <sub>` as an actual
// argv token pair, not merely as a substring of some path.
func commandInvokes(cmd, sub string) bool {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if filepath.Base(f) != "leonard-hook" {
			continue
		}
		if i+1 < len(fields) && fields[i+1] == sub {
			return true
		}
	}
	return false
}

// missingTools reports which of want the matcher fails to cover, and whether
// the matcher could be evaluated at all. Claude Code evaluates matchers in
// two modes (per the hooks docs):
//
//   - "" or "*" — match every tool. ("*" is not a valid regex, so Claude
//     special-cases it; ".*" is NOT special-cased here — it falls through to
//     the regex path, where it matches everything anyway. Handling every
//     match-all spelling in one place avoids the F4 bug where a match-all
//     buried in an alternation, e.g. "Edit|.*", was missed.)
//   - only [A-Za-z0-9_- ,|] — a case-INsensitive list of exact tool names,
//     separated by `|` or `,`, surrounding whitespace optional.
//   - anything else — an unanchored, case-SENSITIVE JavaScript regex.
//
// understood is false only when the matcher is a regex that will not compile.
// In that case the caller must warn rather than assert coverage either way:
// claiming full coverage would silently suppress a real SECURITY finding
// (this is the F1 failure mode in another form), and claiming a gap would cry
// wolf on a matcher we simply don't understand.
func missingTools(matcher string, want []string) (missing []string, understood bool) {
	m := strings.TrimSpace(matcher)
	if m == "" || m == "*" {
		return nil, true
	}

	if exactListMatcher.MatchString(m) {
		present := map[string]bool{}
		for _, tok := range splitExactList(m) {
			present[strings.ToLower(tok)] = true
		}
		for _, tool := range want {
			if !present[strings.ToLower(tool)] {
				missing = append(missing, tool)
			}
		}
		return missing, true
	}

	// Regex mode. Go's regexp (RE2) is unanchored via MatchString and
	// case-sensitive by default — the same defaults as the JS regex Claude
	// Code uses. RE2 rejects a few JS constructs (lookahead, backreferences);
	// those surface as a compile failure and become an "understood == false"
	// warning rather than a wrong verdict.
	re, err := regexp.Compile(m)
	if err != nil {
		return nil, false
	}
	for _, tool := range want {
		if !re.MatchString(tool) {
			missing = append(missing, tool)
		}
	}
	return missing, true
}

// splitExactList splits an exact-mode matcher on both documented separators
// (`|` and `,`) and trims surrounding whitespace from each token, dropping
// empties. `Edit, Write | Bash` yields [Edit Write Bash].
func splitExactList(matcher string) []string {
	fields := strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func withoutBash(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != "Bash" {
			out = append(out, t)
		}
	}
	return out
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// renderWiringFindings prints the Wiring section. Nothing is printed when
// there is nothing to say, so a healthy project's doctor output stays terse.
func renderWiringFindings(out interface {
	Write([]byte) (int, error)
}, findings []WiringFinding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(out, "\nWiring\n")
	for _, f := range findings {
		fmt.Fprintf(out, "  [%s] %s\n", f.Severity.label(), f.Message)
		if f.Detail != "" {
			fmt.Fprintf(out, "    %s\n", f.Detail)
		}
	}
}
