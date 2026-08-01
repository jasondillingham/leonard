package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSettings drops a settings.local.json into root's .claude/ dir.
func writeSettings(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.local.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findingsWith(findings []WiringFinding, sev wiringSeverity) []WiringFinding {
	var out []WiringFinding
	for _, f := range findings {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
}

const goodSettings = `{
  "hooks": {
    "PreToolUse":  [{"matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
    "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
  }
}`

func TestCheckHookWiring_HealthyConfigIsSilent(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, goodSettings)
	if got := checkHookWiring(root); len(got) != 0 {
		t.Fatalf("expected no findings on a correct config, got %+v", got)
	}
}

// The regression that motivated this check: bosun, tracecast, and Leonard's
// own repo were all found with a matcher omitting Bash (#101), silently
// reopening bughunt-7 F2.
func TestCheckHookWiring_MissingBashIsSecurityFinding(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "hooks": {
        "PreToolUse":  [{"matcher": "Edit|Write", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	got := checkHookWiring(root)
	sec := findingsWith(got, wiringSecurity)
	if len(sec) != 1 {
		t.Fatalf("expected exactly 1 SECURITY finding, got %d: %+v", len(sec), got)
	}
	if !strings.Contains(sec[0].Message, "Bash") || !strings.Contains(sec[0].Message, "bughunt-7 F2") {
		t.Errorf("security finding should name Bash and the CVE-equivalent: %q", sec[0].Message)
	}
	// The same matcher also omits MultiEdit/NotebookEdit — that's a
	// coverage warning, and must not be duplicated into the security line.
	warns := findingsWith(got, wiringWarn)
	if len(warns) != 1 || strings.Contains(warns[0].Message, "Bash") {
		t.Errorf("expected one non-Bash coverage warning, got %+v", warns)
	}
}

// F1 (CRITICAL regression): escaped pipes force Claude Code into regex mode,
// where `\|` is a literal pipe, so the matcher matches NO real tool and the
// hook fires for nothing. The old code split on `|`, saw a bare trailing
// "Bash", and reported it covered — affirmatively telling the operator they
// were protected when they were not. This is the worst failure this file can
// have, and it must produce a SECURITY finding.
func TestCheckHookWiring_EscapedPipeRegexIsSecurityFinding(t *testing.T) {
	root := t.TempDir()
	// JSON "\\|" decodes to the Go/regex string "\|".
	writeSettings(t, root, `{
      "hooks": {
        "PreToolUse":  [{"matcher": "Edit\\|Write\\|MultiEdit\\|NotebookEdit\\|Bash", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	sec := findingsWith(checkHookWiring(root), wiringSecurity)
	if len(sec) != 1 || !strings.Contains(sec[0].Message, "Bash") {
		t.Fatalf("escaped-pipe regex should yield a Bash SECURITY finding, got %+v", checkHookWiring(root))
	}
}

// F3: in regex mode the match is case-sensitive, so a lowercase "bash" does
// not cover the "Bash" tool. (The `.*` forces regex mode.)
func TestCheckHookWiring_RegexModeIsCaseSensitive(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "hooks": {
        "PreToolUse":  [{"matcher": "Edit|Write|MultiEdit|Notebook.*|bash", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	sec := findingsWith(checkHookWiring(root), wiringSecurity)
	if len(sec) != 1 {
		t.Fatalf("regex-mode lowercase bash should not cover Bash; want 1 SECURITY finding, got %+v", checkHookWiring(root))
	}
}

// A regex matcher RE2 cannot compile must warn, never silently claim coverage
// (which would suppress a real SECURITY finding) and never assert a gap.
func TestCheckHookWiring_UncompilableRegexWarns(t *testing.T) {
	root := t.TempDir()
	// A JS lookahead — valid there, rejected by RE2.
	writeSettings(t, root, `{
      "hooks": {
        "PreToolUse":  [{"matcher": "Bash(?=x)", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	got := checkHookWiring(root)
	if len(findingsWith(got, wiringSecurity)) != 0 {
		t.Errorf("an uncompilable matcher must not assert a SECURITY finding: %+v", got)
	}
	found := false
	for _, f := range got {
		if f.Severity == wiringWarn && strings.Contains(f.Message, "could not be evaluated") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an evaluation warning, got %+v", got)
	}
}

// F2: a command whose PATH contains "pre-edit" but is not a pre-edit hook must
// not suppress the "no pre-edit entry" warning.
func TestCheckHookWiring_CommandPathContainingSubcommand(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "hooks": {
        "PreToolUse":  [{"matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash", "hooks": [{"command": "/opt/pre-edit-tools/leonard-hook session-start"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	got := checkHookWiring(root)
	found := false
	for _, f := range got {
		if strings.Contains(f.Message, "no `leonard-hook pre-edit` entry") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a session-start command in a pre-edit-named path must not count as a pre-edit hook: %+v", got)
	}
}

// F7: the security-relevant finding must sort ahead of the mcpServers warning,
// which is appended first, so it isn't buried.
func TestCheckHookWiring_SecuritySortsFirst(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "mcpServers": {"leonard": {"command": "/go/bin/leonard-mcp"}},
      "hooks": {
        "PreToolUse":  [{"matcher": "Edit|Write", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	got := checkHookWiring(root)
	if len(got) == 0 || got[0].Severity != wiringSecurity {
		t.Fatalf("SECURITY finding should sort first, got %+v", got)
	}
}

// An empty matcher means "all tools" in Claude Code. Flagging it would make
// doctor scream at the most permissive config there is.
func TestCheckHookWiring_MatchAllMatchersAreComplete(t *testing.T) {
	for _, m := range []string{"", "*", ".*", "   "} {
		root := t.TempDir()
		writeSettings(t, root, `{
          "hooks": {
            "PreToolUse":  [{"matcher": "`+m+`", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
            "PostToolUse": [{"matcher": "`+m+`", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
          }
        }`)
		if got := checkHookWiring(root); len(got) != 0 {
			t.Errorf("matcher %q should be treated as full coverage, got %+v", m, got)
		}
	}
}

// Exact-mode coverage: whole-token comparison (so "Edit" never satisfies
// "NotebookEdit"), case-insensitive, and both `|` and `,` separators with
// optional surrounding whitespace — all per the hooks docs.
func TestMissingTools_ExactMode(t *testing.T) {
	full := requiredPreEditTools
	cases := []struct {
		matcher    string
		wantMiss   []string
		understood bool
	}{
		{"Edit|Write", []string{"MultiEdit", "NotebookEdit", "Bash"}, true},
		{"Edit|Write|MultiEdit|NotebookEdit|Bash", nil, true},
		// Substring must not satisfy: Edit does not cover NotebookEdit.
		{"Edit|Write|MultiEdit|Bash", []string{"NotebookEdit"}, true},
		// Case-insensitive in exact mode (documented).
		{"edit|write|multiedit|notebookedit|bash", nil, true},
		{"EDIT|WRITE|MULTIEDIT|NOTEBOOKEDIT|BASH", nil, true},
		// Comma separators, and mixed with pipes, with whitespace (F5).
		{"Edit,Write,MultiEdit,NotebookEdit,Bash", nil, true},
		{"Edit, Write, MultiEdit, NotebookEdit, Bash", nil, true},
		{"Edit | Write | MultiEdit | NotebookEdit | Bash", nil, true},
		{"Edit,Write | MultiEdit,NotebookEdit|Bash", nil, true},
	}
	for _, tc := range cases {
		miss, understood := missingTools(tc.matcher, full)
		if understood != tc.understood {
			t.Errorf("missingTools(%q) understood=%v, want %v", tc.matcher, understood, tc.understood)
		}
		if !equalStringSet(miss, tc.wantMiss) {
			t.Errorf("missingTools(%q) missing=%v, want %v", tc.matcher, miss, tc.wantMiss)
		}
	}
}

// Match-all spellings. "" and "*" are the documented literals; ".*" reaches
// the same answer through the regex path (F4: a match-all must be recognized
// even when it is only part of an alternation).
func TestMissingTools_MatchAll(t *testing.T) {
	for _, m := range []string{"", "*", "   ", ".*", "Edit|Write|.*", "(Edit|Write|MultiEdit|NotebookEdit|Bash)", "Edit|Write|MultiEdit|NotebookEdit|Bash.*"} {
		miss, understood := missingTools(m, requiredPreEditTools)
		if !understood || len(miss) != 0 {
			t.Errorf("missingTools(%q) = (%v, %v), want full coverage", m, miss, understood)
		}
	}
}

// equalStringSet compares two slices as sets (order-independent).
func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestCheckHookWiring_FlagsMCPServersInSettings(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "mcpServers": {"leonard": {"command": "/go/bin/leonard-mcp"}},
      "hooks": {
        "PreToolUse":  [{"matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	got := checkHookWiring(root)
	if len(got) != 1 || !strings.Contains(got[0].Message, "mcpServers") {
		t.Fatalf("expected an mcpServers finding, got %+v", got)
	}
}

func TestCheckHookWiring_MissingFileIsInfoNotError(t *testing.T) {
	got := checkHookWiring(t.TempDir())
	if len(got) != 1 || got[0].Severity != wiringInfo {
		t.Fatalf("a project with no settings file should produce one info finding, got %+v", got)
	}
}

func TestCheckHookWiring_InvalidJSONIsReported(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{"hooks": {`)
	got := checkHookWiring(root)
	if len(got) != 1 || !strings.Contains(got[0].Message, "not valid JSON") {
		t.Fatalf("expected a parse finding, got %+v", got)
	}
}

// Other tools' hooks share the file; Leonard must only judge its own.
func TestCheckHookWiring_IgnoresNonLeonardHooks(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "hooks": {
        "PreToolUse": [
          {"matcher": "Edit", "hooks": [{"command": "/usr/bin/some-other-tool check"}]},
          {"matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash", "hooks": [{"command": "/go/bin/leonard-hook pre-edit"}]}
        ],
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	if got := checkHookWiring(root); len(got) != 0 {
		t.Fatalf("another tool's narrow matcher must not be reported, got %+v", got)
	}
}

func TestCheckHookWiring_MissingPreEditEntirely(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
      "hooks": {
        "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"command": "/go/bin/leonard-hook post-edit"}]}]
      }
    }`)
	got := checkHookWiring(root)
	if len(got) != 1 || !strings.Contains(got[0].Message, "no `leonard-hook pre-edit` entry") {
		t.Fatalf("expected a missing-pre-edit finding, got %+v", got)
	}
}

func TestRenderWiringFindings_SilentWhenClean(t *testing.T) {
	var buf bytes.Buffer
	renderWiringFindings(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("healthy project should print nothing, got %q", buf.String())
	}
}

func TestRenderWiringFindings_LabelsSeverity(t *testing.T) {
	var buf bytes.Buffer
	renderWiringFindings(&buf, []WiringFinding{
		{Severity: wiringSecurity, Message: "matcher omits Bash", Detail: "guard never runs"},
	})
	out := buf.String()
	if !strings.Contains(out, "[SECURITY]") || !strings.Contains(out, "guard never runs") {
		t.Errorf("expected labeled finding with detail, got %q", out)
	}
}
