package groundtruth

import (
	"context"
	"fmt"
	"strings"

	"github.com/jasondillingham/leonard/internal/adapters"
	"github.com/jasondillingham/leonard/internal/config"
)

// PreEdit implements the v0.7 hard guard. When the proposed edit
// would write content matching a do-not-claim.md rule, the adapter
// returns Deny with the rule citation — IF the operator has granted
// trust via `leonard config trust ground-truth`. Untrusted projects
// see a warning to stderr but the edit proceeds (so an unconfigured
// install never accidentally blocks an operator from working).
//
// Guards (mirror PostEdit):
//   - empty Content → Pass (no claim to check)
//   - no rules loaded → Pass (truth tree empty)
//   - file outside project root → Pass
//   - file path doesn't match verify_targets → Pass
//
// Hook errors are returned to the dispatcher so a panic-class
// failure (config trust I/O blowing up) is visible upstream rather
// than silently degrading the guard.
func (a *GroundTruthAdapter) PreEdit(_ context.Context, p adapters.PreEditPayload) (adapters.PreEditResult, error) {
	snap := a.snapshot()

	// #15 path_filters: check the proposed file path against
	// forbidden-target rules BEFORE checking content. A blocked
	// path makes the content-based check moot.
	if verdict, ok := evalPathFilters(snap, p); ok {
		return verdict, nil
	}

	// #16 content_filters: check whether content triggers a required-
	// disclosure rule. Runs before the do-not-claim hard guard so a
	// disclosure miss is the verdict rather than a forbidden-claim
	// hit on the same text.
	if verdict, ok := evalContentFilters(snap, p); ok {
		return verdict, nil
	}

	if len(snap.rules) == 0 {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// Bash tool calls set Command instead of Content. When the command
	// contains mutation patterns (>, >>, sed -i, tee) the forbidden-
	// claim text appears in the command body itself (echo argument,
	// heredoc, sed replacement string, etc.) and the detector should
	// run against it. Read-only commands (no mutation pattern) pass
	// without scanning — they can't write new content to files.
	//
	// Bughunt-12 F046: the prior early-return on empty Content fired
	// before this branch, letting every Bash command bypass the guard.
	textToScan := p.Content
	if textToScan == "" && p.Tool == "Bash" && bashCommandMutates(p.Command) {
		textToScan = p.Command
	}
	if textToScan == "" {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// File path is optional for some tool types (Bash carries
	// Command instead). When set, apply the same path-trust +
	// verify-target gating as PostEdit. When empty, fall through
	// to detection — Bash commands containing forbidden text still
	// deserve the guard.
	if p.FilePath != "" {
		absPath := p.FilePath
		if !pathIsAbsolute(absPath) {
			absPath = joinUnderRoot(snap.projectRoot, absPath)
		}
		if !insideProject(snap.projectRoot, absPath) {
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
		if !matchesAnyGlob(absPath, snap.cfg.VerifyTargets) {
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
		// #84 / #8: files inside truth_dir, plus operator-
		// configured exempt_paths globs, skip the matcher. The
		// truth tree's own files contain the rule bodies as data
		// — running the matcher against them creates a self-
		// blocking loop on the operator's own rule content.
		if isExemptFromMatcher(absPath, snap.projectRoot, snap.truthDir, snap.cfg.ExemptPaths) {
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
	}

	res := Detect(textToScan, snap.facts, snap.rules)
	if res.Summary.Forbidden == 0 {
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// Find the first forbidden hit to surface in the deny reason.
	var hit *Claim
	for i := range res.Claims {
		if res.Claims[i].Verdict == VerdictForbidden {
			hit = &res.Claims[i]
			break
		}
	}

	trusted, err := config.AdapterTrusted(snap.projectRoot, Name)
	if err != nil {
		// Trust check failed (symlink defense, I/O error). Fail-
		// closed: don't block on a check we can't perform, but
		// surface the error to the dispatcher so the operator
		// notices.
		fmt.Fprintf(snap.stderr, "leonard: ground-truth pre-edit: trust check failed, falling through to Pass: %v\n", err)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}
	if !trusted {
		fmt.Fprintf(snap.stderr,
			"leonard: ground-truth pre-edit: forbidden claim %q would have been rejected; run `leonard config trust ground-truth` to enable blocking. Proceeding.\n",
			hit.Text,
		)
		return adapters.PreEditResult{Decision: adapters.Pass}, nil
	}

	// Override token: a `leonard override --once` token issued for this
	// specific file bypasses the forbidden-claim deny. The token is keyed
	// on the project-relative path. Bash commands (no FilePath) can't
	// use this escape hatch — they'd need to be rewritten instead.
	if p.FilePath != "" {
		rel := relForFilters(snap.projectRoot, p.FilePath)
		if consumeOverrideToken(snap.projectRoot, rel) {
			fmt.Fprintf(snap.stderr,
				"leonard: ground-truth: forbidden-claim guard bypassed for %s via `leonard override --once`\n",
				rel,
			)
			return adapters.PreEditResult{Decision: adapters.Pass}, nil
		}
	}

	reason := buildDenyReason(hit, textToScan, len(res.Claims))
	return adapters.PreEditResult{
		Decision:    adapters.Deny,
		Reason:      reason,
		AdapterName: Name,
	}, nil
}

// bashCommandMutates reports whether a Bash command string contains
// shell constructs that write new content to files: redirection
// operators (> or >>), in-place sed (-i flag), or tee. mv is excluded
// because it renames rather than writes, so the command text cannot
// contain new forbidden content destined for the target file.
//
// Commands without mutation patterns are read-only and pass without
// scanning — they can't write new content to files.
//
// Bughunt-12 F046: used by PreEdit to gate the Bash command scan.
func bashCommandMutates(command string) bool {
	if command == "" {
		return false
	}
	if strings.Contains(command, ">") {
		return true
	}
	lower := strings.ToLower(command)
	if strings.Contains(lower, "sed") && strings.Contains(lower, "-i") {
		return true
	}
	if strings.Contains(lower, "tee ") || strings.Contains(lower, "|tee") {
		return true
	}
	return false
}

// buildDenyReason renders a deny message that includes:
//   - the matched claim text
//   - the source rule body (#88 — operator doesn't need to open
//     do-not-claim.md to understand the block)
//   - byte offset + 1-based line number where the match starts
//     (#86 — operator can localize the offending entry in a
//     multi-entry insertion, e.g. when 4 watchlist entries get
//     blocked because of one substring in one of them)
//   - "this is N of M findings" hint when there are multiple
//     forbidden hits in the same payload (#86 — the operator
//     should know whether fixing this one will reveal another)
//
// The rule body is truncated to ruleSnippetMax chars to keep the
// permissionDecisionReason field at a reasonable size.
func buildDenyReason(hit *Claim, content string, totalForbidden int) string {
	loc := locateInContent(content, hit.StartByte)
	reason := strings.TrimSpace(hit.RuleReason)
	if len(reason) > ruleSnippetMax {
		reason = reason[:ruleSnippetMax-1] + "…"
	}
	tail := ""
	if totalForbidden > 1 {
		tail = fmt.Sprintf(" (1 of %d forbidden findings in this edit)", totalForbidden)
	}
	if reason == "" {
		return fmt.Sprintf("Forbidden claim %q at %s matches rule %s in do-not-claim.md.%s Rewrite to avoid the claim or update the rule if it is wrong.",
			hit.Text, loc, hit.RulePath, tail)
	}
	return fmt.Sprintf("Forbidden claim %q at %s matches rule %s in do-not-claim.md — %s.%s Rewrite to avoid the claim or update the rule if it is wrong.",
		hit.Text, loc, hit.RulePath, reason, tail)
}

// locateInContent renders the offset of a match into a "line N,
// col M" string for operator readability. Uses 1-based line/col
// indices (matching the convention every editor displays).
//
// When content is empty (e.g. some Bash invocations), returns just
// the byte offset since line/col make no sense.
func locateInContent(content string, byteOff int) string {
	if content == "" {
		return fmt.Sprintf("byte %d", byteOff)
	}
	if byteOff < 0 {
		byteOff = 0
	}
	if byteOff > len(content) {
		byteOff = len(content)
	}
	line, col := 1, 1
	for i := 0; i < byteOff; i++ {
		if content[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return fmt.Sprintf("line %d col %d", line, col)
}

// ruleSnippetMax caps how much of the rule body we splice into the
// deny reason. 200 chars handles most rules (typical do-not-claim
// entries are short bullets) while keeping the wire-level reason
// field reasonable.
const ruleSnippetMax = 200

// pathIsAbsolute is a tiny local helper to avoid a filepath import
// in this file. The post_edit.go file already imports filepath for
// its own absPath logic; we keep this file lean.
func pathIsAbsolute(p string) bool {
	return len(p) > 0 && (p[0] == '/' || (len(p) > 1 && p[1] == ':'))
}

// joinUnderRoot joins a relative path under root. Pulled out so the
// PreEdit path-resolution mirrors PostEdit's shape without sharing
// import lines.
func joinUnderRoot(root, rel string) string {
	if root == "" {
		return rel
	}
	return root + "/" + rel
}
